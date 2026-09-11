package offload

import (
	"context"
	"os"
	"strings"
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

// THE CHECKPOINT-REUSE PATH RECORDED NOTHING, and that is why nobody could tell whether the mechanism
// protecting the cached prefix was working.
//
// Reuse re-emits a prior summary byte-identically: no model call, prefix unchanged. Rolling the
// checkpoint forward instead costs a model call AND a prefix rewrite from the summary onward, i.e. a
// cache write, which this codebase prices at ~11.5x a cache read per token. Those are entirely different
// cost profiles and `/stats` reported them identically, because the reuse branch never set an event and
// never incremented Report.Replays — so `acted_replay` read 0 whether reuse fired on every eligible turn
// or on none. A zero meant UNMEASURED, not NEVER, on the component doing the most work on this workload.
//
// Asserted as a pair on purpose: the second turn must reuse AND say so, and the first turn (which has no
// checkpoint to reuse) must NOT claim a reuse. A counter that fires unconditionally satisfies neither.
func newSummarizeReusing(t *testing.T, resummarizeTokens int) *Summarize {
	t.Helper()
	c, err := newSummarize([]byte(
		"keep_last: 2\nmin_tokens: 10\nresummarize_tokens: " + itoa(resummarizeTokens) + "\n" +
			"trigger:\n  min_messages: 2\n  min_request_tokens: 10\n"))
	if err != nil {
		t.Fatalf("newSummarize: %v", err)
	}
	s := c.(*Summarize)
	s.modelClient = &fixedModel{out: "SUMMARY: explored the handler, 3 tests fail."}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestSummarizeCheckpointReuseIsCounted(t *testing.T) {
	base := []bschemas.ChatMessage{
		sysMsg("you are a coding agent"),
		userMsg("Fix the failing handler in src/mod/file.go and run the tests."),
		callMsg("t_a"),
		bulkResult("t_a"),
		assistantMsg("Next I will run the tests."),
		userMsg("keep going"),
	}
	st := store.NewMemory(store.Options{})
	newCtx := func() *components.Ctx {
		return &components.Ctx{Ctx: context.Background(), Session: "reuse-sess",
			Store: st, CtxWindow: 1_000_000}
	}
	// A generous refresh interval, so the second turn's tail cannot push it over.
	s := newSummarizeReusing(t, 1_000_000)

	// TURN ONE: no checkpoint exists, so this is the fresh path.
	req1 := &bschemas.BifrostChatRequest{Input: schema.CloneMessages(base)}
	var rep1 components.Report
	if _, err := s.Offload(req1, &rep1, newCtx()); err != nil {
		t.Fatal(err)
	}
	if rep1.Replays != 0 || rep1.Events["summary_checkpoint_reused"] != 0 {
		t.Errorf("turn one claimed a reuse with no checkpoint to reuse (replays=%d events=%v)",
			rep1.Replays, rep1.Events)
	}
	if rep1.Gates["summary_no_checkpoint"] == 0 {
		t.Errorf("turn one did not record WHY it took the fresh path (gates: %v)", rep1.Gates)
	}
	if rep1.Skipped {
		t.Skip("turn one did not summarize on this fixture, so there is no checkpoint for turn two")
	}

	// TURN TWO: same session and store, same messages. The checkpoint must be reused, free.
	req2 := &bschemas.BifrostChatRequest{Input: schema.CloneMessages(base)}
	var rep2 components.Report
	if _, err := s.Offload(req2, &rep2, newCtx()); err != nil {
		t.Fatal(err)
	}
	if rep2.Events["summary_checkpoint_reused"] == 0 {
		t.Fatalf("turn two did not record a checkpoint reuse (gates: %v events: %v)",
			rep2.Gates, rep2.Events)
	}
	if rep2.Replays == 0 {
		t.Errorf("the reuse was recorded as an Event but not in Report.Replays, so /stats still " +
			"files this free turn under acted_fresh — the exact defect being fixed")
	}
	if len(rep2.Calls) != 0 {
		t.Errorf("a reused checkpoint made %d model call(s); reuse exists to avoid exactly that",
			len(rep2.Calls))
	}
}

// And the decline that an UPSTREAM component can cause gets its own name, because it is the only one
// that is somebody else's fault: the hash is over the messages as the rest of the pipeline left them,
// and summarize runs last.
func TestSummarizeRecordsWhenTheCoveredSpanChangedUnderIt(t *testing.T) {
	base := []bschemas.ChatMessage{
		sysMsg("you are a coding agent"),
		userMsg("Fix the failing handler in src/mod/file.go and run the tests."),
		callMsg("t_a"),
		bulkResult("t_a"),
		assistantMsg("Next I will run the tests."),
		userMsg("keep going"),
	}
	st := store.NewMemory(store.Options{})
	newCtx := func() *components.Ctx {
		return &components.Ctx{Ctx: context.Background(), Session: "changed-sess",
			Store: st, CtxWindow: 1_000_000}
	}
	s := newSummarizeReusing(t, 1_000_000)

	req1 := &bschemas.BifrostChatRequest{Input: schema.CloneMessages(base)}
	var rep1 components.Report
	if _, err := s.Offload(req1, &rep1, newCtx()); err != nil {
		t.Fatal(err)
	}
	if rep1.Skipped {
		t.Skip("no checkpoint was created on this fixture")
	}

	// Turn two, but with a message inside the covered span altered — what an earlier component in the
	// pipeline does when it removes or rewrites deep history.
	mutated := schema.CloneMessages(base)
	schema.SetMessageText(&mutated[3], "<<cg:deadbeef>> [tool output removed by an earlier component]")
	req2 := &bschemas.BifrostChatRequest{Input: mutated}
	var rep2 components.Report
	if _, err := s.Offload(req2, &rep2, newCtx()); err != nil {
		t.Fatal(err)
	}
	if rep2.Gates["summary_covered_span_changed"] == 0 {
		t.Errorf("an upstream mutation forced the paid path and nothing recorded it (gates: %v)",
			rep2.Gates)
	}
	if rep2.Events["summary_checkpoint_reused"] != 0 {
		t.Errorf("claimed a reuse on a span that had changed: %v", rep2.Events)
	}
}

// EVERY DECLINE ON THE FRESH PATH IS NAMED. Seven `rep.Skipped = true` returns in Offload used to carry no
// gate between them, and the cost of that was measured rather than imagined: on the iteration 027 probe a
// session sat at 81,580 tokens — above its own 0.90 trigger — for six consecutive turns while summarize
// acted on none of them, and the recorded gates named a span trim and a missing checkpoint, neither of
// which was the reason. "summarize is inert" and "summarize was never asked" read identically.
//
// Asserted as a SET rather than one test per path, because the property that matters is that no route to
// Skipped is anonymous — a new unlabelled early return is the regression, and enumerating the labels one
// at a time would not catch one being added.
func TestEveryFreshPathDeclineIsLabelled(t *testing.T) {
	src, err := os.ReadFile("summarize.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	// The window of source that is Offload's fresh path: from its signature to the emitCheckpoint
	// helpers below it. refuse() has its own gate (stash_reserve_exhausted) covering its returns.
	start, end := -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, "func (s *Summarize) Offload(") {
			start = i
		}
		if start >= 0 && strings.HasPrefix(l, "func ") && i > start {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("could not locate Offload in summarize.go")
	}
	var anonymous []int
	for i := start; i < end; i++ {
		if !strings.Contains(lines[i], "rep.Skipped = true") {
			continue
		}
		// A gate must be raised within the same block — look back a few lines for rep.Gate.
		labelled := false
		for j := i; j >= i-6 && j > start; j-- {
			if strings.Contains(lines[j], "rep.Gate(") {
				labelled = true
				break
			}
		}
		if !labelled {
			anonymous = append(anonymous, i+1)
		}
	}
	if len(anonymous) > 0 {
		t.Fatalf("summarize.go: %d decline path(s) in Offload set rep.Skipped with no rep.Gate above them, "+
			"at line(s) %v. An unlabelled decline is why a session above the trigger could go six turns "+
			"without acting and leave nothing in the counters to say which check refused.",
			len(anonymous), anonymous)
	}
}
