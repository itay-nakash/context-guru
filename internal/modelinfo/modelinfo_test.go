package modelinfo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sample = `{
  "claude-sonnet-5": {"max_input_tokens": 1000000, "max_output_tokens": 128000},
  "anthropic.claude-opus-4": {"max_input_tokens": 200000},
  "gpt-4o": {"max_tokens": 128000},
  "no-window-model": {"input_cost_per_token": 0.001}
}`

// waitReady polls Window (which triggers an async background fetch and returns
// unknown until it lands) until the map is loaded, so the resolution assertions run
// against a populated cache.
func waitReady(t *testing.T, l *LiteLLM, ctx context.Context) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if _, ok := l.Window(ctx, "gpt-4o"); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("litellm map never loaded")
}

func TestLiteLLMResolvesAndNormalizes(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write([]byte(sample))
	}))
	defer srv.Close()
	l := NewLiteLLM(srv.URL, srv.Client(), time.Hour)
	ctx := context.Background()
	waitReady(t, l, ctx)

	// exact + provider-prefixed (aws/claude-sonnet-5 -> claude-sonnet-5).
	if w, ok := l.Window(ctx, "aws/claude-sonnet-5"); !ok || w != 1000000 {
		t.Fatalf("aws/claude-sonnet-5 => %d,%v want 1000000", w, ok)
	}
	// max_tokens fallback when max_input_tokens absent.
	if w, ok := l.Window(ctx, "gpt-4o"); !ok || w != 128000 {
		t.Fatalf("gpt-4o => %d,%v", w, ok)
	}
	// substring match: opus via dotted litellm key.
	if w, ok := l.Window(ctx, "claude-opus-4"); !ok || w != 200000 {
		t.Fatalf("claude-opus-4 => %d,%v", w, ok)
	}
	// unknown model => not ok (fail open).
	if _, ok := l.Window(ctx, "totally-unknown-xyz"); ok {
		t.Fatal("unknown model must return ok=false")
	}
	// cache: many lookups, one fetch.
	for i := 0; i < 5; i++ {
		l.Window(ctx, "gpt-4o")
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("expected a single cached fetch, got %d", got)
	}
}

// TestLiteLLMConcurrentSingleFlight: many concurrent cold-cache lookups must trigger
// exactly ONE upstream fetch (no thundering herd), and Window must never block on the
// (slow) upstream — it returns unknown until the background fetch lands.
func TestLiteLLMConcurrentSingleFlight(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		time.Sleep(150 * time.Millisecond) // slow upstream: a blocking impl would stall callers
		w.Write([]byte(sample))
	}))
	defer srv.Close()
	l := NewLiteLLM(srv.URL, srv.Client(), time.Hour)
	ctx := context.Background()

	// 20 concurrent cold lookups must each return PROMPTLY (non-blocking).
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			l.Window(ctx, "gpt-4o")
			if time.Since(start) > 100*time.Millisecond {
				t.Errorf("Window blocked on the upstream fetch (%v) — must be non-blocking", time.Since(start))
			}
		}()
	}
	wg.Wait()
	waitReady(t, l, ctx) // let the single background fetch complete
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("single-flight violated: expected 1 fetch, got %d", got)
	}
}

func TestChainAndStaticFallback(t *testing.T) {
	// LiteLLM pointing at a dead URL => falls through to Static.
	l := NewLiteLLM("http://127.0.0.1:0/nope", &http.Client{Timeout: 200 * time.Millisecond}, time.Hour)
	c := Chain{l, DefaultStatic()}
	if w, ok := c.Window(context.Background(), "aws/claude-sonnet-5"); !ok || w == 0 {
		t.Fatalf("chain should fall back to static: %d,%v", w, ok)
	}
}
