// Command lab-cx is the standalone context-engineering proxy: it sits between any
// LLM coding agent and the model API, reduces token cost on the request, and
// forwards it upstream. The same engine that powers this binary is importable as
// a library (see ../../engine) so other hosts — e.g. a Kagenti AuthBridge plugin
// — can wrap it without running this process.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kagenti/lab-context-engineering/config"
	"github.com/kagenti/lab-context-engineering/engine"
	"github.com/kagenti/lab-context-engineering/internal/buildinfo"
	"github.com/kagenti/lab-context-engineering/internal/cheapmodel"
	"github.com/kagenti/lab-context-engineering/internal/proxyhttp"
	"github.com/kagenti/lab-context-engineering/observability"
)

func usage() {
	fmt.Fprintf(os.Stderr, `lab-cx — context-engineering proxy for LLM agents

Usage:
  lab-cx proxy [--addr :8080] [--preset balanced] [--upstream URL]   start the proxy
  lab-cx stats [--addr http://localhost:8080]                        print live /stats
  lab-cx version                                                     print version

Point your agent at the proxy, e.g.:
  ANTHROPIC_BASE_URL=http://localhost:8080  OPENAI_BASE_URL=http://localhost:8080/v1

Flags:
  --addr      listen address (default :8080)
  --preset    safe|balanced|aggressive|cache|coding|mcp (default balanced)
  --config    path to a YAML config file (overrides --preset; names which reducers/
              encoders/extract-strategies/stages run, by name; see configs/lab-cx.yaml).
              --extract-model/-provider/-auth/-base still override the file's transport.
  --upstream  forward ALL requests here (e.g. an eval gateway); default routes by
              provider (api.anthropic.com / api.openai.com)
  --mode      initial reduction mode: on|off|deterministic (default on). Settable at
              runtime without a restart: POST /labcx/mode {"mode":"off"} (GET reads it).
              off = transparent passthrough; deterministic is reserved (behaves like on).
  --max-body-bytes  cap request body size in bytes (default 33554432 = 32 MiB);
                    0 means no cap
  --upstream-timeout  max total time per upstream request (e.g. 30s; default 0s = no
                      timeout). WARNING: a non-zero value caps the WHOLE request
                      including streamed responses and can truncate long LLM SSE streams
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Printf("lab-cx %s (%s)\n", buildinfo.Version, buildinfo.Commit)
	case "proxy":
		runProxy(os.Args[2:])
	case "stats":
		runStats(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func runProxy(args []string) {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	preset := fs.String("preset", "balanced", "settings preset")
	configPath := fs.String("config", "", "path to a YAML config file (overrides --preset; see configs/lab-cx.yaml)")
	upstream := fs.String("upstream", "", "forward all requests to this base URL")
	mode := fs.String("mode", "on", "initial reduction mode: on|off|deterministic (runtime-settable via POST /labcx/mode)")
	extractModel := fs.String("extract-model", "", "enable cheap-model extraction with this model (e.g. claude-haiku-4-5)")
	extractProvider := fs.String("extract-provider", "anthropic", "extraction model provider: anthropic|openai")
	extractBase := fs.String("extract-base", "", "base URL for the extraction model (default per provider)")
	extractAuth := fs.String("extract-auth", "x-api-key", "anthropic auth scheme: x-api-key|bearer (bearer for gateway endpoints)")
	summarizeModel := fs.String("summarize-model", "", "enable LLM summarization with this model (e.g. claude-haiku-4-5)")
	summarizeProvider := fs.String("summarize-provider", "anthropic", "summarizer model provider: anthropic|openai")
	summarizeBase := fs.String("summarize-base", "", "base URL for the summarizer model (default per provider)")
	summarizeAuth := fs.String("summarize-auth", "x-api-key", "anthropic auth scheme: x-api-key|bearer")
	reduceCachedPrefix := fs.Bool("reduce-cached-prefix", false, "also run the deterministic reducers and the LLM extractor on the client's self-cached prefix (e.g. Claude Code) instead of deferring to its cache")
	maxBodyBytes := fs.Int64("max-body-bytes", 33554432, "cap request body size in bytes (32 MiB default); 0 means no cap")
	upstreamTimeout := fs.String("upstream-timeout", "0s", "max total time per upstream request (e.g. 30s); 0 means no timeout. WARNING: a non-zero value caps the WHOLE request including streamed responses and can truncate long LLM SSE streams")
	_ = fs.Parse(args)

	upstreamTimeoutDur, err := time.ParseDuration(*upstreamTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lab-cx: invalid --upstream-timeout %q: %v\n", *upstreamTimeout, err)
		os.Exit(2)
	}

	initialMode, ok := proxyhttp.ParseMode(*mode)
	if !ok {
		fmt.Fprintf(os.Stderr, "lab-cx: invalid --mode %q (want on|off|deterministic)\n", *mode)
		os.Exit(2)
	}

	if os.Getenv("LABCX_DISABLE") == "1" {
		fmt.Fprintln(os.Stderr, "lab-cx: LABCX_DISABLE=1 — running as a transparent passthrough")
	}

	// Resolve settings: a --config file (with named component selection) takes
	// precedence over --preset. The file may also name each LLM compactor's transport
	// (provider/model/auth/base/credentials); the matching --extract-*/--summarize-*
	// flags still override it.
	var settings config.Settings
	var tr config.Transports
	settingsSrc := "preset=" + *preset
	if *configPath != "" {
		var loadErr error
		settings, tr, loadErr = config.LoadFull(*configPath)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "lab-cx: %v\n", loadErr)
			os.Exit(2)
		}
		settingsSrc = "config=" + *configPath
	} else {
		settings = config.Preset(*preset)
	}
	// --extract-*/--summarize-* flags override the config transport block (explicit flag
	// wins; else the config value; else the flag default).
	if v := flagOrConfig(fs, "extract-model", *extractModel, tr.Extract.Model); v != "" {
		*extractModel = v
	}
	if v := flagOrConfig(fs, "extract-provider", *extractProvider, tr.Extract.Provider); v != "" {
		*extractProvider = v
	}
	if v := flagOrConfig(fs, "extract-auth", *extractAuth, tr.Extract.Auth); v != "" {
		*extractAuth = v
	}
	if v := flagOrConfig(fs, "extract-base", *extractBase, tr.Extract.Base); v != "" {
		*extractBase = v
	}
	if v := flagOrConfig(fs, "summarize-model", *summarizeModel, tr.Summarize.Model); v != "" {
		*summarizeModel = v
	}
	if v := flagOrConfig(fs, "summarize-provider", *summarizeProvider, tr.Summarize.Provider); v != "" {
		*summarizeProvider = v
	}
	if v := flagOrConfig(fs, "summarize-auth", *summarizeAuth, tr.Summarize.Auth); v != "" {
		*summarizeAuth = v
	}
	if v := flagOrConfig(fs, "summarize-base", *summarizeBase, tr.Summarize.Base); v != "" {
		*summarizeBase = v
	}

	if os.Getenv("LABCX_DISABLE") == "1" {
		settings.Disabled = true
	}
	if *reduceCachedPrefix {
		settings.ReduceCachedPrefix = true
	}
	eng := engine.New(settings, nil, nil)

	// Wire the extract compactor. Source "incoming" reuses the proxied request's own
	// model + credentials (no static client); otherwise build a static client.
	useIncoming := false
	if tr.Extract.Source == "incoming" {
		eng.EnableExtractSpec(engine.ModelSpec{UseIncoming: true}, engine.DefaultExtractConfig())
		useIncoming = true
		fmt.Fprintln(os.Stderr, "lab-cx: extraction enabled (source=incoming)")
	} else if *extractModel != "" {
		key := resolveKey(tr.Extract, *extractProvider, "LABCX_EXTRACT_KEY")
		model, err := buildModel(*extractProvider, *extractModel, *extractBase, *extractAuth, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lab-cx: %v\n", err)
			os.Exit(2)
		}
		eng.EnableExtract(model, engine.DefaultExtractConfig())
		fmt.Fprintf(os.Stderr, "lab-cx: extraction enabled (provider=%s model=%s)\n", *extractProvider, *extractModel)
	}

	// Wire the summarize compactor (same credential mechanism as extract).
	if tr.Summarize.Source == "incoming" {
		eng.EnableSummarizeSpec(engine.ModelSpec{UseIncoming: true}, engine.DefaultSummarizeConfig())
		useIncoming = true
		fmt.Fprintln(os.Stderr, "lab-cx: summarization enabled (source=incoming)")
	} else if *summarizeModel != "" {
		key := resolveKey(tr.Summarize, *summarizeProvider, "LABCX_SUMMARIZE_KEY")
		model, err := buildModel(*summarizeProvider, *summarizeModel, *summarizeBase, *summarizeAuth, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lab-cx: %v\n", err)
			os.Exit(2)
		}
		eng.EnableSummarize(model, engine.DefaultSummarizeConfig())
		fmt.Fprintf(os.Stderr, "lab-cx: summarization enabled (provider=%s model=%s)\n", *summarizeProvider, *summarizeModel)
	}

	agg := observability.NewAggregator(defaultCostRates())
	cfg := proxyhttp.Config{
		Engine: eng, Upstream: *upstream,
		// Stream each event via slog AND fold it into the Aggregator served at /stats.
		Emitter:         observability.Tee{observability.SlogEmitter{}, agg},
		Aggregator:      agg,
		MaxBodyBytes:    *maxBodyBytes,
		UpstreamTimeout: upstreamTimeoutDur,
		InitialMode:     initialMode,
	}
	if useIncoming {
		// A compactor reuses the proxied request's own model + credentials: build a
		// per-request model client from the incoming auth header, request model id, and
		// resolved upstream base.
		cfg.BuildRequestModel = incomingModel
	}
	handler := proxyhttp.New(cfg)

	fmt.Fprintf(os.Stderr, "lab-cx proxy listening on %s (%s)\n", *addr, settingsSrc)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		fmt.Fprintln(os.Stderr, "lab-cx:", err)
		os.Exit(1)
	}
}

// defaultCostRates is a small input/output price table ($/MTok) used to estimate the
// dollar value of saved tokens. Savings are priced on input rates (reduction removes
// input tokens). DefaultCostKey prices any model not listed here. Edit to match your
// provider's published pricing; values here are illustrative defaults.
func defaultCostRates() map[string]observability.CostRate {
	return map[string]observability.CostRate{
		"claude-haiku-4-5":           {InputPerMTok: 1.00, OutputPerMTok: 5.00},
		observability.DefaultCostKey: {InputPerMTok: 3.00, OutputPerMTok: 15.00},
	}
}

// runStats GETs /stats from a running proxy and prints the JSON plus a one-line summary.
func runStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	addr := fs.String("addr", "http://localhost:8080", "base URL of a running lab-cx proxy")
	_ = fs.Parse(args)

	url := *addr + "/stats"
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lab-cx: GET %s: %v\n", url, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "lab-cx: %s returned %d: %s\n", url, resp.StatusCode, string(body))
		os.Exit(1)
	}

	var snap observability.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		fmt.Fprintf(os.Stderr, "lab-cx: bad /stats response: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(body))
	fmt.Println(observability.SummaryOf(snap))
}

// flagOrConfig resolves an extract-* knob: an explicitly-set flag wins; otherwise the
// config value (if non-empty); otherwise the flag's current value (its default). This
// lets a --config file name the extraction transport while --extract-* flags override.
func flagOrConfig(fs *flag.FlagSet, name, flagVal, cfgVal string) string {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	if set {
		return flagVal
	}
	if cfgVal != "" {
		return cfgVal
	}
	return flagVal
}

// resolveKey resolves an LLM compactor's API key: an inline key in the config wins,
// then the config-named env var (key_env), then the compactor's fallback env var, then
// the provider default env. Secrets are never required in YAML, but allowed.
func resolveKey(tr config.ModelTransport, provider, fallbackEnv string) string {
	if tr.APIKey != "" {
		return tr.APIKey
	}
	if tr.KeyEnv != "" {
		if v := os.Getenv(tr.KeyEnv); v != "" {
			return v
		}
	}
	if fallbackEnv != "" {
		if v := os.Getenv(fallbackEnv); v != "" {
			return v
		}
	}
	if provider == "openai" {
		return os.Getenv("OPENAI_API_KEY")
	}
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		return v
	}
	return os.Getenv("ANTHROPIC_AUTH_TOKEN")
}

// buildModel constructs a cheap-model client for a provider/model/base/auth/key.
func buildModel(provider, model, base, auth, key string) (engine.Model, error) {
	switch provider {
	case "openai":
		return cheapmodel.OpenAI{BaseURL: base, APIKey: key, Model: model}, nil
	case "anthropic", "":
		return cheapmodel.Anthropic{BaseURL: base, APIKey: key, Model: model, AuthScheme: auth}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want anthropic|openai)", provider)
	}
}

// incomingModel builds a per-request model client that reuses the proxied request's own
// model id + credentials (from the incoming auth header) and the resolved upstream base.
// Returns nil when no usable credential is present (the compactor then no-ops).
func incomingModel(surfaceName, model, base string, h http.Header) engine.Model {
	switch surfaceName {
	case "openai":
		if key := bearerToken(h.Get("Authorization")); key != "" {
			return cheapmodel.OpenAI{BaseURL: base, APIKey: key, Model: model}
		}
	case "anthropic":
		if key := h.Get("x-api-key"); key != "" {
			return cheapmodel.Anthropic{BaseURL: base, APIKey: key, Model: model, AuthScheme: "x-api-key"}
		}
		if key := bearerToken(h.Get("Authorization")); key != "" {
			return cheapmodel.Anthropic{BaseURL: base, APIKey: key, Model: model, AuthScheme: "bearer"}
		}
	}
	return nil
}

func bearerToken(h string) string {
	const p = "Bearer "
	if strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
