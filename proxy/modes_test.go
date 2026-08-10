package proxy_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rossoctl/context-guru/components"
	_ "github.com/rossoctl/context-guru/components/all"
	"github.com/rossoctl/context-guru/config"
	"github.com/rossoctl/context-guru/metrics"
	"github.com/rossoctl/context-guru/proxy"
	"github.com/rossoctl/context-guru/store"
)

// modeHandler is buildHandler plus an explicit operating mode, and it hands back the
// aggregator so a test can read the mode-partitioned rollups.
func modeHandler(t *testing.T, yaml, upstream string, mode components.Mode) (*proxy.Handler, *metrics.Aggregator) {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	agg := metrics.NewAggregator()
	pipe, err := cfg.Build(agg)
	if err != nil {
		t.Fatal(err)
	}
	h := proxy.New(pipe, store.NewMemory(store.Options{}), agg, proxy.Options{
		OpenAIUpstream: upstream, AnthropicUpstream: upstream, Mode: mode,
	})
	t.Cleanup(h.Close)
	return h, agg
}

// captureUpstream records every body the upstream receives.
func captureUpstream(t *testing.T) (*httptest.Server, func() [][]byte) {
	t.Helper()
	var mu sync.Mutex
	var got [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), got...)
	}
}

const modePipeline = "pipeline: [dedup, cacheinject]\n"

func dupBody() []byte {
	dump := strings.Repeat("a verbose repeated tool output line\n", 60)
	return openAIBody(
		map[string]any{"role": "user", "content": "do the thing"},
		map[string]any{"role": "tool", "tool_call_id": "a", "content": dump},
		map[string]any{"role": "tool", "tool_call_id": "b", "content": dump},
	)
}

func post(t *testing.T, srv *httptest.Server, body []byte) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/openai/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// awaitSnapshot polls until cond holds, so an off-path result can land.
func awaitSnapshot(t *testing.T, agg *metrics.Aggregator, cond func(metrics.Snapshot) bool) metrics.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var snap metrics.Snapshot
	for time.Now().Before(deadline) {
		snap = agg.Snapshot()
		if cond(snap) {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	return snap
}

// TestSyncIsTheDefaultAndUnchanged: an unset mode must behave exactly like the
// explicit sync mode, which is the pre-change behavior. The forwarded bodies are
// compared byte for byte — the golden test the issue asks for, expressed against the
// code's own output rather than a checked-in fixture that would drift on every
// unrelated component change.
func TestSyncIsTheDefaultAndUnchanged(t *testing.T) {
	body := dupBody()

	upA, gotA := captureUpstream(t)
	hA, _ := modeHandler(t, modePipeline, upA.URL, "") // unset
	srvA := httptest.NewServer(hA.Mux())
	defer srvA.Close()
	post(t, srvA, body)

	upB, gotB := captureUpstream(t)
	hB, _ := modeHandler(t, modePipeline, upB.URL, components.ModeSync)
	srvB := httptest.NewServer(hB.Mux())
	defer srvB.Close()
	post(t, srvB, body)

	a, b := gotA(), gotB()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one forward each, got %d and %d", len(a), len(b))
	}
	if !bytes.Equal(a[0], b[0]) {
		t.Fatalf("default mode differs from explicit sync\n default: %s\n sync:    %s", a[0], b[0])
	}
	// And sync really did compact: otherwise the comparison above is vacuous.
	if bytes.Equal(a[0], body) {
		t.Fatal("sync forwarded the original unchanged — the golden comparison proves nothing")
	}
}

// TestObserveForwardsByteIdenticalBody is the mode's core promise: the agent receives
// exactly what it sent, while the hypothetical savings are still recorded.
func TestObserveForwardsByteIdenticalBody(t *testing.T) {
	up, got := captureUpstream(t)
	h, agg := modeHandler(t, modePipeline, up.URL, components.ModeObserve)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	body := dupBody()
	post(t, srv, body)

	fwd := got()
	if len(fwd) != 1 {
		t.Fatalf("expected one forward, got %d", len(fwd))
	}
	if !bytes.Equal(fwd[0], body) {
		t.Fatalf("observe mode MODIFIED the forwarded body\n sent: %s\n fwd:  %s", body, fwd[0])
	}

	snap := awaitSnapshot(t, agg, func(s metrics.Snapshot) bool { return s.ObserveRequests > 0 })
	if snap.ObserveRequests == 0 {
		t.Fatal("observe mode recorded nothing")
	}
	if snap.PotentialSavedTokens <= 0 {
		t.Fatalf("no potential savings recorded: %+v", snap)
	}
	if snap.ActualBaselineTokens <= snap.ProjectedOptimizedTokens {
		t.Fatalf("projected usage is not below the actual baseline: %d vs %d",
			snap.ProjectedOptimizedTokens, snap.ActualBaselineTokens)
	}
	if snap.ObserveNotice == "" {
		t.Fatal("observe mode did not emit its banner")
	}
	if snap.Mode != string(components.ModeObserve) {
		t.Fatalf("mode not reported: %q", snap.Mode)
	}
}

// TestObserveMetricsCannotBeSummedIntoEnforcedTotals is the correctness requirement: a
// hypothetical must be unreachable from every enforced aggregate, or the product's
// headline savings claim is silently inflated.
func TestObserveMetricsCannotBeSummedIntoEnforcedTotals(t *testing.T) {
	up, _ := captureUpstream(t)
	h, agg := modeHandler(t, modePipeline, up.URL, components.ModeObserve)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	for i := 0; i < 3; i++ {
		post(t, srv, dupBody())
	}
	snap := awaitSnapshot(t, agg, func(s metrics.Snapshot) bool { return s.ObserveRequests > 0 })
	if snap.ObserveRequests == 0 {
		t.Fatal("nothing was observed; the test proves nothing")
	}
	if snap.Requests != 0 || snap.TokensBefore != 0 || snap.TokensAfter != 0 || snap.SavedTokens != 0 {
		t.Fatalf("observe results leaked into the enforced totals: %+v", snap)
	}
	if snap.SyncEnforced != 0 || snap.AsyncEnforced != 0 {
		t.Fatalf("observe counted as enforced: sync=%d async=%d", snap.SyncEnforced, snap.AsyncEnforced)
	}
	if len(snap.Components) != 0 {
		t.Fatalf("observe results leaked into the enforced per-component map: %v", snap.Components)
	}
	if len(snap.PotentialComponents) == 0 {
		t.Fatal("per-component hypotheticals were not recorded at all")
	}
	// The serialized payload must keep the two vocabularies disjoint.
	m := marshalMap(t, snap)
	for _, enforced := range []string{"saved_tokens", "savings_pct", "tokens_before", "tokens_after", "requests", "components"} {
		if _, ok := m[enforced]; !ok {
			t.Fatalf("%q disappeared from /stats — backward compatibility broken", enforced)
		}
	}
	for _, hypothetical := range []string{
		"potential_saved_tokens", "projected_optimized_tokens", "actual_baseline_tokens",
		"potential_components", "observe_notice", "observe_hypothetical_requests",
	} {
		if _, ok := m[hypothetical]; !ok {
			t.Fatalf("hypothetical key %q missing from the payload", hypothetical)
		}
	}
}

// TestStatsStaysBackwardCompatible: deploy/harbor/*.py parses this payload, so fields
// may be added but never renamed or removed.
func TestStatsStaysBackwardCompatible(t *testing.T) {
	up, _ := captureUpstream(t)
	h, _ := modeHandler(t, modePipeline, up.URL, components.ModeSync)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()
	post(t, srv, dupBody())

	resp, err := http.Get(srv.URL + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"requests", "tokens_before", "tokens_after", "saved_tokens", "savings_pct",
		"wasted_tokens", "bounces", "adjusted_saved", "components", "top_passthrough",
		"llm_calls", "llm_input_tokens", "llm_output_tokens",
		"cg_added_ms_avg", "upstream_ms_avg", "upstream_ms_avg_bypassed",
	} {
		if _, ok := m[k]; !ok {
			t.Fatalf("/stats lost the pre-existing field %q", k)
		}
	}
	for _, k := range []string{"mode", "sync_enforced", "async_enforced"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("/stats is missing the new field %q", k)
		}
	}
	if m["mode"] != string(components.ModeSync) {
		t.Fatalf("mode is %v, want sync", m["mode"])
	}
	if m["sync_enforced"].(float64) < 1 {
		t.Fatalf("sync request not counted as enforced: %v", m["sync_enforced"])
	}
}

// TestAsyncForwardsAndDefersWork: the request goes out, is counted under its own
// mode, and the queue tuple is exposed whole.
func TestAsyncForwardsAndDefersWork(t *testing.T) {
	up, got := captureUpstream(t)
	h, agg := modeHandler(t, modePipeline, up.URL, components.ModeAsync)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	post(t, srv, dupBody())
	if fwd := got(); len(fwd) != 1 {
		t.Fatalf("expected one forward, got %d", len(fwd))
	}
	snap := agg.Snapshot()
	if snap.AsyncEnforced != 1 {
		t.Fatalf("async request not counted under its own mode: %+v", snap)
	}
	if snap.SyncEnforced != 0 {
		t.Fatal("async request counted as sync")
	}
	if snap.ObserveRequests != 0 || snap.PotentialSavedTokens != 0 {
		t.Fatal("async results leaked into the hypothetical namespace")
	}
	q, ok := marshalMap(t, snap)["async_queue"]
	if !ok {
		t.Fatal("async_queue absent from /stats in async mode")
	}
	var tuple map[string]int64
	if err := json.Unmarshal(q, &tuple); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"queued", "pending", "processed", "dropped", "errors", "stale_discarded"} {
		if _, ok := tuple[k]; !ok {
			t.Fatalf("async_queue is missing %q — the whole tuple must be exposed", k)
		}
	}
}

// TestConcurrentTurnsOneSession pushes many simultaneous turns of ONE session through
// the real handler in async mode: no race (run under -race), no corruption, and every
// request still answered.
func TestConcurrentTurnsOneSession(t *testing.T) {
	up, got := captureUpstream(t)
	h, _ := modeHandler(t, modePipeline, up.URL, components.ModeAsync)
	srv := httptest.NewServer(h.Mux())
	defer srv.Close()

	body := dupBody()
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/openai/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-context-guru-session", "shared")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()
	if n := len(got()); n != 24 {
		t.Fatalf("forwarded %d of 24 requests", n)
	}
}

// TestCloseLeavesNoGoroutines: the pool the handler owns must be reclaimed.
func TestCloseLeavesNoGoroutines(t *testing.T) {
	// Everything unrelated (the mock upstream's own goroutines) is created BEFORE the
	// baseline, so the only difference this measures is the pool's.
	up, _ := captureUpstream(t)
	settleGoroutines()
	before := runtime.NumGoroutine()

	cfg, err := config.LoadBytes([]byte(modePipeline))
	if err != nil {
		t.Fatal(err)
	}
	agg := metrics.NewAggregator()
	pipe, err := cfg.Build(agg)
	if err != nil {
		t.Fatal(err)
	}
	h := proxy.New(pipe, store.NewMemory(store.Options{}), agg, proxy.Options{
		OpenAIUpstream: up.URL, Mode: components.ModeAsync,
	})
	h.Close()
	h.Close() // idempotent

	settleGoroutines()
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutine leak after Close: %d before, %d after", before, after)
	}
}

// TestSyncModeStartsNoPool: sync adds no machinery — no pool, no async_queue.
func TestSyncModeStartsNoPool(t *testing.T) {
	up, _ := captureUpstream(t)
	_, agg := modeHandler(t, modePipeline, up.URL, components.ModeSync)
	raw, err := json.Marshal(agg.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("async_queue")) {
		t.Fatalf("sync mode advertised an async queue: %s", raw)
	}
}

func TestUnknownModeIsRejected(t *testing.T) {
	if _, err := config.LoadBytes([]byte("pipeline: [dedup]\nmode: turbo\n")); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	for _, ok := range []string{"", "sync", "async", "observe"} {
		if _, err := config.LoadBytes([]byte("pipeline: [dedup]\nmode: " + ok + "\n")); err != nil {
			t.Fatalf("mode %q rejected: %v", ok, err)
		}
	}
}

func marshalMap(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func settleGoroutines() {
	for i := 0; i < 20; i++ {
		runtime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
}
