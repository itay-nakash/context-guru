package dash

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// API serves the dashboard: the JSON endpoints, the SSE stream, and the embedded
// UI. It holds only read access to the store plus the recorder's counters — it
// never writes a request row.
type API struct {
	rec   *Recorder
	trust []*net.IPNet
}

// NewAPI builds the HTTP surface for a recorder. Malformed CIDRs are skipped with
// no error: a typo in a trust list must not stop the proxy, and the failure mode
// (loopback-only) is the safe one.
func NewAPI(rec *Recorder) *API {
	a := &API{rec: rec}
	for _, c := range rec.Opts().TrustedCIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			a.trust = append(a.trust, n)
		}
	}
	return a
}

// Mount registers every dashboard route on a mux under the given prefix
// (typically "/dashboard" for the UI and "/api" for the data).
func (a *API) Mount(m *http.ServeMux) {
	m.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		// One canonical URL: /dashboard and /dashboard/ must not be two pages.
		http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
	})
	m.Handle("GET /dashboard/", http.StripPrefix("/dashboard/", uiHandler()))
	m.HandleFunc("GET /api/stats", a.stats)
	m.HandleFunc("GET /api/series", a.series)
	m.HandleFunc("GET /api/requests", a.requests)
	m.HandleFunc("GET /api/requests/{id}", a.request)
	m.HandleFunc("GET /api/sessions", a.sessions)
	m.HandleFunc("GET /api/components", a.components)
	m.HandleFunc("GET /api/facets", a.facets)
	m.HandleFunc("GET /api/config", a.config)
	m.HandleFunc("GET /api/benchmarks", a.benchmarks)
	m.HandleFunc("GET /api/benchmarks/{id}/tasks", a.benchmarkTasks)
	m.HandleFunc("GET /api/capture", a.capture)
	m.HandleFunc("GET /api/events", a.rec.Hub().ServeHTTP)
}

// trusted reports whether a request may see per-request CONTENT and the effective
// configuration. Loopback always may; otherwise the peer must be in a configured
// trusted CIDR. Aggregates are deliberately NOT gated — a proxy bound to 0.0.0.0
// should still show its own numbers, and the point of this tool is observability.
//
// This is the one place headroom's gate is worth copying, and the one place it is
// not: we gate CONTENT (which can carry a user's source code), never metrics.
func (a *API) trusted(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range a.trust {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	// The dashboard is same-origin only; no CORS header, so a random page cannot
	// read a developer's transcripts out of a locally-bound proxy.
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// filterFrom parses the shared filter query parameters. Unknown values are simply
// not matched — a filter is a view, so a bad value shows an empty list rather than
// a 400 the UI has to special-case.
func filterFrom(r *http.Request) Filter {
	q := r.URL.Query()
	f := Filter{
		Session:    q.Get("session"),
		Model:      q.Get("model"),
		Provider:   q.Get("provider"),
		Agent:      q.Get("agent"),
		Preset:     q.Get("preset"),
		Mode:       q.Get("mode"),
		Component:  q.Get("component"),
		Reason:     q.Get("reason"),
		Accounting: q.Get("accounting"),
		Q:          q.Get("q"),
	}
	f.Since = atoi64(q.Get("since"))
	f.Until = atoi64(q.Get("until"))
	return f
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	o, err := a.rec.DB().Overview(filterFrom(r))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, o)
}

func (a *API) series(w http.ResponseWriter, r *http.Request) {
	bucket := atoi64(r.URL.Query().Get("bucket"))
	b, err := a.rec.DB().Series(filterFrom(r), bucket)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"bucket_ms": bucket, "buckets": b})
}

func (a *API) requests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := a.rec.DB().Requests(filterFrom(r), atoi64(q.Get("before")), atoiDefault(q.Get("limit"), 50))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, p)
}

func (a *API) request(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if id <= 0 {
		httpErr(w, http.StatusBadRequest, "bad id")
		return
	}
	trusted := a.trusted(r)
	e, err := a.rec.DB().Request(id, trusted)
	if err != nil {
		httpErr(w, http.StatusNotFound, "no such request")
		return
	}
	writeJSON(w, map[string]any{
		"request": e,
		// Tell the UI WHY content is missing, so "no content" and "not allowed to
		// see content" are never the same empty panel.
		"content_visible":   trusted,
		"content_captured":  a.rec.Opts().CaptureContent,
		"content_cap_bytes": a.rec.Opts().ContentCap,
	})
}

func (a *API) sessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, total, err := a.rec.DB().Sessions(filterFrom(r),
		atoiDefault(q.Get("limit"), 50), atoiDefault(q.Get("offset"), 0))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"sessions": rows, "total": total})
}

func (a *API) components(w http.ResponseWriter, r *http.Request) {
	rows, err := a.rec.DB().Components(filterFrom(r))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"components": rows})
}

func (a *API) facets(w http.ResponseWriter, r *http.Request) {
	f, err := a.rec.DB().Facets(filterFrom(r))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, f)
}

func (a *API) config(w http.ResponseWriter, r *http.Request) {
	if !a.trusted(r) {
		httpErr(w, http.StatusForbidden,
			"effective configuration is visible from loopback or a trusted CIDR only")
		return
	}
	// Redact even for a trusted caller: nothing sensitive should be in here at all,
	// and a defence that only applies to untrusted callers is one misconfiguration
	// away from being no defence.
	writeJSON(w, RedactConfig(a.rec.Opts().Effective))
}

func (a *API) benchmarks(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		runs, tasks := a.rec.DB().IngestBenchRoots(a.rec.Opts().BenchDirs)
		writeJSON(w, map[string]any{"ingested_runs": runs, "ingested_tasks": tasks})
		return
	}
	runs, err := a.rec.DB().BenchRuns()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"runs": runs})
}

func (a *API) benchmarkTasks(w http.ResponseWriter, r *http.Request) {
	rows, err := a.rec.DB().BenchTasks(atoi64(r.PathValue("id")), r.URL.Query().Get("arm"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"tasks": rows})
}

// capture reports the capture pipeline's own health, drops included. Exposed as a
// first-class endpoint (and rendered in the UI) because a dashboard that hides its
// own coverage gaps cannot be trusted about anything else.
func (a *API) capture(w http.ResponseWriter, r *http.Request) {
	s := a.rec.Stats()
	writeJSON(w, map[string]any{
		"capture": s,
		"description": "Captured is what the proxy handed to the capture channel; written is what " +
			"reached the database; dropped is what a full channel discarded rather than " +
			"delay a request. A non-zero drop count means the numbers above under-report — " +
			"raise the queue size or lower the traffic before drawing conclusions.",
	})
}
