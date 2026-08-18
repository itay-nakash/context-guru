package offload

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

// --- fixtures ---------------------------------------------------------------

// scriptedModel records every prompt it is handed and answers from a closure, so a
// test can assert on what the reflection module SAW as well as what it did with the
// reply. Recording the prompt is the point: the sliding window is the part of
// AgentDiet that no other component here implements, and it is invisible in the
// output — a component that sent only the target step would still reduce it.
type scriptedModel struct {
	mu      sync.Mutex
	prompts []string
	reply   func(prompt string) (string, error)
}

func (m *scriptedModel) Complete(_ context.Context, p string) (string, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, p)
	m.mu.Unlock()
	if m.reply == nil {
		return "", nil
	}
	return m.reply(p)
}

func (m *scriptedModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prompts)
}

func (m *scriptedModel) lastPrompt(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.prompts) == 0 {
		t.Fatal("model was never called, so this assertion proves nothing — check the " +
			"fixture clears min_step_tokens and that the target step index exists")
	}
	return m.prompts[len(m.prompts)-1]
}

// asstMsg is an assistant message that issued one tool call, i.e. the opening half
// of a step. The tool call is carried in the OpenAI-shaped ToolCalls field, which is
// what serializeStep renders as <call tool="...">.
func asstMsg(text, tool, args string) bschemas.ChatMessage {
	txt, name := text, tool
	return bschemas.ChatMessage{
		Role:    bschemas.ChatMessageRoleAssistant,
		Content: &bschemas.ChatMessageContent{ContentStr: &txt},
		ChatAssistantMessage: &bschemas.ChatAssistantMessage{
			ToolCalls: []bschemas.ChatAssistantMessageToolCall{{
				Function: bschemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: args},
			}},
		},
	}
}

// bigOutput is a tool output comfortably above the default 500-token step floor.
func bigOutput(tag string) string {
	return strings.Repeat("tests/test_"+tag+".py::test_case PASSED [ 42%]\n", 80)
}

// traj builds `steps` complete agent steps behind one user turn. Every step gets a
// large tool output so the θ gate cannot be the reason a test sees no reduction.
func traj(steps int) *bschemas.BifrostChatRequest {
	req := &bschemas.BifrostChatRequest{
		Input: []bschemas.ChatMessage{userMsg("Fix the failing handler in src/mod/file.py and run the tests.")},
	}
	for i := 0; i < steps; i++ {
		req.Input = append(req.Input,
			asstMsg("Checking step "+string(rune('A'+i)), "bash",
				`{"command":"python -m pytest tests/test_`+string(rune('a'+i))+`.py -v"}`),
			toolResultMsg(bigOutput(string(rune('a'+i)))))
	}
	return req
}

func newAgentDietTest(t *testing.T, yaml string, model components.Model) *AgentDiet {
	t.Helper()
	c, err := newAgentDiet([]byte(yaml))
	if err != nil {
		t.Fatalf("newAgentDiet: %v", err)
	}
	d, ok := c.(*AgentDiet)
	if !ok {
		t.Fatalf("newAgentDiet returned %T, want *AgentDiet", c)
	}
	d.modelClient = model
	return d
}

func dietCtx(session string, model components.Model) *components.Ctx {
	return &components.Ctx{
		Session: session,
		Store:   store.NewMemory(store.Options{}),
		Ctx:     context.Background(),
		Model:   components.ModelSpec{Static: model, Incoming: model},
	}
}

// reduceAll is a cooperating reflection model: it returns one short takeaway for
// result 0 of the target step, the shape the prompt asks for. The fixtures give each
// step a single tool result, so one payload is all that is needed.
func reduceAll(string) (string, error) {
	return `<step id="0">` + "\n" +
		`<result id="0">... (individual test lines omitted; mostly PASSED)</result>` + "\n" +
		"</step>", nil
}

// --- step splitting --------------------------------------------------------

func TestSplitStepsGroupsAssistantWithItsToolResults(t *testing.T) {
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		userMsg("task"),                    // 0
		asstMsg("a", "bash", `{}`),         // 1
		toolResultMsg("out1"),              // 2
		asstMsg("b", "bash", `{}`),         // 3
		toolResultMsg("out2a"),             // 4  parallel calls…
		toolResultMsg("out2b"),             // 5  …same step
		asstMsg("in flight", "bash", `{}`), // 6  no result yet
	}}
	steps := splitSteps(req)
	if len(steps) != 2 {
		t.Fatalf("splitSteps = %d steps, want 2 (the trailing assistant is the in-flight "+
			"turn and must not count, or the age window slides on alternating requests)", len(steps))
	}
	if steps[0].assistant != 1 || len(steps[0].tools) != 1 || steps[0].tools[0] != 2 {
		t.Errorf("step 0 = %+v, want assistant 1 with tool [2]", steps[0])
	}
	if steps[1].assistant != 3 || len(steps[1].tools) != 2 {
		t.Errorf("step 1 = %+v, want assistant 3 with 2 parallel tool results", steps[1])
	}
}

func TestSplitStepsClosesOnUserTurn(t *testing.T) {
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		asstMsg("a", "bash", `{}`),
		toolResultMsg("out1"),
		userMsg("actually, do this instead"),
		asstMsg("b", "bash", `{}`),
		toolResultMsg("out2"),
	}}
	if steps := splitSteps(req); len(steps) != 2 {
		t.Fatalf("splitSteps = %d, want 2 steps separated by the user turn", len(steps))
	}
}

// --- the age window (delay_steps) ------------------------------------------

// The defining behaviour: the target is chosen by AGE, and the most recent
// delay_steps steps are untouchable. This is AgentDiet's safety property — a bad
// reduction can never land on what the agent is working on right now.
func TestAgentDietReducesOnlyTheStepAtConfiguredAge(t *testing.T) {
	model := &scriptedModel{reply: reduceAll}
	d := newAgentDietTest(t, "min_saved_tokens: 1\n", model) // a=2, b=1 by default
	req := traj(4)                                           // steps 0..3 ⇒ target = 3-2 = 1
	before := make([]string, len(req.Input))
	for i := range req.Input {
		before[i] = mustText(req.Input[i])
	}

	rep := &components.Report{}
	if _, err := d.Offload(req, rep, dietCtx("age", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if model.calls() != 1 {
		t.Fatalf("model called %d times, want exactly 1 — AgentDiet performs ONE reflection "+
			"per turn, not one per output", model.calls())
	}

	// Tool results live at input indices 2,4,6,8 for steps 0,1,2,3.
	target := 4 // step 1
	if mustText(req.Input[target]) == before[target] {
		t.Errorf("step 1 (age 2) was NOT reduced; that is the only eligible step")
	}
	for _, i := range []int{2, 6, 8} {
		if mustText(req.Input[i]) != before[i] {
			t.Errorf("input[%d] changed, but only the step at age delay_steps=2 may be "+
				"touched (indices 6 and 8 are the protected recent steps)", i)
		}
	}
}

func TestAgentDietSkipsUntilTrajectoryIsDeepEnough(t *testing.T) {
	model := &scriptedModel{reply: reduceAll}
	d := newAgentDietTest(t, "", model)
	req := traj(2) // target = 1-2 < 0
	rep := &components.Report{}
	if _, err := d.Offload(req, rep, dietCtx("shallow", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if model.calls() != 0 {
		t.Errorf("model called on a %d-step trajectory; nothing has aged past the delay yet", 2)
	}
	if !rep.Skipped {
		t.Error("rep.Skipped = false; a component that did nothing must say so")
	}
}

// --- the sliding window (context_steps) ------------------------------------

// The window is what lets the model call content *redundant* or *expired* rather
// than merely verbose, and it is invisible in the output — so assert on the prompt.
func TestAgentDietPromptCarriesTheSlidingWindow(t *testing.T) {
	model := &scriptedModel{reply: reduceAll}
	d := newAgentDietTest(t, "min_saved_tokens: 1\n", model)
	req := traj(4) // target 1, so the window is steps [0 … 3]
	if _, err := d.Offload(req, &components.Report{}, dietCtx("win", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	p := model.lastPrompt(t)
	for id := 0; id <= 3; id++ {
		if !strings.Contains(p, `<step id="`+strconv.Itoa(id)+`">`) {
			t.Errorf("prompt is missing step %d; want b=1 before through a=2 after the target", id)
		}
	}
	if !strings.Contains(p, "Now compress the step with id 1.") {
		t.Error("prompt does not name the target step, so the model cannot know which to reduce")
	}
	if !strings.Contains(p, `<call tool="bash">`) {
		t.Error("prompt has no <call> tag: the tool call beside its result is the redundancy signal")
	}
}

func TestAgentDietContextStepsZeroStartsAtTheTarget(t *testing.T) {
	model := &scriptedModel{reply: reduceAll}
	d := newAgentDietTest(t, "context_steps: 0\nmin_saved_tokens: 1\n", model)
	req := traj(4)
	if _, err := d.Offload(req, &components.Report{}, dietCtx("b0", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if p := model.lastPrompt(t); strings.Contains(p, `<step id="0">`) {
		t.Error("context_steps: 0 must not include the step before the target")
	}
}

// --- the two thresholds ----------------------------------------------------

func TestAgentDietSkipsStepBelowMinStepTokens(t *testing.T) {
	model := &scriptedModel{reply: reduceAll}
	d := newAgentDietTest(t, "min_step_tokens: 100000\n", model)
	if _, err := d.Offload(traj(4), &components.Report{}, dietCtx("theta", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if model.calls() != 0 {
		t.Error("model called on a step below min_step_tokens; θ exists so a short step " +
			"never pays for a call")
	}
}

// The apply-gate: a reduction that comes back but saves almost nothing must be
// DISCARDED, not applied. Applying it would pay a cache-write of the whole suffix
// to remove a handful of tokens — the trade the gate exists to refuse.
func TestAgentDietDiscardsMarginalReduction(t *testing.T) {
	req := traj(4)
	orig := mustText(req.Input[4])
	// Give back nearly the whole output: a few characters shorter, so it shrinks but
	// clears neither min_saved_tokens nor max_keep_ratio.
	model := &scriptedModel{reply: func(string) (string, error) {
		return `<step id="1"><result id="0">` + orig[:len(orig)-20] + `</result></step>`, nil
	}}
	d := newAgentDietTest(t, "min_saved_tokens: 400\nmax_keep_ratio: 0.8\n", model)

	rep := &components.Report{}
	if _, err := d.Offload(req, rep, dietCtx("gate", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if model.calls() != 1 {
		t.Fatalf("model calls = %d, want 1 (the gate is post-call by design)", model.calls())
	}
	if mustText(req.Input[4]) != orig {
		t.Error("a marginal reduction was applied; min_saved_tokens/max_keep_ratio must " +
			"reject it so the suffix cache-write is not paid for nothing")
	}
	if !rep.Skipped {
		t.Error("rep.Skipped = false after discarding the only candidate")
	}
}

// --- reversibility ---------------------------------------------------------

func TestAgentDietLeavesAReversibleMarker(t *testing.T) {
	model := &scriptedModel{reply: reduceAll}
	d := newAgentDietTest(t, "min_saved_tokens: 1\n", model)
	req := traj(4)
	orig := mustText(req.Input[4])

	c := dietCtx("rev", model)
	keys, err := d.Offload(req, &components.Report{}, c)
	if err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("returned %d cache keys, want 1 — an Offload that drops bytes without "+
			"stashing is reverted by the pipeline", len(keys))
	}
	got := mustText(req.Input[4])
	if !expand.HasPlaceholder(got) {
		t.Errorf("reduced message carries no <<cg:…>> marker: %q", got)
	}
	stashed, ok := c.Store.Get(keys[0])
	if !ok {
		t.Fatal("original was not stashed under the returned key; expand could not restore it")
	}
	if string(stashed) != orig {
		t.Error("stashed original does not match the message content that was replaced")
	}
}

// --- freeze + replay -------------------------------------------------------

// The agent re-sends its full original trajectory every turn, so a reduction has to
// be replayed byte-identically or the output flips reduced→full→reduced and churns
// the provider's KV cache on every turn. This is also how reductions ACCUMULATE
// across a session, which is what the paper gets for free by editing the agent's own
// trajectory in place.
func TestAgentDietReplaysFrozenReductionWithoutCallingTheModel(t *testing.T) {
	model := &scriptedModel{reply: reduceAll}
	d := newAgentDietTest(t, "min_saved_tokens: 1\n", model)
	c := dietCtx("freeze", model)

	first := traj(4)
	if _, err := d.Offload(first, &components.Report{}, c); err != nil {
		t.Fatalf("first Offload: %v", err)
	}
	reduced := mustText(first.Input[4])
	if model.calls() != 1 {
		t.Fatalf("first turn made %d calls, want 1", model.calls())
	}

	// Next turn: the same trajectory plus one more step. The step reduced above is
	// now older, so it is no longer the target — it must still come out reduced.
	second := traj(5)
	if _, err := d.Offload(second, &components.Report{}, c); err != nil {
		t.Fatalf("second Offload: %v", err)
	}
	if got := mustText(second.Input[4]); got != reduced {
		t.Errorf("frozen reduction not replayed byte-identically:\n got %q\nwant %q", got, reduced)
	}
	if model.calls() != 2 {
		t.Errorf("model calls = %d, want 2 — the aged step must replay from the freeze "+
			"(no call) while exactly one NEW step is reduced", model.calls())
	}
}

// An agent retry re-sends a trajectory of the SAME depth, so the same step is targeted
// on two consecutive requests. The second pass must replay the freeze and stop there.
//
// Two details make this a real hazard rather than a theoretical one, and the fixture
// has to reproduce both. After a replay the message holds the REDUCED text, so (a) a
// frozen lookup keyed on its content hash MISSES, and (b) under marker_mode: off there
// is no placeholder for skipReduce to notice. What normally saves it anyway is the θ
// gate — a reduced step is usually short enough to fall below min_step_tokens. So the
// case that actually bites is a big output whose reduction is STILL above θ, which is
// what this builds: without the replay bookkeeping the step is reduced twice, the second
// time from already-reduced bytes.
func TestAgentDietRetryAtSameDepthDoesNotDoubleReduce(t *testing.T) {
	// ~2,250 tokens original; the reply keeps ~1,100 — a real cut that still clears θ.
	huge := strings.Repeat("tests/test_collection.py::test_case PASSED [ 42%]\n", 200)
	stillBig := strings.Repeat("tests/test_collection.py::test_case PASSED [ 42%]\n", 100)
	model := &scriptedModel{reply: func(string) (string, error) {
		return `<step id="1"><result id="0">` + stillBig + `</result></step>`, nil
	}}
	d := newAgentDietTest(t, "min_saved_tokens: 1\nmarker_mode: off\n", model)
	c := dietCtx("retry", model)

	build := func() *bschemas.BifrostChatRequest {
		req := traj(4)
		for _, i := range []int{2, 4, 6, 8} {
			m := req.Input[i]
			h := huge
			m.Content = &bschemas.ChatMessageContent{ContentStr: &h}
			req.Input[i] = m
		}
		return req
	}

	first := build()
	if _, err := d.Offload(first, &components.Report{}, c); err != nil {
		t.Fatalf("first Offload: %v", err)
	}
	reduced := mustText(first.Input[4])
	if model.calls() != 1 {
		t.Fatalf("first turn made %d calls, want 1", model.calls())
	}
	if reduced == huge {
		t.Fatal("the fixture did not reduce anything, so the retry assertion proves nothing")
	}
	// The reduction must still clear θ, or the θ gate — not the guard under test —
	// is what stops the second pass.
	if schema.TextTokens(reduced) <= d.minStep {
		t.Fatalf("reduced step is %d tokens, at or below θ=%d: this fixture cannot exercise "+
			"the double-reduce path", schema.TextTokens(reduced), d.minStep)
	}

	second := build() // same depth: the retry
	if _, err := d.Offload(second, &components.Report{}, c); err != nil {
		t.Fatalf("retry Offload: %v", err)
	}
	if got := mustText(second.Input[4]); got != reduced {
		t.Errorf("retry produced different bytes for the same step:\n got %d tokens\nwant %d",
			schema.TextTokens(got), schema.TextTokens(reduced))
	}
	if model.calls() != 1 {
		t.Errorf("model calls = %d, want 1 — the retry must replay the freeze, not pay for "+
			"a second reduction of already-reduced text", model.calls())
	}
}

// --- fail-open (fault injection) -------------------------------------------

// CONTRIBUTING requires a fault-injection path per component. Compaction must never
// break the agent's request, but the abandoned call must be VISIBLE: an arm that
// silently stopped reducing under load otherwise reads as an arm that got faster.
func TestAgentDietTimeoutIsCountedAndLeavesInputIntact(t *testing.T) {
	timeoutsBefore, errorsBefore := AgentDietTimeouts(), AgentDietErrors()

	t.Setenv("CONTEXT_GURU_AGENTDIET_TIMEOUT", "150ms")
	prev := agentDietTimeout
	agentDietTimeout = resolveTimeoutEnv("CONTEXT_GURU_AGENTDIET_TIMEOUT", defaultAgentDietTimeout)
	defer func() { agentDietTimeout = prev }()
	if agentDietTimeout != 150*time.Millisecond {
		t.Fatalf("timeout override not applied: got %v", agentDietTimeout)
	}

	model := &slowModel{}
	d := newAgentDietTest(t, "", model)
	req := traj(4)
	orig := mustText(req.Input[4])

	rep := &components.Report{}
	keys, err := d.Offload(req, rep, dietCtx("timeout", model))
	if err != nil {
		t.Fatalf("Offload returned %v; a blown deadline must fail OPEN, not error", err)
	}
	if atomic.LoadInt64(&model.calls) == 0 {
		t.Fatal("model was never called, so the timeout path was not exercised")
	}
	if len(keys) != 0 {
		t.Errorf("returned %d keys on the timeout path, want 0 (nothing was stashed)", len(keys))
	}
	if mustText(req.Input[4]) != orig {
		t.Error("input mutated on the timeout path; the original request must stay forwardable")
	}
	if !rep.Skipped {
		t.Error("rep.Skipped = false after a timeout with nothing applied")
	}
	if got := AgentDietTimeouts() - timeoutsBefore; got != 1 {
		t.Errorf("agentdiet_timeouts += %d, want 1 — a silent fail-open is how an arm "+
			"that stopped reducing gets mistaken for one that got faster", got)
	}
	if got := AgentDietErrors() - errorsBefore; got != 0 {
		t.Errorf("agentdiet_errors += %d, want 0 — a deadline is not a transport error, "+
			"and conflating them hides which knob to reach for", got)
	}
}

func TestAgentDietModelErrorFailsOpenAsAnError(t *testing.T) {
	errorsBefore := AgentDietErrors()
	model := &scriptedModel{reply: func(string) (string, error) {
		return "", errors.New("502 from the cheap-model route")
	}}
	d := newAgentDietTest(t, "", model)
	req := traj(4)
	orig := mustText(req.Input[4])

	rep := &components.Report{}
	if _, err := d.Offload(req, rep, dietCtx("err", model)); err != nil {
		t.Fatalf("Offload returned %v; a model error must fail open", err)
	}
	if mustText(req.Input[4]) != orig {
		t.Error("input mutated after a model error")
	}
	if got := AgentDietErrors() - errorsBefore; got != 1 {
		t.Errorf("agentdiet_errors += %d, want 1", got)
	}
}

func TestAgentDietWithNoModelIsAQuietNoOp(t *testing.T) {
	d := newAgentDietTest(t, "", nil)
	req := traj(4)
	orig := mustText(req.Input[4])
	rep := &components.Report{}
	c := &components.Ctx{
		Session: "nomodel", Store: store.NewMemory(store.Options{}), Ctx: context.Background(),
	}
	if _, err := d.Offload(req, rep, c); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if mustText(req.Input[4]) != orig {
		t.Error("content changed with no model available")
	}
	if !rep.Skipped {
		t.Error("rep.Skipped = false with no model configured")
	}
}

// A garbage reply must not be spliced. The model has no stop sequence here, so an
// unparseable answer is an expected event, not an exceptional one.
func TestAgentDietIgnoresUnparseableReply(t *testing.T) {
	model := &scriptedModel{reply: func(string) (string, error) {
		return "Sure! I have compressed the step for you.", nil
	}}
	d := newAgentDietTest(t, "", model)
	req := traj(4)
	orig := mustText(req.Input[4])
	rep := &components.Report{}
	if _, err := d.Offload(req, rep, dietCtx("garbage", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if mustText(req.Input[4]) != orig {
		t.Error("a reply carrying no <result> block was spliced into the request")
	}
	if !rep.Skipped {
		t.Error("rep.Skipped = false after an unparseable reply")
	}
}

// A reply that omits `id=` on a step with PARALLEL tool calls must reduce nothing. This
// is the one wrong answer worse than no answer: an unlabelled block read as id 0 puts
// one tool's compressed output into another tool's message, the never-worse check waves
// it through because it is smaller, and freeze then replays the mismatch all session.
// Declining is correct — the step stays verbatim and the next turn can try again.
func TestAgentDietDropsUnlabelledResultOnAParallelStep(t *testing.T) {
	model := &scriptedModel{reply: func(string) (string, error) {
		// Well-formed, cooperative, and unplaceable: the model kept ONE result and
		// dropped the id, so nothing says which of the two calls this answers.
		return `<step id="1">` + "\n" +
			`<result>... (listing trimmed; 3 candidate files)</result>` + "\n</step>", nil
	}}
	d := newAgentDietTest(t, "min_saved_tokens: 1\n", model)

	// Step 1 answers two parallel calls, and at a=2 with 4 steps it is the target.
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
		userMsg("Find and fix the failing handler."),
		asstMsg("step A", "bash", `{"command":"pytest -v"}`),
		toolResultMsg(bigOutput("a")),
		asstMsg("step B, two calls at once", "bash", `{"command":"ls -R src"}`),
		toolResultMsg(bigOutput("b0")), // step 1, result 0
		toolResultMsg(bigOutput("b1")), // step 1, result 1
		asstMsg("step C", "bash", `{"command":"pytest -v"}`),
		toolResultMsg(bigOutput("c")),
		asstMsg("step D", "bash", `{"command":"pytest -v"}`),
		toolResultMsg(bigOutput("d")),
	}}
	if steps := splitSteps(req); len(steps) != 4 || len(steps[1].tools) != 2 {
		t.Fatalf("fixture is wrong: %d steps, step 1 has %d results — the test proves "+
			"nothing unless the TARGET step is the one with parallel calls",
			len(steps), len(splitSteps(req)[1].tools))
	}
	before := []string{mustText(req.Input[4]), mustText(req.Input[5])}

	rep := &components.Report{}
	if _, err := d.Offload(req, rep, dietCtx("parallel", model)); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if model.calls() != 1 {
		t.Fatalf("model called %d times, want 1 — the reply must be REJECTED after the "+
			"call, not declined before it, or this asserts nothing", model.calls())
	}
	for k, i := range []int{4, 5} {
		if mustText(req.Input[i]) != before[k] {
			t.Errorf("result %d was rewritten from an unlabelled block; one tool's output "+
				"can now be sitting in the other tool's message", k)
		}
	}
	if !rep.Skipped {
		t.Error("rep.Skipped = false although no reduction was applied")
	}
}

// --- reply parsing ---------------------------------------------------------

func TestParseReducedResults(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		n        int // results the target step has, i.e. the size of the id space
		want     map[int]string
	}{
		{"plain", `<step id="3"><result id="0">short</result></step>`, 1, map[int]string{0: "short"}},
		{
			"prose and fences around it",
			"Sure, here it is:\n```xml\n<step id=\"3\">\n<result id=\"0\">short</result>\n</step>\n```",
			1, map[int]string{0: "short"},
		},
		{
			"parallel results keep their ids",
			`<step id="3"><result id="0">a</result><result id="1">b</result></step>`,
			2, map[int]string{0: "a", 1: "b"},
		},
		{
			"missing trailing step close",
			`<step id="3"><result id="0">a</result>`,
			1, map[int]string{0: "a"},
		},
		{
			"unterminated result keeps what arrived",
			`<step id="3"><result id="0">truncated mid`,
			1, map[int]string{0: "truncated mid"},
		},
		{
			"no id is read as 0 in a single-result step",
			`<step id="3"><result>a</result></step>`,
			1, map[int]string{0: "a"},
		},
		// The next four are the misattribution guard. In a step with parallel tool calls
		// an unlabelled block cannot be placed, and placing it at 0 anyway would put one
		// tool's compressed output into another tool's message — smaller, so the
		// never-worse check passes, then frozen and replayed for the whole session.
		{
			"an unlabelled block is dropped when the step has several results",
			`<step id="3"><result id="0">a</result><result>b</result></step>`,
			2, map[int]string{0: "a"},
		},
		{
			"an unlabelled block cannot claim id 0 when the real one was dropped",
			`<step id="3"><result>b</result></step>`,
			2, map[int]string{},
		},
		{
			"an unparseable id is not id 0 either",
			`<step id="3"><result id="two">b</result></step>`,
			2, map[int]string{},
		},
		{
			"an unterminated unlabelled block is dropped too",
			`<step id="3"><result>truncated mid`,
			2, map[int]string{},
		},
		{"single quotes", `<step id="3"><result id='2'>a</result></step>`, 3, map[int]string{2: "a"}},
		{"no result blocks", `I could not compress this step.`, 1, map[int]string{}},
		{
			"first wins on duplicate ids",
			`<step id="3"><result id="0">first</result><result id="0">second</result></step>`,
			1, map[int]string{0: "first"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseReducedResults(tc.in, tc.n)
			if len(got) != len(tc.want) {
				t.Fatalf("parsed %d results, want %d: %#v", len(got), len(tc.want), got)
			}
			for k, w := range tc.want {
				if strings.TrimSpace(got[k]) != w {
					t.Errorf("result %d = %q, want %q", k, got[k], w)
				}
			}
		})
	}
}

// --- config ----------------------------------------------------------------

func TestAgentDietDefaultsMatchThePaper(t *testing.T) {
	d := newAgentDietTest(t, "", nil)
	if d.delay != 2 || d.ctxBefore != 1 || d.minStep != 500 {
		t.Errorf("defaults a=%d b=%d θ=%d, want the paper's tuned 2/1/500",
			d.delay, d.ctxBefore, d.minStep)
	}
	if d.minSaved != 400 || d.maxKeepRatio != 0.8 {
		t.Errorf("apply-gate defaults %d/%.2f, want the artifact's 400/0.80",
			d.minSaved, d.maxKeepRatio)
	}
	if d.tailOnly {
		t.Error("cache_tail_only defaults true; with an age-chosen target that would make " +
			"the component a silent no-op on every caching backend")
	}
}

func TestAgentDietConfigOverrides(t *testing.T) {
	d := newAgentDietTest(t, "delay_steps: 0\ncontext_steps: 2\nmin_step_tokens: 0\n"+
		"min_saved_tokens: 10\nmax_keep_ratio: 0.5\ncache_tail_only: true\n", nil)
	if d.delay != 0 || d.ctxBefore != 2 || d.minStep != 0 || d.minSaved != 10 ||
		d.maxKeepRatio != 0.5 || !d.tailOnly {
		t.Errorf("overrides not applied: %+v", d)
	}
}

func TestAgentDietRejectsUnparseableConfig(t *testing.T) {
	if _, err := newAgentDiet([]byte("delay_steps: [not, an, int]\n")); err == nil {
		t.Error("newAgentDiet accepted a malformed config; a typo must fail at boot")
	}
}

// --- small helpers ---------------------------------------------------------

func mustText(m bschemas.ChatMessage) string {
	if m.Content == nil {
		return ""
	}
	if m.Content.ContentStr != nil {
		return *m.Content.ContentStr
	}
	var s string
	for _, b := range m.Content.ContentBlocks {
		if b.Text != nil {
			s += *b.Text
		}
	}
	return s
}
