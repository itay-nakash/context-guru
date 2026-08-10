package apply

import (
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/modes"
)

// Opts is BodyOpts' input: everything the positional BodyFull takes, plus the operating
// mode (#31) and the per-session boundary tracker.
//
// A struct rather than more positional arguments: the parameter list was already at the
// limit of readability, and these fields are set by one host only.
type Opts struct {
	Provider bschemas.ModelProvider
	Body     []byte
	// Session is the host-supplied session id ("" => content hash).
	Session string
	Bypass  bool
	Models  components.ModelSpec
	// Window is the model's resolved context window (max input tokens; 0 = unknown).
	Window int
	// CacheMode is "auto" (default) | "on" | "off" — see resolveCacheAware.
	CacheMode string

	// Mode is the operating mode. Empty means components.ModeSync, so a caller that does
	// not know about modes gets exactly today's behavior.
	Mode components.Mode
	// Tracker, when set, owns the per-session cached-prefix boundary. Supplying it also
	// removes the concurrent-turn race in the legacy read-then-deferred-write of prevLen.
	// nil => the legacy store-backed path, unchanged for library callers and /compact.
	Tracker *modes.Tracker
}

// Result is BodyOpts' output.
type Result struct {
	// Body is the body to forward. Always valid: on any trouble it is the input.
	Body []byte
	// Changed is false when Body is the untouched input.
	Changed bool
	// Session is the resolved session id (the caller usually cannot compute it: it falls
	// back to a content hash of system + first user message).
	Session string
	// Run is the pipeline's report for this request, nil when the pipeline did not run.
	// Observe mode needs it: the run is the ONLY output, since the body is thrown away.
	Run *components.RunReport
}
