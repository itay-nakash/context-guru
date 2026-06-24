// Command lab-cx is the standalone context-engineering proxy: it sits between any
// LLM coding agent and the model API, reduces token cost on the request, and
// forwards it upstream. The same engine that powers this binary is importable as
// a library (see ../../engine) so other hosts — e.g. a Kagenti AuthBridge plugin
// — can wrap it without running this process.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
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
  lab-cx version                                                     print version

Point your agent at the proxy, e.g.:
  ANTHROPIC_BASE_URL=http://localhost:8080  OPENAI_BASE_URL=http://localhost:8080/v1

Flags:
  --addr      listen address (default :8080)
  --preset    safe|balanced|aggressive|cache|coding|mcp (default balanced)
  --upstream  forward ALL requests here (e.g. an eval gateway); default routes by
              provider (api.anthropic.com / api.openai.com)
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
	upstream := fs.String("upstream", "", "forward all requests to this base URL")
	extractModel := fs.String("extract-model", "", "enable cheap-model extraction with this model (e.g. claude-haiku-4-5)")
	extractProvider := fs.String("extract-provider", "anthropic", "extraction model provider: anthropic|openai")
	extractBase := fs.String("extract-base", "", "base URL for the extraction model (default per provider)")
	maxBodyBytes := fs.Int64("max-body-bytes", 33554432, "cap request body size in bytes (32 MiB default); 0 means no cap")
	upstreamTimeout := fs.String("upstream-timeout", "0s", "max total time per upstream request (e.g. 30s); 0 means no timeout. WARNING: a non-zero value caps the WHOLE request including streamed responses and can truncate long LLM SSE streams")
	_ = fs.Parse(args)

	upstreamTimeoutDur, err := time.ParseDuration(*upstreamTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lab-cx: invalid --upstream-timeout %q: %v\n", *upstreamTimeout, err)
		os.Exit(2)
	}

	if os.Getenv("WINNOW_DISABLE") == "1" {
		fmt.Fprintln(os.Stderr, "lab-cx: WINNOW_DISABLE=1 — running as a transparent passthrough")
	}
	settings := config.Preset(*preset)
	if os.Getenv("WINNOW_DISABLE") == "1" {
		settings.Disabled = true
	}
	eng := engine.New(settings, nil, nil)
	if *extractModel != "" {
		key := os.Getenv("WINNOW_EXTRACT_KEY")
		var model engine.Model
		switch *extractProvider {
		case "openai":
			if key == "" {
				key = os.Getenv("OPENAI_API_KEY")
			}
			model = cheapmodel.OpenAI{
				BaseURL: *extractBase, APIKey: key, Model: *extractModel,
			}
		case "anthropic", "":
			if key == "" {
				key = os.Getenv("ANTHROPIC_API_KEY")
			}
			model = cheapmodel.Anthropic{
				BaseURL: *extractBase, APIKey: key, Model: *extractModel,
			}
		default:
			fmt.Fprintf(os.Stderr, "lab-cx: unknown --extract-provider %q (want anthropic|openai)\n", *extractProvider)
			os.Exit(2)
		}
		eng.EnableExtract(model, engine.DefaultExtractConfig())
		fmt.Fprintf(os.Stderr, "lab-cx: extraction enabled (provider=%s model=%s)\n", *extractProvider, *extractModel)
	}
	handler := proxyhttp.New(proxyhttp.Config{
		Engine: eng, Upstream: *upstream,
		Emitter:         observability.SlogEmitter{},
		MaxBodyBytes:    *maxBodyBytes,
		UpstreamTimeout: upstreamTimeoutDur,
	})

	fmt.Fprintf(os.Stderr, "lab-cx proxy listening on %s (preset=%s)\n", *addr, *preset)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		fmt.Fprintln(os.Stderr, "lab-cx:", err)
		os.Exit(1)
	}
}
