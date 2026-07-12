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
	"log"
	"log/slog"
	"net/http"
	"os"

	_ "github.com/kagenti/context-guru/components/all"
	"github.com/kagenti/context-guru/config"
	"github.com/kagenti/context-guru/metrics"
	"github.com/kagenti/context-guru/proxy"
)

func main() {
	var (
		addr      = envOr("LISTEN_ADDR", ":4000")
		cfgPath   = flag.String("config", envOr("CONFIG", ""), "path to context-guru YAML config")
		preset    = flag.String("preset", envOr("PRESET", "balanced"), "preset to use when --config is absent")
		openai    = flag.String("openai-upstream", envOr("OPENAI_UPSTREAM", "https://api.openai.com"), "OpenAI upstream base URL")
		anthropic = flag.String("anthropic-upstream", envOr("ANTHROPIC_UPSTREAM", "https://api.anthropic.com"), "Anthropic upstream base URL")
	)
	flag.Parse()

	cfg, err := loadConfig(*cfgPath, *preset)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	agg := metrics.NewAggregator()
	emitter := metrics.Tee{agg, metrics.Slog{L: slog.Default()}}
	pipe, err := cfg.Build(emitter)
	if err != nil {
		log.Fatalf("build pipeline: %v", err)
	}

	h := proxy.New(pipe, cfg.NewStore(), agg, proxy.Options{
		OpenAIUpstream:    *openai,
		AnthropicUpstream: *anthropic,
		// Gateway mode: real provider keys live here (eval-containers passes them
		// via env); the agent holds only a placeholder. Empty => pass client auth.
		OpenAIKey:    os.Getenv("OPENAI_API_KEY"),
		AnthropicKey: os.Getenv("ANTHROPIC_API_KEY"),
		ForceModel:   os.Getenv("FORCE_MODEL"), // eval-containers pins EVAL_MODEL's model here
	})

	slog.Info("context-guru-proxy listening", "addr", addr, "pipeline", cfg.Pipeline)
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
