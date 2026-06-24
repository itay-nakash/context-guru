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

// maxLatencySamples bounds the retained latency samples so memory stays flat over a
// long-running process. The most recent maxLatencySamples are kept (ring buffer).
const maxLatencySamples = 1024

// Aggregator is an Emitter that accumulates Events into process-wide reduction stats,
// safe for concurrent use. A host installs it as the proxy Emitter and serves its
// Snapshot/WriteJSON on a /stats endpoint. It does not replace a streaming Emitter —
// a host can fan out to both.
type Aggregator struct {
	rates map[string]CostRate

	mu            sync.Mutex
	requests      int64
	tokensBefore  int64
	tokensAfter   int64
	tokensSaved   int64
	cacheInjected int64
	extracted     int64
	stageErrors   int64
	costSavedUSD  float64

	// latency holds a bounded ring of recent added-latency samples (ms). next is the
	// next write index; filled tracks how many slots are valid.
	latency [maxLatencySamples]int
	next    int
	filled  int
}

// NewAggregator returns an Aggregator pricing savings with rates (keyed by model, plus
// an optional DefaultCostKey fallback). A nil rates map disables cost estimation.
func NewAggregator(rates map[string]CostRate) *Aggregator {
	return &Aggregator{rates: rates}
}

// Emit folds one Event into the running totals.
func (a *Aggregator) Emit(_ context.Context, e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()

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
	a.costSavedUSD += float64(e.TokensSaved) / 1e6 * a.inputRate(e.RequestModel)

	a.latency[a.next] = e.LatencyMillis
	a.next = (a.next + 1) % maxLatencySamples
	if a.filled < maxLatencySamples {
		a.filled++
	}
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

// Snapshot is a point-in-time, JSON-serializable view of the aggregated stats.
type Snapshot struct {
	Requests              int64   `json:"requests"`
	TokensBefore          int64   `json:"tokens_before"`
	TokensAfter           int64   `json:"tokens_after"`
	TokensSaved           int64   `json:"tokens_saved"`
	Ratio                 float64 `json:"reduction_ratio"` // tokens_saved / tokens_before (higher is more reduction; 0 = no savings)
	CacheInjected         int64   `json:"cache_injected"`
	Extracted             int64   `json:"extracted"`
	StageErrors           int64   `json:"stage_errors"`
	CostSavedUSD          float64 `json:"cost_saved_usd"`
	AddedLatencyP50Millis int     `json:"added_latency_p50_ms"`
	AddedLatencyP95Millis int     `json:"added_latency_p95_ms"`
}

// Snapshot returns the current totals.
func (a *Aggregator) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	s := Snapshot{
		Requests:      a.requests,
		TokensBefore:  a.tokensBefore,
		TokensAfter:   a.tokensAfter,
		TokensSaved:   a.tokensSaved,
		CacheInjected: a.cacheInjected,
		Extracted:     a.extracted,
		StageErrors:   a.stageErrors,
		CostSavedUSD:  a.costSavedUSD,
	}
	if a.tokensBefore > 0 {
		// Fraction of input tokens removed: 0 = no savings, 0.9 = 90% reduced.
		s.Ratio = float64(a.tokensSaved) / float64(a.tokensBefore)
	}
	s.AddedLatencyP50Millis = a.percentileLocked(0.50)
	s.AddedLatencyP95Millis = a.percentileLocked(0.95)
	return s
}

// percentileLocked computes the p-quantile of the retained latency samples. Caller
// holds a.mu. Returns 0 when no samples have been recorded.
func (a *Aggregator) percentileLocked(p float64) int {
	if a.filled == 0 {
		return 0
	}
	xs := make([]int, a.filled)
	copy(xs, a.latency[:a.filled])
	sort.Ints(xs)
	// Nearest-rank: index = ceil(p*N)-1, clamped.
	idx := int(p*float64(a.filled)+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= a.filled {
		idx = a.filled - 1
	}
	return xs[idx]
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
		"requests=%d tokens %d→%d saved=%d (reduction=%.1f%%) cost_saved=$%.4f cache=%d extract=%d errors=%d latency p50=%dms p95=%dms",
		s.Requests, s.TokensBefore, s.TokensAfter, s.TokensSaved, s.Ratio*100,
		s.CostSavedUSD, s.CacheInjected, s.Extracted, s.StageErrors,
		s.AddedLatencyP50Millis, s.AddedLatencyP95Millis,
	)
}
