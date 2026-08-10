package apply

import (
	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/modes"
	"github.com/rossoctl/context-guru/session"
	"github.com/tidwall/gjson"
)

// Opts is BodyOpts' input: everything the positional BodyFull takes, plus the
// operating mode (#31) and the per-session generation state async mode needs.
//
// A struct rather than a 13th positional argument: the parameter list was already at
// the limit of readability, and modes add three fields that only one host sets.
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

	// Mode is the operating mode. Empty means components.ModeSync, so a caller that
	// does not know about modes gets exactly today's behavior.
	Mode components.Mode
	// Deferred marks the OFF-PATH async run: nothing it produces is forwarded, it
	// exists to populate the frozen state later turns replay. Only the async worker
	// sets it, and it is what re-enables the LLM components the inline async pass
	// deliberately withholds.
	Deferred bool
	// CacheUncompactedTail disables async's tail cache protection. In async mode the
	// tail beyond the cached prefix is by construction content a not-yet-landed
	// compaction is going to replace, so by default no breakpoint is placed there —
	// protecting cache-write economics, because a breakpoint written over a tail we
	// then replace converts a 0.1x read into a 1.25x write. Set true only for a backend confirmed
	// not to cache, where the protection costs a breakpoint slot and buys nothing.
	CacheUncompactedTail bool
	// PrevLen, when non-nil, supplies the cached-prefix boundary (the number of
	// normalized messages the previous turn carried) instead of resolving it. The
	// off-path async job MUST set it: it runs against the body of turn N but at a time
	// when the tracker has already advanced past it, so re-resolving would either
	// gate everything away or gate nothing, and a frozen decision made under the wrong
	// boundary is replayed on every later turn — churning exactly the cached prefix
	// cache-awareness exists to protect.
	PrevLen *int
	// Tracker, when set, owns the per-session cached-prefix boundary and compaction
	// generation. Supplying it also removes the concurrent-turn race in the legacy
	// read-then-deferred-write of prevLen (#31/#25). nil => legacy store-backed path.
	Tracker *modes.Tracker
}

// Result is BodyOpts' output.
type Result struct {
	// Body is the body to forward. Always valid: on any trouble it is the input.
	Body []byte
	// Changed is false when Body is the untouched input.
	Changed bool
	// Session is the resolved session id (the caller usually cannot compute it: it
	// falls back to a content hash of system + first user message).
	Session string
	// PrevLen is the cached-prefix boundary this request was built with (the previous
	// turn's normalized message count), so an off-path job can reuse the exact same one.
	PrevLen int
	// Generation is the session's compaction generation this result was built from,
	// when a Tracker was supplied. An async job carries it and its result is discarded
	// if the session has moved on (Tracker.CommitIfCurrent).
	Generation uint64
	// Run is the pipeline's report for this request, nil when the pipeline did not run.
	// Observe mode needs it: the run is the ONLY output, since the body is thrown away.
	Run *components.RunReport
}

// SessionOf resolves the session id apply will use for this body — the explicit id
// when the host has one, else the content hash of system + first user message. Hosts
// that must key off-path work by session (the async worker's dedup key and the
// generation check) need the SAME id apply computes, so this exposes that one
// resolution rather than letting a second implementation drift from it.
func SessionOf(provider bschemas.ModelProvider, body []byte, explicit string) string {
	if explicit != "" {
		return session.Resolve(explicit, "", "")
	}
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return session.Resolve("", "", "")
	}
	norm, _ := normalize(provider, msgs.Array())
	sys, firstUser := systemAndFirstUser(norm)
	return session.Resolve("", sys, firstUser)
}
