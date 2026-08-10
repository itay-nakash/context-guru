package dash

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures the dashboard. The zero value is usable: an in-memory
// database, content capture on, 7-day / 512 MiB retention, loopback-only access
// to per-request content and effective config.
type Options struct {
	// DBPath is the SQLite file. "" or ":memory:" keeps everything in RAM (the
	// no-persistence mode), which is also the automatic fallback when the path
	// cannot be opened — the proxy must never fail to start over a dashboard.
	DBPath string
	// Retention bounds the store by age AND size. Zero values use the defaults
	// below; a negative value disables that rule.
	RetentionAge   time.Duration
	RetentionBytes int64
	// CaptureContent enables the before/after content capture the diff view needs.
	// Opt-OUT: it is the headline feature, so it defaults on. ContentCap bounds each
	// captured blob (default 16 KiB); ContentMaxPerRequest bounds how many
	// rewritten messages are captured per request (default 24).
	CaptureContent       bool
	ContentCap           int
	ContentMaxPerRequest int
	// QueueSize is the capture channel's depth (default 4096). When it is full,
	// events are DROPPED and counted rather than blocking a request.
	QueueSize int
	// BatchSize / FlushInterval control how the writer batches inserts.
	BatchSize     int
	FlushInterval time.Duration
	// TrustedCIDRs are the networks allowed to see per-request CONTENT and the
	// effective configuration. Loopback is always allowed. Aggregates are open to
	// everyone (a proxy people bind to 0.0.0.0 still wants its numbers visible).
	TrustedCIDRs []string
	// Preset / Mode label captured rows so the UI can filter by configuration.
	Preset string
	Mode   string
	// Effective is the resolved, already-structured configuration to serve at
	// /api/config. It is redacted before serving; nothing sensitive should be in
	// here in the first place.
	Effective map[string]any
	// BenchDirs are directories scanned for harbor benchmark runs (summary.json +
	// rows-*.json) at startup and on demand.
	BenchDirs []string
}

const (
	defaultQueueSize      = 4096
	defaultBatchSize      = 128
	defaultFlushInterval  = 250 * time.Millisecond
	defaultRetentionAge   = 7 * 24 * time.Hour
	defaultRetentionBytes = 512 << 20
	defaultContentCap     = 16 << 10
	defaultContentPerReq  = 24
	pruneInterval         = 5 * time.Minute
)

func (o *Options) withDefaults() {
	if o.QueueSize <= 0 {
		o.QueueSize = defaultQueueSize
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = defaultFlushInterval
	}
	if o.RetentionAge == 0 {
		o.RetentionAge = defaultRetentionAge
	}
	if o.RetentionBytes == 0 {
		o.RetentionBytes = defaultRetentionBytes
	}
	if o.ContentCap == 0 {
		o.ContentCap = defaultContentCap
	}
	if o.ContentMaxPerRequest == 0 {
		o.ContentMaxPerRequest = defaultContentPerReq
	}
}

// Recorder is the capture pipeline: a buffered channel, one writer goroutine that
// batches inserts, and an SSE hub the writer fans summaries out to.
//
// The contract that matters: Record NEVER blocks and never returns an error. A
// full queue drops the event and increments a counter that the dashboard itself
// displays — an observability layer that silently lies about its own coverage is
// worse than one that admits a gap. This is why the dashboard cannot add request
// latency: the hot path does one channel send with a default branch.
type Recorder struct {
	db   *DB
	opts Options
	hub  *Hub

	ch   chan *Event
	done chan struct{}
	wg   sync.WaitGroup

	captured atomic.Int64
	dropped  atomic.Int64
	written  atomic.Int64
	errors   atomic.Int64

	// Cache-attribution state: the last time we saw each session and whether we
	// have seen each model, so a cold start is never reported as a bust.
	mu        sync.Mutex
	lastSeen  map[string]int64 // session -> epoch ms of previous request
	seenModel map[string]bool
	// perComp accumulates unique-savings dedup keys so a per-request unique figure
	// exists at capture time. Bounded; see markUnique.
	seenKeys map[string]struct{}
}

// NewRecorder opens the store and starts the writer goroutine. It never returns a
// fatal error for a bad path: an unopenable database degrades to in-memory, with a
// warning, because the proxy's job is to proxy.
func NewRecorder(opts Options) (*Recorder, error) {
	opts.withDefaults()
	db, err := Open(opts.DBPath)
	if err != nil {
		slog.Warn("dash: could not open the dashboard database; falling back to in-memory (history will not survive a restart)",
			"path", opts.DBPath, "err", err)
		db, err = Open(":memory:")
		if err != nil {
			return nil, err
		}
	}
	r := &Recorder{
		db:        db,
		opts:      opts,
		hub:       NewHub(),
		ch:        make(chan *Event, opts.QueueSize),
		done:      make(chan struct{}),
		lastSeen:  map[string]int64{},
		seenModel: map[string]bool{},
		seenKeys:  map[string]struct{}{},
	}
	r.wg.Add(1)
	go r.run()
	return r, nil
}

// DB exposes the store for queries (read-only use by the API).
func (r *Recorder) DB() *DB { return r.db }

// Opts exposes the effective options (read-only).
func (r *Recorder) Opts() Options { return r.opts }

// Hub exposes the SSE fan-out.
func (r *Recorder) Hub() *Hub { return r.hub }

// Record hands an event to the writer. It is safe from any goroutine, never
// blocks, and never fails: a full queue drops and counts. Callers must not touch
// the event afterwards — the writer owns it.
func (r *Recorder) Record(e *Event) {
	if r == nil || e == nil {
		return
	}
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	r.captured.Add(1)
	select {
	case r.ch <- e:
	default:
		r.dropped.Add(1)
	}
}

// Close stops the writer after draining what is already queued.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	close(r.done)
	r.wg.Wait()
	r.hub.Close()
	return r.db.Close()
}

// Stats reports the capture pipeline's own health — including its drops, which is
// the number that keeps every other number honest.
type Stats struct {
	Captured int64  `json:"captured"`
	Written  int64  `json:"written"`
	Dropped  int64  `json:"dropped"`
	Errors   int64  `json:"errors"`
	Queued   int    `json:"queued"`
	QueueCap int    `json:"queue_cap"`
	Clients  int    `json:"sse_clients"`
	DBPath   string `json:"db_path"`
	DBBytes  int64  `json:"db_bytes"`
}

// Stats snapshots the pipeline counters.
func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	size, _ := r.db.sizeBytes()
	return Stats{
		Captured: r.captured.Load(), Written: r.written.Load(),
		Dropped: r.dropped.Load(), Errors: r.errors.Load(),
		Queued: len(r.ch), QueueCap: cap(r.ch),
		Clients: r.hub.Clients(), DBPath: r.db.Path(), DBBytes: size,
	}
}

// run is the single writer goroutine: batch, insert in one transaction, fan out,
// and prune on a timer. Nothing else writes to the database.
func (r *Recorder) run() {
	defer r.wg.Done()
	batch := make([]*Event, 0, r.opts.BatchSize)
	flush := time.NewTicker(r.opts.FlushInterval)
	defer flush.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()

	write := func() {
		if len(batch) == 0 {
			return
		}
		if err := r.db.insertBatch(batch); err != nil {
			r.errors.Add(int64(len(batch)))
			slog.Warn("dash: dropping a batch of captured requests", "n", len(batch), "err", err)
		} else {
			r.written.Add(int64(len(batch)))
			for _, e := range batch {
				r.hub.Publish(e)
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case e := <-r.ch:
			batch = append(batch, e)
			if len(batch) >= r.opts.BatchSize {
				write()
			}
		case <-flush.C:
			write()
		case <-prune.C:
			if n, err := r.db.Prune(time.Now(), r.opts.RetentionAge, r.opts.RetentionBytes); err != nil {
				slog.Warn("dash: retention prune failed", "err", err)
			} else if n > 0 {
				slog.Info("dash: pruned old dashboard rows", "requests", n)
			}
		case <-r.done:
			// Drain whatever is queued so a clean shutdown does not lose the tail.
			for {
				select {
				case e := <-r.ch:
					batch = append(batch, e)
					if len(batch) >= r.opts.BatchSize {
						write()
					}
					continue
				default:
				}
				break
			}
			write()
			return
		}
	}
}

// Observe records the session/model facts needed for cache attribution and
// returns them. Called on the request path (one map lookup under a short mutex),
// before Record.
func (r *Recorder) Observe(session, model string, now int64) (seenSession, seenModel bool, sinceLastMs int64) {
	if r == nil {
		return true, true, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, seenSession := r.lastSeen[session]
	if seenSession {
		sinceLastMs = now - prev
	}
	seenModel = r.seenModel[model]
	// Bound both maps: a proxy runs for weeks and every distinct session id would
	// otherwise be retained forever. Sessions are keyed by content hash or client
	// id, so the working set is small; a reset just re-reports a cold start, which
	// is honest (we genuinely no longer know).
	// ponytail: crude clear-on-overflow; swap for an LRU if session churn ever matters.
	if len(r.lastSeen) > 20000 {
		r.lastSeen = map[string]int64{}
	}
	if len(r.seenModel) > 1000 {
		r.seenModel = map[string]bool{}
	}
	r.lastSeen[session] = now
	r.seenModel[model] = true
	return seenSession, seenModel, sinceLastMs
}

// MarkUnique attributes a component's savings to NEW content only, deduping by
// the content keys the component stashed — the same rule metrics.Aggregator uses,
// so the dashboard's unique figure and /stats' agree. Returns saved tokens
// attributable to content not seen before.
func (r *Recorder) MarkUnique(component string, keys []string, saved int) int {
	if r == nil || saved <= 0 {
		return 0
	}
	if len(keys) == 0 {
		return saved // no key to dedup on: count the run once (Aggregator's rule)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// ponytail: clear-on-overflow, same reasoning as Observe. Over-reporting a
	// repeat as unique after a reset is bounded and visible via overcount_ratio.
	if len(r.seenKeys) > 200000 {
		r.seenKeys = map[string]struct{}{}
	}
	newKeys := 0
	for _, k := range keys {
		ck := component + "\x00" + k
		if _, seen := r.seenKeys[ck]; !seen {
			r.seenKeys[ck] = struct{}{}
			newKeys++
		}
	}
	if newKeys == 0 {
		return 0
	}
	return saved * newKeys / len(keys)
}
