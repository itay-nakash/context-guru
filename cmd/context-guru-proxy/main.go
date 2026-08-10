// Command context-guru-proxy is the LLM proxy integration and the
// eval-containers gateway. It runs the context-guru component pipeline on
// inbound chat requests, then forwards them to the configured upstream
// provider, exposing /openai + /anthropic on one port plus /healthz, /stats,
// and /expand.
//
// Config is loaded from --config (YAML); upstreams and listen address from
// flags/env. Fail open: on any pipeline trouble the original request is
// forwarded untouched.
package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rossoctl/context-guru/components"
	_ "github.com/rossoctl/context-guru/components/all"
	"github.com/rossoctl/context-guru/config"
	"github.com/rossoctl/context-guru/dash"
	"github.com/rossoctl/context-guru/internal/buildinfo"
	"github.com/rossoctl/context-guru/internal/cheapmodel"
	"github.com/rossoctl/context-guru/internal/modelinfo"
	"github.com/rossoctl/context-guru/metrics"
	"github.com/rossoctl/context-guru/proxy"
)

func main() {
	var (
		addr      = envOr("LISTEN_ADDR", ":4000")
		cfgPath   = flag.String("config", envOr("CONFIG", ""), "path to context-guru YAML config")
		preset    = flag.String("preset", envOr("PRESET", "codesmart"), "preset to use when --config is absent (codesmart = the SWE-bench-winning cache-aware config; codesafe = deterministic-only)")
		openai    = flag.String("openai-upstream", envOr("OPENAI_UPSTREAM", "https://api.openai.com"), "OpenAI upstream base URL")
		anthropic = flag.String("anthropic-upstream", envOr("ANTHROPIC_UPSTREAM", "https://api.anthropic.com"), "Anthropic upstream base URL")
		bob       = flag.String("bob-upstream", envOr("BOB_UPSTREAM", ""), "Bob (BobShell) backend base URL; enables the Bob gateway routes when set (e.g. https://api.us-east.bob.ibm.com)")
		storeFlag = flag.String("store", envOr("STORE", ""), "override state store: true|false (default: config store.enabled, else on)")
		modeFlag  = flag.String("mode", envOr("MODE", ""), "operating mode: sync (default) | observe (overrides the config's mode:)")

		// Dashboard. Off by default so an existing deployment's behavior and route
		// table are unchanged until asked for; on, it adds /dashboard/ + /api/*.
		// NOTE: deliberately NO "disable observability in production" gate — for a
		// tool whose value IS observability, that would be backwards.
		dashOn = flag.Bool("dashboard", envBool("DASHBOARD", false),
			"enable the persistent dashboard (embedded UI at /dashboard/, JSON+SSE at /api/*)")
		dashDB = flag.String("dashboard-db", envOr("DASHBOARD_DB", "./context-guru-dashboard.db"),
			"dashboard SQLite path; ':memory:' keeps history in RAM only (lost on restart)")
		dashRetain = flag.Duration("dashboard-retention", envDuration("DASHBOARD_RETENTION", 7*24*time.Hour),
			"drop dashboard rows older than this (0 = no age limit)")
		dashMaxBytes = flag.Int64("dashboard-max-bytes", int64(envInt("DASHBOARD_MAX_BYTES", 512<<20)),
			"cap the dashboard database size, dropping oldest rows first (0 = no size limit)")
		// Content capture is opt-IN, not opt-out. The before/after diff is the dashboard's
		// best view, but it is the one path that writes ARBITRARY agent output to disk, and
		// arbitrary output cannot be allowlisted the way headers and config keys are — it
		// gets pattern scrubbing, and a pattern denylist is always one unseen credential
		// shape behind reality (a review of 22 realistic shapes found 11 leaking). So the
		// default is the safe one and the operator turns it on for their own transcripts.
		dashContent = flag.Bool("dashboard-content", envBool("DASHBOARD_CONTENT", false),
			"capture before/after message text for the diff view; stores arbitrary agent output on disk (scrubbed of known credential shapes and size-capped first), so it is opt-in")
		dashContentCap = flag.Int("dashboard-content-cap", envInt("DASHBOARD_CONTENT_CAP", 16<<10),
			"maximum bytes stored per captured before/after blob")
		dashQueue = flag.Int("dashboard-queue", envInt("DASHBOARD_QUEUE", 4096),
			"capture channel depth; a full channel DROPS events (counted, and shown in the UI) rather than delaying a request")
		dashCIDRs = flag.String("dashboard-trusted-cidrs", envOr("DASHBOARD_TRUSTED_CIDRS", ""),
			"comma-separated CIDRs allowed to view per-request CONTENT and the effective config (loopback always is; aggregates are open)")
		dashBench = flag.String("dashboard-bench-dirs", envOr("DASHBOARD_BENCH_DIRS", ""),
			"comma-separated directories of benchmark runs (each with summary.json + rows-*.json) to ingest")
	)
	flag.Parse()

	cfg, err := loadConfig(*cfgPath, *preset)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *modeFlag != "" {
		cfg.Mode = *modeFlag // flag/env wins over the config file when set
	}
	mode, err := cfg.OperatingMode()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if v, ok := parseBool(*storeFlag); ok {
		cfg.Store.Enabled = &v // flag/env wins over the config file when set
	}

	agg := metrics.NewAggregator()
	emitter := metrics.Tee{agg, metrics.Slog{L: slog.Default()}}
	pipe, err := cfg.Build(emitter)
	if err != nil {
		log.Fatalf("build pipeline: %v", err)
	}

	windows := modelWindows()

	var rec *dash.Recorder
	if *dashOn {
		opts := dash.Options{
			DBPath:         *dashDB,
			RetentionAge:   *dashRetain,
			RetentionBytes: *dashMaxBytes,
			CaptureContent: *dashContent,
			ContentCap:     *dashContentCap,
			QueueSize:      *dashQueue,
			TrustedCIDRs:   splitComma(*dashCIDRs),
			BenchDirs:      splitComma(*dashBench),
			Preset:         cfg.Preset,
			Mode:           dash.ModeActive,
			Effective:      effectiveConfig(cfg, addr, *openai, *anthropic, *bob, *dashDB, *dashContent, *dashCIDRs),
		}
		// A negative retention means "no limit"; a zero means "use the default". Map
		// an explicit 0 from the flag to "no limit", which is what a user typing 0 means.
		if *dashRetain == 0 {
			opts.RetentionAge = -1
		}
		if *dashMaxBytes == 0 {
			opts.RetentionBytes = -1
		}
		r, err := dash.NewRecorder(opts)
		if err != nil {
			log.Fatalf("dashboard: %v", err)
		}
		rec = r
		defer rec.Close()
		if runs, tasks := rec.DB().IngestBenchRoots(opts.BenchDirs); runs > 0 {
			slog.Info("dashboard: ingested benchmark runs", "runs", runs, "tasks", tasks)
		}
		slog.Info("dashboard enabled", "url", "http://"+addr+"/dashboard/", "db", rec.DB().Path(),
			"content_capture", *dashContent)
	}

	h := proxy.New(pipe, cfg.NewStore(), agg, proxy.Options{
		OpenAIUpstream:    *openai,
		AnthropicUpstream: *anthropic,
		BobUpstream:       *bob, // enables the Bob gateway routes when set (BOB_UPSTREAM)
		// Gateway mode: real provider keys live here (eval-containers passes them
		// via env); the agent holds only a placeholder. Empty => pass client auth.
		OpenAIKey:    os.Getenv("OPENAI_API_KEY"),
		AnthropicKey: os.Getenv("ANTHROPIC_API_KEY"),
		ForceModel:   os.Getenv("FORCE_MODEL"),   // eval-containers pins EVAL_MODEL's model here
		CheapModel:   cheapModelFromEnv(),        // static "config"-source LLM for NeedsModel components
		InjectExpand: os.Getenv("INJECT_EXPAND"), // auto (default) | always | never
		CacheMode:    os.Getenv("CACHE_MODE"),    // auto (default) | on | off — cache-aware compaction
		Windows:      windows,                    // dynamic context-window resolver (fraction triggers)
		Prices:       priceResolver(windows),     // per-token rates, so each captured request is priced at write time
		Preset:       cfg.Preset,
		Dashboard:    rec,  // nil unless --dashboard
		Mode:         mode, // sync (default) | observe — explicit, never inferred
		Observe: proxy.ObserveOptions{
			MaxQueue: cfg.Observe.MaxQueue,
			Workers:  cfg.Observe.Workers,
		},

		// Per-request /compact override: swap the pipeline (?preset / header) while
		// keeping this config's component blocks. nil-safe in the handler.
		PipelineFor: func(preset string, names []string) (*components.Pipeline, error) {
			oc := *cfg // override Pipeline only; component blocks + store carry over
			switch {
			case len(names) > 0:
				oc.Pipeline = names
			case preset != "":
				p, ok := config.PresetPipeline(preset) // map lookup, not YAML from request input
				if !ok {
					return nil, fmt.Errorf("unknown preset %q", preset)
				}
				oc.Pipeline = p
			}
			return oc.Build(emitter)
		},
	})

	defer h.Close() // stop the off-path worker pool cleanly (no-op in sync mode)
	if mode == components.ModeObserve {
		slog.Warn("context-guru: OBSERVE MODE — requests are forwarded UNMODIFIED; " +
			"/stats reports what compaction WOULD have saved under potential_*/projected_* keys")
	}
	slog.Info("context-guru-proxy listening", "addr", addr, "pipeline", cfg.Pipeline, "mode", mode)
	if err := http.ListenAndServe(addr, h.Mux()); err != nil {
		log.Fatal(err)
	}
}

func loadConfig(path, preset string) (*config.Config, error) {
	if path != "" {
		return config.Load(path)
	}
	return config.LoadBytes([]byte("preset: " + preset + "\n"))
}

// splitComma splits a comma-separated flag value into trimmed, non-empty items.
func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// priceResolver returns the Pricer side of the window resolver, when it has one.
// A nil Pricer means "no rates known", and every captured row is then marked
// partially accounted rather than priced as free.
func priceResolver(r modelinfo.Resolver) modelinfo.Pricer {
	p, _ := r.(modelinfo.Pricer)
	return p
}

// effectiveConfig assembles the RESOLVED configuration for the dashboard's config
// view — preset expanded, pipeline as actually built, upstream bases and dashboard
// settings included. It is key-allowlisted by dash.RedactConfig before serving, and
// deliberately carries no credential: keys are read from the environment at use
// time and never copied into this map.
func effectiveConfig(cfg *config.Config, addr, openai, anthropic, bob, dbPath string, content bool, cidrs string) map[string]any {
	comps := map[string]any{}
	for name, node := range cfg.Components {
		var v any
		if err := node.Decode(&v); err == nil {
			comps[name] = v
		}
	}
	return map[string]any{
		"preset":               cfg.Preset,
		"pipeline":             cfg.Pipeline,
		"components":           comps,
		"listen_addr":          addr,
		"openai_upstream":      openai,
		"anthropic_upstream":   anthropic,
		"bob_upstream":         bob,
		"force_model":          os.Getenv("FORCE_MODEL"),
		"cache_mode":           envOr("CACHE_MODE", "auto"),
		"inject_expand":        envOr("INJECT_EXPAND", "auto"),
		"cheap_model":          os.Getenv("CHEAP_MODEL"),
		"cheap_model_provider": envOr("CHEAP_MODEL_PROVIDER", "anthropic"),
		"store":                map[string]any{"ttl_seconds": cfg.Store.TTLSeconds, "max_entries": cfg.Store.MaxEntries},
		"dashboard":            map[string]any{"db_path": dbPath, "capture_content": content, "trusted_cidrs": cidrs},
		"build_version":        buildinfo.Version,
		"build_commit":         buildinfo.Commit,
	}
}

// envBool reads a permissive boolean environment variable.
func envBool(key string, def bool) bool {
	if v, ok := parseBool(os.Getenv(key)); ok {
		return v
	}
	return def
}

// envInt reads an integer environment variable, falling back on anything unparseable.
func envInt(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return def
}

// envDuration reads a Go duration environment variable (e.g. "72h").
func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key))); err == nil {
		return d
	}
	return def
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseBool reads a permissive bool override; ok=false for an empty/unknown
// value so the config file's setting is left untouched.
func parseBool(s string) (v, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "on", "yes":
		return true, true
	case "false", "0", "off", "no":
		return false, true
	}
	return false, false
}

// modelWindows builds the dynamic context-window resolver used for fraction-based
// triggers. Default chain: LiteLLM's public prices map (cached) -> small embedded
// fallback. MODEL_INFO_URL overrides the map source; MODEL_INFO=off disables it
// (windows unknown => fraction triggers ignored, absolutes apply).
func modelWindows() modelinfo.Resolver {
	if strings.EqualFold(os.Getenv("MODEL_INFO"), "off") {
		return nil
	}
	return modelinfo.Chain{
		modelinfo.NewLiteLLM(os.Getenv("MODEL_INFO_URL"), nil, 0),
		modelinfo.DefaultStatic(),
	}
}

// cheapModelFromEnv builds the static "config"-source LLM client for NeedsModel
// components (extract code/rlm, summarize with model.source=config). Returns nil
// when CHEAP_MODEL is unset, so those components fall back / no-op.
//
//	CHEAP_MODEL           model id (e.g. claude-haiku-4-5); unset => no client
//	CHEAP_MODEL_PROVIDER  anthropic (default) | openai
//	CHEAP_MODEL_BASE      upstream base URL (default: the matching provider default)
//	CHEAP_MODEL_KEY       API key (default: ANTHROPIC_API_KEY / OPENAI_API_KEY)
//	CHEAP_MODEL_AUTH      anthropic auth scheme: x-api-key (default) | bearer
func cheapModelFromEnv() components.Model {
	model := os.Getenv("CHEAP_MODEL")
	if model == "" {
		return nil
	}
	switch envOr("CHEAP_MODEL_PROVIDER", "anthropic") {
	case "openai":
		return cheapmodel.OpenAI{
			BaseURL: os.Getenv("CHEAP_MODEL_BASE"), Model: model,
			APIKey: envOr("CHEAP_MODEL_KEY", os.Getenv("OPENAI_API_KEY")),
		}
	default:
		return cheapmodel.Anthropic{
			BaseURL: os.Getenv("CHEAP_MODEL_BASE"), Model: model,
			APIKey:     envOr("CHEAP_MODEL_KEY", os.Getenv("ANTHROPIC_API_KEY")),
			AuthScheme: os.Getenv("CHEAP_MODEL_AUTH"),
		}
	}
}
