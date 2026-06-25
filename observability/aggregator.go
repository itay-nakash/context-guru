package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
)

// DefaultCostKey is the CostRates key used to price a model whose own rate is not
// listed. Lets a host configure one generic fallback (e.g. an average input rate)
// while still naming specific models.
const DefaultCostKey = "default"

// CostRate is the per-million-token price for a model, split by direction. Only the
// input rate is used to estimate savings, since reduction removes input tokens.
type CostRate struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

const (
	// maxLatencySamples bounds retained latency samples (per scope) so memory stays
	// flat over a long-running process — most recent samples kept (ring buffer).
	maxLatencySamples = 1024
	// maxRecentCalls bounds the retained per-call records served at /stats.
	maxRecentCalls = 256
	// maxSessions bounds the per-session breakdown; the least-recently-seen session is
	// evicted past this.
	maxSessions = 512
)

// LatencyStats summarizes a set of added-latency samples (milliseconds).
type LatencyStats struct {
	P50Millis  int `json:"p50_ms"`
	P95Millis  int `json:"p95_ms"`
	P99Millis  int `json:"p99_ms"`
	MaxMillis  int `json:"max_ms"`
	MeanMillis int `json:"mean_ms"`
}

// latRing is a bounded ring of latency samples with quantile/aggregate helpers.
type latRing struct {
	buf    []int
	next   int
	filled int
}

func newLatRing(n int) latRing { return latRing{buf: make([]int, n)} }

func (r *latRing) add(v int) {
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.filled < len(r.buf) {
		r.filled++
	}
}

func (r *latRing) stats() LatencyStats {
	if r.filled == 0 {
		return LatencyStats{}
	}
	xs := make([]int, r.filled)
	copy(xs, r.buf[:r.filled])
	sort.Ints(xs)
	sum := 0
	for _, x := range xs {
		sum += x
	}
	q := func(p float64) int {
		idx := int(p*float64(r.filled)+0.999999) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= r.filled {
			idx = r.filled - 1
		}
		return xs[idx]
	}
	return LatencyStats{
		P50Millis: q(0.50), P95Millis: q(0.95), P99Millis: q(0.99),
		MaxMillis: xs[r.filled-1], MeanMillis: sum / r.filled,
	}
}

// sessionAgg accumulates one conversation's metrics.
type sessionAgg struct {
	requests      int64
	tokensBefore  int64
	tokensAfter   int64
	tokensSaved   int64
	cacheInjected int64
	extracted     int64
	stageErrors   int64
	reducedTotal  int64
	candidates    int64
	costSavedUSD  float64
	toolsTotal    int
	toolDefTokens int
	lastModel     string
	lastSurface   string
	lastSeq       int64
	lat           latRing
}

// Aggregator is an Emitter that accumulates Events into process-wide reduction stats, a
// per-session breakdown, and a recent-per-call ring — safe for concurrent use. A host
// installs it as the proxy Emitter and serves its Snapshot/WriteJSON on /stats.
type Aggregator struct {
	rates map[string]CostRate

	mu            sync.Mutex
	seq           int64
	requests      int64
	tokensBefore  int64
	tokensAfter   int64
	tokensSaved   int64
	cacheInjected int64
	extracted     int64
	stageErrors   int64
	reducedTotal  int64
	candidates    int64
	costSavedUSD  float64
	lat           latRing

	sessions map[string]*sessionAgg
	recent   []CallRecord // ring of recent per-call records
	rNext    int
	rFilled  int
}

// NewAggregator returns an Aggregator pricing savings with rates (keyed by model, plus
// an optional DefaultCostKey fallback). A nil rates map disables cost estimation.
func NewAggregator(rates map[string]CostRate) *Aggregator {
	return &Aggregator{
		rates:    rates,
		lat:      newLatRing(maxLatencySamples),
		sessions: make(map[string]*sessionAgg),
		recent:   make([]CallRecord, maxRecentCalls),
	}
}

// CallRecord is the full per-call metric set retained for /stats (recent_calls).
type CallRecord struct {
	Seq             int64   `json:"seq"`
	System          string  `json:"system"`
	RequestModel    string  `json:"request_model"`
	Surface         string  `json:"surface"`
	SessionID       string  `json:"session_id"`
	TokensBefore    int     `json:"tokens_before"`
	TokensAfter     int     `json:"tokens_after"`
	TokensSaved     int     `json:"tokens_saved"`
	Ratio           float64 `json:"reduction_ratio"`
	CacheInjected   bool    `json:"cache_injected"`
	Extracted       bool    `json:"extracted"`
	StageErrors     int     `json:"stage_errors"`
	ToolsTotal      int     `json:"tools_total"`
	ToolDefTokens   int     `json:"tool_def_tokens"`
	ReducedCount    int     `json:"reduced_blocks"`
	CandidatesCount int     `json:"extract_candidates"`
	FrozenCount     int     `json:"frozen_messages"`
	Rehydrated      int     `json:"rehydrated"`
	AtCompaction    bool    `json:"at_compaction"`
	AddedLatencyMs  int     `json:"added_latency_ms"`
}

// Emit folds one Event into the global totals, its session, and the recent-calls ring.
func (a *Aggregator) Emit(_ context.Context, e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.seq++
	cost := float64(e.TokensSaved) / 1e6 * a.inputRate(e.RequestModel)

	a.requests++
	a.tokensBefore += int64(e.TokensBefore)
	a.tokensAfter += int64(e.TokensAfter)
	a.tokensSaved += int64(e.TokensSaved)
	if e.CacheInject {
		a.cacheInjected++
	}
	if e.Extracted {
		a.extracted++
	}
	a.stageErrors += int64(e.StageErrors)
	a.reducedTotal += int64(e.ReducedCount)
	a.candidates += int64(e.CandidatesCount)
	a.costSavedUSD += cost
	a.lat.add(e.LatencyMillis)

	// Per-session.
	if e.SessionID != "" {
		s := a.sessions[e.SessionID]
		if s == nil {
			a.evictSessionsLocked()
			s = &sessionAgg{lat: newLatRing(maxLatencySamples)}
			a.sessions[e.SessionID] = s
		}
		s.requests++
		s.tokensBefore += int64(e.TokensBefore)
		s.tokensAfter += int64(e.TokensAfter)
		s.tokensSaved += int64(e.TokensSaved)
		if e.CacheInject {
			s.cacheInjected++
		}
		if e.Extracted {
			s.extracted++
		}
		s.stageErrors += int64(e.StageErrors)
		s.reducedTotal += int64(e.ReducedCount)
		s.candidates += int64(e.CandidatesCount)
		s.costSavedUSD += cost
		s.toolsTotal = e.ToolsTotal
		s.toolDefTokens = e.ToolDefTokens
		s.lastModel = e.RequestModel
		s.lastSurface = e.Surface
		s.lastSeq = a.seq
		s.lat.add(e.LatencyMillis)
	}

	// Recent per-call ring.
	a.recent[a.rNext] = CallRecord{
		Seq: a.seq, System: e.System, RequestModel: e.RequestModel, Surface: e.Surface,
		SessionID: e.SessionID, TokensBefore: e.TokensBefore, TokensAfter: e.TokensAfter,
		TokensSaved: e.TokensSaved, Ratio: e.Ratio, CacheInjected: e.CacheInject,
		Extracted: e.Extracted, StageErrors: e.StageErrors, ToolsTotal: e.ToolsTotal,
		ToolDefTokens: e.ToolDefTokens, ReducedCount: e.ReducedCount,
		CandidatesCount: e.CandidatesCount, FrozenCount: e.FrozenCount,
		Rehydrated: e.Rehydrated, AtCompaction: e.AtCompaction, AddedLatencyMs: e.LatencyMillis,
	}
	a.rNext = (a.rNext + 1) % maxRecentCalls
	if a.rFilled < maxRecentCalls {
		a.rFilled++
	}
}

// evictSessionsLocked drops the least-recently-seen session when at capacity.
func (a *Aggregator) evictSessionsLocked() {
	if len(a.sessions) < maxSessions {
		return
	}
	var oldestID string
	var oldestSeq int64 = 1<<63 - 1
	for id, s := range a.sessions {
		if s.lastSeq < oldestSeq {
			oldestSeq, oldestID = s.lastSeq, id
		}
	}
	delete(a.sessions, oldestID)
}

// inputRate resolves the input price for a model, falling back to DefaultCostKey, then 0.
func (a *Aggregator) inputRate(model string) float64 {
	if a.rates == nil {
		return 0
	}
	if r, ok := a.rates[model]; ok {
		return r.InputPerMTok
	}
	if r, ok := a.rates[DefaultCostKey]; ok {
		return r.InputPerMTok
	}
	return 0
}

// SessionSnapshot is one conversation's aggregated metrics.
type SessionSnapshot struct {
	SessionID     string       `json:"session_id"`
	Requests      int64        `json:"requests"`
	TokensBefore  int64        `json:"tokens_before"`
	TokensAfter   int64        `json:"tokens_after"`
	TokensSaved   int64        `json:"tokens_saved"`
	Ratio         float64      `json:"reduction_ratio"`
	CacheInjected int64        `json:"cache_injected"`
	Extracted     int64        `json:"extracted"`
	StageErrors   int64        `json:"stage_errors"`
	ReducedBlocks int64        `json:"reduced_blocks"`
	Candidates    int64        `json:"extract_candidates"`
	CostSavedUSD  float64      `json:"cost_saved_usd"`
	ToolsTotal    int          `json:"tools_total"`
	ToolDefTokens int          `json:"tool_def_tokens"`
	Model         string       `json:"model"`
	Surface       string       `json:"surface"`
	Latency       LatencyStats `json:"latency"`
}

// Snapshot is a point-in-time, JSON-serializable view of the aggregated stats: global
// totals, a per-session breakdown, and the most recent per-call records.
type Snapshot struct {
	Requests      int64   `json:"requests"`
	TokensBefore  int64   `json:"tokens_before"`
	TokensAfter   int64   `json:"tokens_after"`
	TokensSaved   int64   `json:"tokens_saved"`
	Ratio         float64 `json:"reduction_ratio"` // tokens_saved / tokens_before (0 = no savings)
	CacheInjected int64   `json:"cache_injected"`
	Extracted     int64   `json:"extracted"`
	StageErrors   int64   `json:"stage_errors"`
	ReducedBlocks int64   `json:"reduced_blocks"`
	Candidates    int64   `json:"extract_candidates"`
	CostSavedUSD  float64 `json:"cost_saved_usd"`
	Sessions      int     `json:"sessions"`

	// Flat latency fields (kept for compatibility) + full distribution.
	AddedLatencyP50Millis int          `json:"added_latency_p50_ms"`
	AddedLatencyP95Millis int          `json:"added_latency_p95_ms"`
	Latency               LatencyStats `json:"latency"`

	SessionStats []SessionSnapshot `json:"session_stats,omitempty"`
	RecentCalls  []CallRecord      `json:"recent_calls,omitempty"`
}

// Snapshot returns the current totals plus per-session and recent-call detail.
func (a *Aggregator) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	lat := a.lat.stats()
	s := Snapshot{
		Requests: a.requests, TokensBefore: a.tokensBefore, TokensAfter: a.tokensAfter,
		TokensSaved: a.tokensSaved, CacheInjected: a.cacheInjected, Extracted: a.extracted,
		StageErrors: a.stageErrors, ReducedBlocks: a.reducedTotal, Candidates: a.candidates,
		CostSavedUSD: a.costSavedUSD, Sessions: len(a.sessions),
		AddedLatencyP50Millis: lat.P50Millis, AddedLatencyP95Millis: lat.P95Millis,
		Latency: lat,
	}
	if a.tokensBefore > 0 {
		s.Ratio = float64(a.tokensSaved) / float64(a.tokensBefore)
	}

	for id, sa := range a.sessions {
		ss := SessionSnapshot{
			SessionID: id, Requests: sa.requests, TokensBefore: sa.tokensBefore,
			TokensAfter: sa.tokensAfter, TokensSaved: sa.tokensSaved,
			CacheInjected: sa.cacheInjected, Extracted: sa.extracted, StageErrors: sa.stageErrors,
			ReducedBlocks: sa.reducedTotal, Candidates: sa.candidates, CostSavedUSD: sa.costSavedUSD,
			ToolsTotal: sa.toolsTotal, ToolDefTokens: sa.toolDefTokens,
			Model: sa.lastModel, Surface: sa.lastSurface, Latency: sa.lat.stats(),
		}
		if sa.tokensBefore > 0 {
			ss.Ratio = float64(sa.tokensSaved) / float64(sa.tokensBefore)
		}
		s.SessionStats = append(s.SessionStats, ss)
	}
	// Stable order: most recently active session first.
	sort.Slice(s.SessionStats, func(i, j int) bool {
		return a.sessions[s.SessionStats[i].SessionID].lastSeq > a.sessions[s.SessionStats[j].SessionID].lastSeq
	})

	// Recent calls in chronological order (oldest retained → newest).
	for i := 0; i < a.rFilled; i++ {
		idx := (a.rNext - a.rFilled + i + maxRecentCalls*2) % maxRecentCalls
		s.RecentCalls = append(s.RecentCalls, a.recent[idx])
	}
	return s
}

// WriteJSON writes the current Snapshot as indented JSON.
func (a *Aggregator) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(a.Snapshot())
}

// Summary returns a one-line human-readable digest of the current stats.
func (a *Aggregator) Summary() string {
	return SummaryOf(a.Snapshot())
}

// SummaryOf renders a Snapshot as a one-line human-readable digest. Useful for a
// client (e.g. `lab-cx stats`) that holds only a decoded Snapshot.
func SummaryOf(s Snapshot) string {
	return fmt.Sprintf(
		"requests=%d sessions=%d tokens %d→%d saved=%d (reduction=%.1f%%) cost_saved=$%.4f cache=%d extract=%d reduced=%d errors=%d latency p50=%dms p95=%dms p99=%dms max=%dms",
		s.Requests, s.Sessions, s.TokensBefore, s.TokensAfter, s.TokensSaved, s.Ratio*100,
		s.CostSavedUSD, s.CacheInjected, s.Extracted, s.ReducedBlocks, s.StageErrors,
		s.AddedLatencyP50Millis, s.AddedLatencyP95Millis, s.Latency.P99Millis, s.Latency.MaxMillis,
	)
}
