// Package modelinfo resolves a model's context window (max input tokens)
// DYNAMICALLY, so context-guru's triggers can scale with the model rather than
// hard-coding thresholds. The window is obtained without maintaining our own model
// list: the primary source is LiteLLM's community-maintained
// model_prices_and_context_window.json (fetched once and cached); an optional
// gateway /model/info probe is tried first for deployments that expose it; a tiny
// embedded table is the last-resort fallback.
//
// Every lookup fails OPEN: an unknown model returns ok=false and callers fall back
// to absolute thresholds. Nothing here ever blocks a request — fetches are cached,
// single-flighted, and time-bounded, and any error just leaves the window unknown.
package modelinfo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Resolver returns a model's context window in tokens. ok=false means "unknown".
type Resolver interface {
	Window(ctx context.Context, model string) (tokens int, ok bool)
}

// LiteLLMPricesURL is the community-maintained map of model -> {max_input_tokens,…}.
// Overridable (air-gapped mirrors) via NewLiteLLM.
const LiteLLMPricesURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// normalize lowercases a model id and strips a provider prefix so gateway route
// names (aws/claude-sonnet-5, anthropic/…, bedrock/…, us.anthropic.…) match the
// keys LiteLLM uses. Returns the full normalized id and its last path segment.
func normalize(model string) (full, tail string) {
	full = strings.ToLower(strings.TrimSpace(model))
	tail = full
	if i := strings.LastIndexAny(tail, "/"); i >= 0 {
		tail = tail[i+1:]
	}
	return full, tail
}

// LiteLLM fetches and caches the LiteLLM prices map, serving per-model windows.
type LiteLLM struct {
	URL    string
	Client *http.Client
	TTL    time.Duration

	mu       sync.Mutex
	byKey    map[string]int // normalized key -> max_input_tokens
	fetched  time.Time      // last fetch ATTEMPT (success or failure)
	fetching bool           // a background fetch is in flight (single-flight guard)
}

// negTTL is how long to wait before retrying after a failed/empty fetch when no map
// has ever been loaded — short enough to recover quickly, long enough not to hammer an
// unreachable source on every request.
const negTTL = 30 * time.Second

// NewLiteLLM builds a resolver. url="" uses LiteLLMPricesURL; ttl<=0 uses 6h.
func NewLiteLLM(url string, client *http.Client, ttl time.Duration) *LiteLLM {
	if url == "" {
		url = LiteLLMPricesURL
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	return &LiteLLM{URL: url, Client: client, TTL: ttl}
}

// refreshIfStale kicks off a BACKGROUND fetch when the cache is stale — it never blocks
// the caller. Window() is on the request hot path, so blocking on a (up-to-10s) GitHub
// GET would inflate every request's latency; instead the first calls return "unknown"
// (window 0 ⇒ fraction triggers ignored, absolutes apply — the safe default) until the
// fetch lands. Single-flighted via `fetching`; on failure the attempt time is recorded
// so we don't refetch until negTTL passes (negative caching, no thundering herd).
func (l *LiteLLM) refreshIfStale(context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	haveData := l.byKey != nil
	fresh := haveData && time.Since(l.fetched) < l.TTL
	retryGap := l.TTL
	if !haveData {
		retryGap = negTTL
	}
	if fresh || l.fetching || (!l.fetched.IsZero() && time.Since(l.fetched) < retryGap) {
		return
	}
	l.fetching = true
	go func() {
		// Detached context: the fetch outlives the triggering request; its own Client
		// timeout bounds it. Record the attempt time regardless of outcome (negative cache).
		m, err := l.fetch(context.Background())
		l.mu.Lock()
		l.fetching = false
		l.fetched = time.Now()
		if err == nil && len(m) > 0 {
			l.byKey = m // on failure keep any prior map (fail open)
		}
		l.mu.Unlock()
	}()
}

func (l *LiteLLM) fetch(ctx context.Context) (map[string]int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var raw map[string]struct {
		MaxInputTokens int `json:"max_input_tokens"`
		MaxTokens      int `json:"max_tokens"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	m := make(map[string]int, len(raw)*2)
	for k, v := range raw {
		w := v.MaxInputTokens
		if w == 0 {
			w = v.MaxTokens
		}
		if w == 0 {
			continue
		}
		full, tail := normalize(k)
		m[full] = w
		if _, ok := m[tail]; !ok { // don't clobber a more-specific full key
			m[tail] = w
		}
	}
	return m, nil
}

// Window returns the model's context window from the cached LiteLLM map.
func (l *LiteLLM) Window(ctx context.Context, model string) (int, bool) {
	l.refreshIfStale(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byKey == nil {
		return 0, false
	}
	full, tail := normalize(model)
	if w, ok := l.byKey[full]; ok {
		return w, true
	}
	if w, ok := l.byKey[tail]; ok {
		return w, true
	}
	// last resort: any key that contains the tail (e.g. claude-sonnet-5 vs
	// anthropic.claude-sonnet-5) — pick the max window among matches.
	best, found := 0, false
	for k, w := range l.byKey {
		if strings.Contains(k, tail) && w > best {
			best, found = w, true
		}
	}
	return best, found
}

// Static is a tiny last-resort table keyed by substring. Used only when a dynamic
// source is unavailable. Deliberately small — it is a floor, not a registry.
type Static struct{ table []staticEntry }

type staticEntry struct {
	substr string
	window int
}

// DefaultStatic covers the common families with conservative windows.
func DefaultStatic() Static {
	return Static{table: []staticEntry{
		{"claude-sonnet-5", 1000000}, {"claude-opus-4", 200000}, {"claude", 200000},
		{"gpt-5", 400000}, {"gpt-4o", 128000}, {"gpt-4", 128000}, {"o1", 200000}, {"o3", 200000},
		{"gemini-2", 1000000}, {"gemini", 1000000}, {"llama", 128000}, {"mistral", 32000},
	}}
}

func (s Static) Window(_ context.Context, model string) (int, bool) {
	m := strings.ToLower(model)
	for _, e := range s.table { // first match wins; order most-specific first
		if strings.Contains(m, e.substr) {
			return e.window, true
		}
	}
	return 0, false
}

// Chain tries each resolver in order; the first ok wins.
type Chain []Resolver

func (c Chain) Window(ctx context.Context, model string) (int, bool) {
	for _, r := range c {
		if w, ok := r.Window(ctx, model); ok {
			return w, true
		}
	}
	return 0, false
}
