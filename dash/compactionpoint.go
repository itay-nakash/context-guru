package dash

import (
	"strings"

	"github.com/rossoctl/context-guru/internal/modelinfo"
)

// THE COMPACTION POINT: the definition this whole feature's arithmetic rests on.
//
// # The definition
//
//	C(deployment, client, model) is the PROVIDER-BILLED INPUT at which the conversation's own
//	compaction mechanism acts -- the largest prompt that mechanism allows to be sent before it
//	rewrites the transcript.
//
// Two figures derive from it and nothing else should:
//
//	the TRIGGER   summarize fires at  frac x C          (shipped frac 0.9)
//	the SPAN      an episode covers   C - frac x C       = (1 - frac) x C
//
// The reason both come from C rather than from the model window W is the argument for the feature:
// compacting just BEFORE the conversation's own mechanism would have acted captures the saving of a
// large prefix going cold, and costs no accuracy that was not already going to be lost -- because a
// compaction was going to happen there anyway. Firing against W instead is only correct when C == W.
//
// # WHICH RULER, and why this definition never needs the client's own number
//
// C is in provider-billed input: `fresh_input + cache_read + cache_write`. That is the ruler the gate
// compares (Ctx.PrevBilledInput), the ruler a context window is stated in, and the only ruler every
// party shares. The same request is counted three ways and they differ by a third:
//
//	provider-billed input        199,184   = 0.996 of a 200,000 window   <-- C is in THIS
//	Claude Code's own count      ~167,000  = 0.835                        <-- never used here
//	our message-text count       147,493   = 0.737                        <-- tokens_before
//
// A client thresholding at ~167,000 of ITS count and the provider billing 199,184 on that same
// request are the SAME EVENT. So C is DEFINED as an observation in billed tokens, not as a
// translation of the client's threshold -- which means the conversion factor between the rulers never
// has to be known, and cannot be got wrong. Putting 0.835 here would be the units error
// Trigger.Fires shipped with, one level up.
//
// # "WHENEVER THE NATIVE COMPACTION WORKS" -- the four cases, because it genuinely differs
//
//  1. THE CLIENT COMPACTS (Claude Code and similar agents). C is OBSERVED: the billed input of a
//     request the agent-compaction detector flagged. proxy/agentcompaction.go already writes that
//     verdict to `requests.bypassed`, so C is one column away. Verified on a real session: exactly
//     one of 53 rows carried bypassed=1, and it billed 199,184 -- the transcript at its largest.
//     A size drop in tokens_before on the following turn is an independent second marker and agreed
//     exactly. REQUIRE BOTH: the phrase detector has a reachable false positive (the phrase is quoted
//     verbatim in this repo's own docs, so an agent that reads that page gets it into a tool_result),
//     and a false C poisons the trigger for the whole model.
//
//  2. WE ASK THE PROVIDER TO COMPACT (#241, the beta compact_20260112). C is CHOSEN, not observed:
//     it is the `trigger.input_tokens` value we set. Exactly known, no measurement needed.
//
//  3. NOTHING COMPACTS (a raw API client, llm-d, a self-hosted backend). There is no native
//     compaction, so the conversation grows until the PROVIDER rejects it. C is then the hard limit,
//     which is the model window in billed tokens: C == W, and that is a FACT rather than a fallback.
//     This is also the deployment where this component matters most, because nothing else stands
//     between the session and a 400.
//
//  4. UNKNOWN. A client never observed compacting, on a deployment where we cannot tell case 1 from
//     case 3. C == W is the only usable value, but it is a GUESS here where it was a fact in case 3,
//     and the two must not report alike -- see ceilingProvenance.
//
// # WHAT VARIES, all of which is why C cannot be a constant
//
//   - By MODEL. A client thresholding on a fraction of the window scales C with W: haiku 200K against
//     sonnet/opus 1M.
//   - By CLIENT, and by that client's CONFIGURATION. Claude Code's auto-compact threshold is
//     settable, so two tenants on the same model legitimately have different C. This is the largest
//     source of variation and the one a shipped table cannot capture.
//   - By CLIENT VERSION. The 0.996 measured here and a ~0.835 reported elsewhere may be exactly this.
//   - By PROVIDER, twice over: it sets the hard limit for case 3, and whether case 2 exists at all.
//
// So C is properly per (tenant, client, model) and LEARNED. The table below is a fallback for a
// deployment with no observations yet, and #239 is the issue for learning it.
//
// # THE STATISTIC, once there are several observations
//
// Observations vary, because how much the last turn added before the client acted varies. A LOW
// percentile is conservative for BOTH uses, which is what makes it a principled choice rather than a
// taste:
//
//   - the TRIGGER: a lower C fires earlier, which still beats the client's own mechanism. A C that is
//     too HIGH is the dangerous one -- the client resets before billed input ever reaches frac x C,
//     so the component never fires at all and looks broken.
//   - the SPAN: a lower C narrows the span, which under-reports.
//
// And note what an observation bounds. The client acts when it EXCEEDS its threshold, so the flagged
// request is at or above it: an observed C is an UPPER bound on the threshold in billed terms. Taking
// a low percentile and then frac x C pulls safely under it twice.
type ceilingProvenance string

const (
	// ceilingMeasured: case 1 or 2. Observed on a real client run in billed tokens, or set by us.
	ceilingMeasured ceilingProvenance = "measured"
	// ceilingAssumed: reported behaviour or reasoned bound, not observed. Used because the
	// alternative is publishing nothing, and named so it is not mistaken for the first kind.
	ceilingAssumed ceilingProvenance = "assumed"
	// ceilingDefault: case 4. No entry and no observation, so C == W as a guess. Distinct from case 3,
	// where C == W is a fact -- this package cannot yet tell the two apart, which is itself worth
	// reporting rather than smoothing over.
	ceilingDefault ceilingProvenance = "default"
)

// clientCeiling is one C: the fraction, where it came from, and enough of the why that a maintainer
// can tell whether it still holds.
type clientCeiling struct {
	Frac float64
	Prov ceilingProvenance
	Note string
}

// clientCeilings is matched by SUBSTRING against the normalized model id, LONGEST pattern first, so
// `claude-haiku-4-5` beats a bare `claude` and a provider prefix or a `[1m]` variant suffix does not
// defeat the match (modelinfo.NormalizeID strips both).
//
// The lookup sorts by pattern length rather than trusting declaration order, because a table whose
// correctness depends on nobody appending in the wrong place is a table that gets appended to in the
// wrong place -- the exact defect #233 was, in the window table next door.
//
// EVERY ENTRY HERE IS A FALLBACK. It is what a deployment gets before it has observations of its own,
// and it cannot capture the largest source of variation (the client's configured threshold). Two of
// the three entries are not measurements and say so.
var clientCeilings = []struct {
	Pattern string
	Ceiling clientCeiling
}{
	{"claude-haiku-4-5", clientCeiling{
		Frac: 0.996,
		Prov: ceilingMeasured,
		Note: "case 1, measured: on a real Claude Code session the request the agent-compaction " +
			"detector flagged billed 199,184 of a 200,000 window, and the following turn's " +
			"tokens_before drop agreed. n=1, one client version, one configuration. The client's own " +
			"indicator read ~167,000 on that same request, which is the same event in the client's " +
			"ruler rather than a second measurement.",
	}},
	{"claude-opus-5", clientCeiling{
		Frac: 1.00,
		Prov: ceilingAssumed,
		Note: "case 4 dressed as case 1: the repo owner's observation that Claude Code on Opus runs " +
			"to essentially 100% of the window before compacting. Not observed in billed tokens by " +
			"any run. 1.00 is also the LEAST conservative value available, and too-high is the " +
			"dangerous direction for the trigger -- if the real C is lower, billed input never " +
			"reaches 0.9 x 1.00 x W and the component never fires.",
	}},
	{"claude-sonnet-5", clientCeiling{
		Frac: 1.00,
		Prov: ceilingAssumed,
		Note: "no observation of any kind. 1.00 is inherited from the Opus entry on the reasoning " +
			"that both ship a 1M window behind the same client, which is weak. The entry most worth " +
			"measuring, and a run costs a full 1M-window session to reach the client's own point.",
	}},
}

// defaultClientCeiling is what an unlisted model gets: case 3 and case 4 both, because this package
// cannot yet distinguish "nothing compacts here, so the window IS the point" from "we have never
// seen this client compact".
//
// 1.00 rather than something lower, and the choice needs stating because it is NOT conservative for
// the span -- a C that is too high makes the span too wide and OVER-reports. It is 1.00 anyway
// because a fabricated lower bound would make every unlisted model's savings quietly smaller by an
// amount nobody chose, and an operator comparing two models would be reading a difference this table
// invented. 1.00 is transparently an absence of information rather than a guess pretending to be
// one, and `provenance: default` on the panel says so.
const defaultClientCeiling = 1.00

// clientCeilingFor resolves a model's compaction point from the fallback table. A deployment with
// observations of its own should not be reading this -- see #239.
func clientCeilingFor(model string) clientCeiling {
	id := modelinfo.NormalizeID(model)
	best := clientCeiling{Frac: defaultClientCeiling, Prov: ceilingDefault,
		Note: "no entry and no observation for this model, so the span runs to the whole window. " +
			"That is correct where nothing compacts (a raw API client), a guess where the client " +
			"compacts and we have not seen it, and this package cannot yet tell those apart."}
	longest := 0
	for _, e := range clientCeilings {
		if len(e.Pattern) > longest && strings.Contains(id, e.Pattern) {
			best, longest = e.Ceiling, len(e.Pattern)
		}
	}
	return best
}
