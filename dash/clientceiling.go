package dash

import (
	"strings"

	"github.com/rossoctl/context-guru/internal/modelinfo"
)

// Where the CLIENT compacts its own transcript, per model, as a fraction of the context window
// measured in PROVIDER-BILLED input.
//
// # Why this is not in internal/modelinfo
//
// It looks like a per-model fact and it is not one. It is a fact about a (client, model) PAIR: the
// same model behind a different agent, or behind Claude Code with a configured auto-compact
// threshold, compacts somewhere else. modelinfo owns properties of the model itself — the window,
// the rates — and putting a client's behaviour in there would invite reading it as published.
//
// # Why the panel needs it at all
//
// The compaction-episode span is an ATTRIBUTION boundary: `ceiling - fill`. We fire at the fill
// threshold, and in the world where we did not compact the conversation keeps growing until the
// CLIENT compacts. Past that point both worlds are running on a summarized transcript and nothing
// further is attributable to us. So the far end of every episode's span is this number, and getting
// it wrong moves a published figure — too high over-reports, which is the direction that matters.
//
// # THE RULER, which is the part that is easy to get wrong
//
// These fractions are of PROVIDER-BILLED input, because that is what the fill gate compares and what
// a context window is stated in. They are NOT the figure the client's own context indicator shows.
// The same moment on the haiku run below reads three ways:
//
//	provider-billed input        199,184   = 0.996   <-- what belongs in this table
//	Claude Code's own count      ~167,000  = 0.835   <-- what the client displays
//	our message-text count       147,493   = 0.737   <-- tokens_before
//
// A 0.835 here would be the same units error Trigger.Fires shipped with, one level up. The client
// thresholding at ~167,000 of ITS count and the provider billing 199,184 at that same turn are the
// same event; the conversion between them is empirical, and the measured one is what goes in.
//
// # Provenance is carried, because two of the three entries are not measurements
//
// The same discipline modelinfo.Exact keeps for windows, and for the same reason: a guess and a
// measurement must not be summed or reported alike. #239 is the issue for learning these per
// deployment instead of shipping a table.
type ceilingProvenance string

const (
	// ceilingMeasured: observed on a real client run, in billed tokens, by watching the client
	// actually compact. scripts/scenarios/a-firing-rate.sh reports it.
	ceilingMeasured ceilingProvenance = "measured"
	// ceilingAssumed: not observed. A reported behaviour or a reasoned bound, used because the
	// alternative is publishing nothing, and named so nobody mistakes it for the first kind.
	ceilingAssumed ceilingProvenance = "assumed"
	// ceilingDefault: no entry for this model at all, so the conservative fallback applies.
	ceilingDefault ceilingProvenance = "default"
)

// clientCeiling is one entry: the fraction, where it came from, and enough of the why that a
// maintainer can tell whether it still holds.
type clientCeiling struct {
	Frac float64
	Prov ceilingProvenance
	Note string
}

// clientCeilings is matched by SUBSTRING against the normalized model id, longest pattern first, so
// `claude-haiku-4-5` wins over a bare `claude` and a provider prefix or a `[1m]` variant suffix does
// not defeat the match (modelinfo.NormalizeID strips both).
//
// ORDER MATTERS and the lookup sorts by pattern length rather than relying on declaration order,
// because a table whose correctness depends on nobody appending in the wrong place is a table that
// will be appended to in the wrong place.
var clientCeilings = []struct {
	Pattern string
	Ceiling clientCeiling
}{
	{"claude-haiku-4-5", clientCeiling{
		Frac: 0.996,
		Prov: ceilingMeasured,
		Note: "measured on a real Claude Code session: the last turn before the client compacted " +
			"billed 199,184 of a 200,000 window. n=1, one client version. The client's own indicator " +
			"read ~167,000 on the same turn, which is the same event in the client's ruler rather " +
			"than a second measurement.",
	}},
	{"claude-opus-5", clientCeiling{
		Frac: 1.00,
		Prov: ceilingAssumed,
		Note: "the repo owner's observation that Claude Code on Opus runs to essentially 100% of the " +
			"window before compacting. NOT measured in billed tokens by a scenario run, so it is an " +
			"observation rather than a figure this panel derived. 1.00 is also the LEAST conservative " +
			"value it could take, which is the direction that over-reports — so a measurement here " +
			"can only move published savings down.",
	}},
	{"claude-sonnet-5", clientCeiling{
		Frac: 1.00,
		Prov: ceilingAssumed,
		Note: "NO observation of any kind, and 1.00 is inherited from the Opus entry on the reasoning " +
			"that both ship a 1M window and the same client. That reasoning is weak and the value is " +
			"the over-reporting direction. This is the entry most worth measuring, and a run costs " +
			"a full 1M-window session to reach the client's ceiling.",
	}},
}

// defaultClientCeiling is what an unlisted model gets.
//
// 1.00 rather than something lower, and the choice deserves stating because it is NOT the
// conservative direction. A ceiling that is too HIGH makes the span too WIDE, which credits turns
// past the point the client would have compacted and OVER-reports.
//
// It is 1.00 anyway because the alternative is worse in a different way: a fabricated lower bound
// would make every unlisted model's savings quietly smaller by an amount nobody chose, and an
// operator comparing two models would be reading a difference this table invented. 1.00 is the one
// value that is transparently an absence of information rather than a guess pretending to be one,
// and `ceiling_provenance: default` on the panel says so. Narrowing it is #239's job, with data.
const defaultClientCeiling = 1.00

// clientCeilingFor resolves a model's client compaction ceiling.
func clientCeilingFor(model string) clientCeiling {
	id := modelinfo.NormalizeID(model)
	best := clientCeiling{Frac: defaultClientCeiling, Prov: ceilingDefault,
		Note: "no entry for this model; the span runs to the whole window, which is the " +
			"over-reporting direction and is reported as such rather than narrowed by a guess"}
	longest := 0
	for _, e := range clientCeilings {
		if len(e.Pattern) > longest && strings.Contains(id, e.Pattern) {
			best, longest = e.Ceiling, len(e.Pattern)
		}
	}
	return best
}
