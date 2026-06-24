// Package observability emits per-request reduction telemetry in OpenTelemetry GenAI
// vocabulary (gen_ai.*) plus this component's savings attributes. The Emitter
// interface lets a host plug its own sink; the default emits structured logs via
// stdlib slog so the core stays dependency-free.
//
// ponytail: slog in gen_ai.* field names, not the OTel SDK — keeps zero deps. A real
// OTLP span exporter is a drop-in Emitter implementation (the named upgrade); Kagenti
// hosts typically already run a collector and can map these fields, or supply an
// Emitter that opens spans.
package observability

import (
	"context"
	"log/slog"
)

// Event is one reduction's telemetry. Field comments give the OTel attribute key the
// default emitter uses.
type Event struct {
	System       string  // gen_ai.system (e.g. "anthropic", "openai")
	RequestModel string  // gen_ai.request.model
	Surface      string  // context_engineering.surface
	TokensBefore int     // context_engineering.tokens.before
	TokensAfter  int     // context_engineering.tokens.after
	TokensSaved  int     // context_engineering.tokens.saved
	Ratio        float64 // context_engineering.tokens.ratio
	CacheInject  bool    // context_engineering.cache_injected
	Extracted    bool    // context_engineering.extracted
	StageErrors  int     // context_engineering.stage_errors
}

// Emitter records reduction events. Implementations must be safe for concurrent use.
type Emitter interface {
	Emit(ctx context.Context, e Event)
}

// Nop discards events.
type Nop struct{}

func (Nop) Emit(context.Context, Event) {}

// SlogEmitter logs events as structured records in gen_ai.* vocabulary.
type SlogEmitter struct{ Logger *slog.Logger }

func (s SlogEmitter) Emit(ctx context.Context, e Event) {
	l := s.Logger
	if l == nil {
		l = slog.Default()
	}
	l.LogAttrs(ctx, slog.LevelInfo, "context.reduction",
		slog.String("gen_ai.system", e.System),
		slog.String("gen_ai.request.model", e.RequestModel),
		slog.String("context_engineering.surface", e.Surface),
		slog.Int("context_engineering.tokens.before", e.TokensBefore),
		slog.Int("context_engineering.tokens.after", e.TokensAfter),
		slog.Int("context_engineering.tokens.saved", e.TokensSaved),
		slog.Float64("context_engineering.tokens.ratio", e.Ratio),
		slog.Bool("context_engineering.cache_injected", e.CacheInject),
		slog.Bool("context_engineering.extracted", e.Extracted),
		slog.Int("context_engineering.stage_errors", e.StageErrors),
	)
}
