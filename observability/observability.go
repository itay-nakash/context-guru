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

// Event is one reduction's telemetry — every field a single call exposes. Field
// comments give the OTel attribute key the default emitter uses. The Aggregator folds
// these into process-wide totals, a per-session breakdown, and a recent-calls ring,
// all served at /stats.
type Event struct {
	System       string // gen_ai.system (e.g. "anthropic", "openai")
	RequestModel string // gen_ai.request.model
	Surface      string // context_engineering.surface
	SessionID    string // context_engineering.session_id (stable per conversation)

	TokensBefore int     // context_engineering.tokens.before
	TokensAfter  int     // context_engineering.tokens.after
	TokensSaved  int     // context_engineering.tokens.saved
	Ratio        float64 // context_engineering.tokens.ratio

	CacheInject bool // context_engineering.cache_injected
	Extracted   bool // context_engineering.extracted
	StageErrors int  // context_engineering.stage_errors

	// Richer per-call reduction detail (from the engine Report).
	ToolsTotal      int  // context_engineering.tools.total (tool definitions in the request)
	ToolDefTokens   int  // context_engineering.tools.def_tokens (their token cost)
	ReducedCount    int  // context_engineering.reduced_blocks (blocks the deterministic pass reduced)
	CandidatesCount int  // context_engineering.extract_candidates (large outputs handed to the extractor)
	FrozenCount     int  // context_engineering.frozen_messages (prefix left untouched)
	Rehydrated      int  // context_engineering.rehydrated (markers restored on a compaction turn)
	AtCompaction    bool // context_engineering.at_compaction

	LatencyMillis int // context_engineering.added_latency_ms (time the reduce path added)
}

// Emitter records reduction events. Implementations must be safe for concurrent use.
type Emitter interface {
	Emit(ctx context.Context, e Event)
}

// Nop discards events.
type Nop struct{}

func (Nop) Emit(context.Context, Event) {}

// Tee fans an event out to several Emitters in order (e.g. stream via slog AND
// accumulate in an Aggregator). nil entries are skipped.
type Tee []Emitter

func (t Tee) Emit(ctx context.Context, e Event) {
	for _, em := range t {
		if em != nil {
			em.Emit(ctx, e)
		}
	}
}

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
		slog.String("context_engineering.session_id", e.SessionID),
		slog.Int("context_engineering.tokens.before", e.TokensBefore),
		slog.Int("context_engineering.tokens.after", e.TokensAfter),
		slog.Int("context_engineering.tokens.saved", e.TokensSaved),
		slog.Float64("context_engineering.tokens.ratio", e.Ratio),
		slog.Bool("context_engineering.cache_injected", e.CacheInject),
		slog.Bool("context_engineering.extracted", e.Extracted),
		slog.Int("context_engineering.stage_errors", e.StageErrors),
		slog.Int("context_engineering.tools.total", e.ToolsTotal),
		slog.Int("context_engineering.tools.def_tokens", e.ToolDefTokens),
		slog.Int("context_engineering.reduced_blocks", e.ReducedCount),
		slog.Int("context_engineering.extract_candidates", e.CandidatesCount),
		slog.Int("context_engineering.frozen_messages", e.FrozenCount),
		slog.Int("context_engineering.rehydrated", e.Rehydrated),
		slog.Bool("context_engineering.at_compaction", e.AtCompaction),
		slog.Int("context_engineering.added_latency_ms", e.LatencyMillis),
	)
}
