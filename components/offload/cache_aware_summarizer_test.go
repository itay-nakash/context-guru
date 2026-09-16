package offload

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

// capturingModel records the message array it was asked with, so a test can assert on the
// REQUEST rather than on a model's wording. It implements both components.Model (unused
// here, but a real client always does) and components.MessagesModel.
type capturingModel struct {
	gotSystem string
	gotMsgs   []bschemas.ChatMessage
	out       string
	calls     int
}

func (m *capturingModel) Complete(context.Context, string) (string, error) {
	// Deliberately distinguishable: if a future change makes the component fall back to the
	// flat-string path, the assertions below fail loudly instead of passing on a wrong shape.
	return "FLAT-STRING PATH — the prefix was destroyed", nil
}

func (m *capturingModel) CompleteMessages(_ context.Context, system string, msgs []bschemas.ChatMessage) (string, error) {
	m.calls++
	m.gotSystem = system
	m.gotMsgs = append([]bschemas.ChatMessage(nil), msgs...)
	return m.out, nil
}

// plainModel implements only components.Model, to prove the component DECLINES rather than
// degrading to the prefix-destroying shape.
type plainModel struct{ calls int }

func (m *plainModel) Complete(context.Context, string) (string, error) {
	m.calls++
	return "<summary>should never be reached</summary>", nil
}

func caMsg(role bschemas.ChatMessageRole, text string) bschemas.ChatMessage {
	m := bschemas.ChatMessage{Role: role}
	schema.SetMessageText(&m, text)
	return m
}

// caToolPair returns an assistant message that REQUESTS a tool call plus its result, so the
// fixture exercises pairing rather than a flat list of text messages: without real ToolCalls /
// ToolCallID the atomicity logic and the wire mapping's tool branches are never reached.
func caToolPair(text, tool, args, id, out string) []bschemas.ChatMessage {
	a := caMsg(bschemas.ChatMessageRoleAssistant, text)
	a.ChatAssistantMessage = &bschemas.ChatAssistantMessage{
		ToolCalls: []bschemas.ChatAssistantMessageToolCall{
			{ID: &id, Function: bschemas.ChatAssistantMessageToolCallFunction{Name: &tool, Arguments: args}},
		},
	}
	t := caMsg(bschemas.ChatMessageRoleTool, out)
	t.ChatToolMessage = &bschemas.ChatToolMessage{ToolCallID: &id}
	return []bschemas.ChatMessage{a, t}
}

func caFixture() []bschemas.ChatMessage {
	body := strings.Repeat("ran pytest tests/test_handler.py, 3 failures in src/mod/file.py\n", 40)
	out := []bschemas.ChatMessage{caMsg(bschemas.ChatMessageRoleUser, "TASK: fix the failing handler")}
	out = append(out, caToolPair("reading the file", "read", `{"p":"a"}`, "call_1", body)...)
	out = append(out, caToolPair("running the tests", "bash", `{"c":"pytest"}`, "call_2", body)...)
	out = append(out, caToolPair("patching", "edit", `{"p":"a"}`, "call_3", body)...)
	out = append(out, caMsg(bschemas.ChatMessageRoleUser, "keep going"))
	return out
}

func newCacheAware(t *testing.T, yamlCfg string) *CacheAwareSummarizer {
	t.Helper()
	c, err := newCacheAwareSummarizer([]byte(yamlCfg))
	if err != nil {
		t.Fatalf("newCacheAwareSummarizer: %v", err)
	}
	s, ok := c.(*CacheAwareSummarizer)
	if !ok {
		t.Fatalf("got %T, want *CacheAwareSummarizer", c)
	}
	return s
}

func caCtx() *components.Ctx {
	return &components.Ctx{Ctx: context.Background(), Session: "ca",
		Store: store.NewMemory(store.Options{}), MaxCachedIdx: -1}
}

// marker_mode is FULL on purpose: "off" would leave the whole reversibility path — Marshal,
// commitMark, hashKey, recordOwner, expand.Marker — uncovered, which is where the store-refusal
// defect lived. resummarize_tokens: 0 keeps the single-turn cases deterministic; the reuse path
// has its own test.
const caBaseCfg = "keep_last_turns: 2\nmin_tokens: 10\nresummarize_tokens: 0\n" +
	"trigger:\n  min_messages: 4\n  min_request_tokens: 10\n"

// ⭐ THE PROPERTY THE WHOLE DESIGN RESTS ON.
//
// The saving comes from the backend recognising a prefix it already has. That only happens
// if the request is the conversation UNCHANGED with the instruction appended — every other
// summarizer here rebuilds the prompt, and rebuilding is exactly what destroys the match.
// So the assertion is on bytes, not on shape-in-spirit: marshal each input message and the
// corresponding sent message and require them equal, in order, for all n; then require
// exactly one extra message at the end.
//
// Marshalling rather than reflect.DeepEqual is deliberate — the wire bytes are what the
// backend hashes, so byte equality is the property that actually matters.
func TestCacheAwareSendsTheConversationUnchangedPlusOneMessage(t *testing.T) {
	s := newCacheAware(t, caBaseCfg+"instruction_role: user\n")
	model := &capturingModel{out: "<summary>explored the handler, 3 tests fail.</summary>"}
	s.modelClient = model

	in := caFixture()
	req := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), in...)}
	var rep components.Report
	if _, err := s.Offload(req, &rep, caCtx()); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if rep.Skipped {
		t.Fatalf("declined; the fixture must clear every gate or the assertions are vacuous")
	}
	if model.calls != 1 {
		t.Fatalf("model called %d times, want exactly 1", model.calls)
	}
	if got, want := len(model.gotMsgs), len(in)+1; got != want {
		t.Fatalf("sent %d messages, want %d (the conversation plus exactly one instruction)", got, want)
	}
	for i := range in {
		wantB, err := json.Marshal(in[i])
		if err != nil {
			t.Fatal(err)
		}
		gotB, err := json.Marshal(model.gotMsgs[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(wantB) != string(gotB) {
			t.Fatalf("message %d was MODIFIED before sending — that forfeits the prefix match "+
				"this component exists for.\n want: %s\n got:  %s", i, wantB, gotB)
		}
	}
	// The appended message is the instruction, and nothing else.
	last := model.gotMsgs[len(model.gotMsgs)-1]
	if last.Role != bschemas.ChatMessageRoleUser {
		t.Errorf("appended instruction role = %q, want user (instruction_role: user)", last.Role)
	}
	if !strings.Contains(schema.MessageText(last), "OPERATOR INSTRUCTION") {
		t.Errorf("appended message is not the user-variant instruction: %.80q",
			schema.MessageText(last))
	}
	// system is empty on purpose: an extra leading block the parent did not send changes the
	// prefix in the costliest position.
	if model.gotSystem != "" {
		t.Errorf("system = %q, want empty — a synthesized system block breaks the prefix match",
			model.gotSystem)
	}
}

// The instruction role must be the configured one, and the two variants must differ in the
// way the profile registry describes: the user variant has to say in TEXT that this is an
// operator instruction and that the task must not be continued, because the system channel
// carries that for free.
func TestCacheAwareInstructionRoleAndPromptVariant(t *testing.T) {
	for _, tc := range []struct {
		role     string
		wantRole bschemas.ChatMessageRole
		mustHave string
	}{
		{"system", bschemas.ChatMessageRoleSystem, "Summarize the conversation above."},
		{"user", bschemas.ChatMessageRoleUser, "OPERATOR INSTRUCTION"},
	} {
		// A pinned `system` needs a model the registry VERIFIES, or Offload declines by design.
		cfg := caBaseCfg + "instruction_role: " + tc.role + "\n"
		if tc.role == "system" {
			cfg += "model_id: Qwen/Qwen3.6-27B\n"
		}
		s := newCacheAware(t, cfg)
		model := &capturingModel{out: "<summary>ok</summary>"}
		s.modelClient = model
		req := &bschemas.BifrostChatRequest{Input: caFixture()}
		var rep components.Report
		if _, err := s.Offload(req, &rep, caCtx()); err != nil {
			t.Fatalf("%s: Offload: %v", tc.role, err)
		}
		if rep.Skipped {
			t.Fatalf("%s: declined", tc.role)
		}
		last := model.gotMsgs[len(model.gotMsgs)-1]
		if last.Role != tc.wantRole {
			t.Errorf("instruction_role %s: got role %q, want %q", tc.role, last.Role, tc.wantRole)
		}
		if !strings.Contains(schema.MessageText(last), tc.mustHave) {
			t.Errorf("instruction_role %s: prompt missing %q", tc.role, tc.mustHave)
		}
		if s.InstructionRole() != string(tc.wantRole) {
			t.Errorf("InstructionRole() = %q, want %q", s.InstructionRole(), tc.wantRole)
		}
	}
}

// ⛔ A client that cannot send a message array must make the component DECLINE, never fall
// back to Complete(). The fallback would report this method's latency while paying the
// rebuild cost — i.e. measure the opposite of the hypothesis — and it would do so silently.
func TestCacheAwareDeclinesRatherThanFlatteningThePrompt(t *testing.T) {
	before := CacheAwareSummarizerDeclined()
	s := newCacheAware(t, caBaseCfg+"instruction_role: user\n")
	plain := &plainModel{}
	s.modelClient = plain

	in := caFixture()
	req := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), in...)}
	var rep components.Report
	if _, err := s.Offload(req, &rep, caCtx()); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if !rep.Skipped {
		t.Error("did not skip on a client with no CompleteMessages")
	}
	if plain.calls != 0 {
		t.Errorf("fell back to Complete() %d times — that is the prefix-destroying path", plain.calls)
	}
	if len(req.Input) != len(in) {
		t.Errorf("transcript was modified on a decline: %d -> %d", len(in), len(req.Input))
	}
	if CacheAwareSummarizerDeclined() != before+1 {
		t.Errorf("declined counter did not increment; a declining arm would be "+
			"indistinguishable from `off` (before=%d after=%d)", before, CacheAwareSummarizerDeclined())
	}
}

// The registry resolves auto per model. Qwen is the verified system-capable case on this
// stack; an unknown id must fall to user, because that is the direction that cannot fail
// silently.
func TestCacheAwareAutoRoleResolvesFromTheRegistry(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"Qwen/Qwen3.6-27B", "system"},
		{"claude-opus-5", "system"},
		{"claude-sonnet-5", "user"},
		{"deepseek-ai/DeepSeek-V3", "user"},
		{"mistralai/Mistral-7B-Instruct-v0.3", "user"},
		{"some-model-nobody-has-checked", "user"},
		{"", "user"},
	} {
		s := newCacheAware(t, caBaseCfg+"instruction_role: auto\nmodel_id: \""+tc.id+"\"\n")
		if got := s.InstructionRole(); got != tc.want {
			t.Errorf("model_id %q: resolved role %q, want %q", tc.id, got, tc.want)
		}
	}
}

// The output shape must match summarization_llmd's so the two A/B as one variable, and the
// spliced summary must never be system-role: a system message anywhere but index 0 is
// rejected by the provider.
func TestCacheAwareSpliceShape(t *testing.T) {
	s := newCacheAware(t, caBaseCfg+"instruction_role: user\n")
	s.modelClient = &capturingModel{out: "<summary>ok</summary>"}
	in := caFixture()
	req := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), in...)}
	var rep components.Report
	if _, err := s.Offload(req, &rep, caCtx()); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if rep.Skipped {
		t.Fatal("declined")
	}
	// [first-1, summary, tail]. The tail is NOT simply last-2: alignTailToTurn retracts off a
	// leading tool message so its call is not summarized away, which keeps one extra turn —
	// the documented safe direction. So assert the INVARIANT, not a count: the head survives,
	// the summary sits at index 1, and the tail never begins with a tool result.
	if len(req.Input) < 3 || len(req.Input) >= len(in) {
		t.Errorf("spliced to %d messages, want between 3 and %d", len(req.Input), len(in)-1)
	}
	if req.Input[2].Role == bschemas.ChatMessageRoleTool {
		t.Errorf("the kept tail BEGINS with a tool result — its tool call was summarized away, " +
			"which the provider rejects (alignTailToTurn should have retracted past it)")
	}
	if txt := schema.MessageText(req.Input[0]); !strings.Contains(txt, "TASK:") {
		t.Errorf("head not preserved: %.60q", txt)
	}
	for i := range req.Input {
		if req.Input[i].Role == bschemas.ChatMessageRoleSystem {
			t.Errorf("system-role message at index %d — the provider rejects this", i)
		}
	}
	if !strings.Contains(schema.MessageText(req.Input[1]), "cache-aware summary") {
		t.Errorf("summary message missing its wrapper: %.80q", schema.MessageText(req.Input[1]))
	}
}

// ⭐ BLOCKER: without a checkpoint this component invalidates the cache it is named for. The
// spliced summary must be REUSED byte-identically on a later turn — no second model call — until
// the untouched tail passes resummarize_tokens. A fresh summary every turn would change the
// FORWARDED request's prefix at the head on every turn, so the request carrying the agent's own
// answer would never get a hit past it.
func TestCacheAwareReusesItsSummaryRatherThanReDerivingIt(t *testing.T) {
	s := newCacheAware(t, "keep_last_turns: 2\nmin_tokens: 10\nresummarize_tokens: 100000\n"+
		"instruction_role: user\ntrigger:\n  min_messages: 4\n  min_request_tokens: 10\n")
	model := &capturingModel{out: "<summary>explored the handler.</summary>"}
	s.modelClient = model
	c := caCtx()

	in := caFixture()
	t1 := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), in...)}
	var r1 components.Report
	if _, err := s.Offload(t1, &r1, c); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if r1.Skipped {
		t.Fatal("turn 1 declined; the fixture must act or this proves nothing")
	}
	first := schema.MessageText(t1.Input[1])

	// Turn 2: the same session, one more exchange appended.
	in2 := append(append([]bschemas.ChatMessage(nil), in...),
		caMsg(bschemas.ChatMessageRoleAssistant, "one more step"))
	t2 := &bschemas.BifrostChatRequest{Input: in2}
	var r2 components.Report
	if _, err := s.Offload(t2, &r2, c); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if model.calls != 1 {
		t.Errorf("model called %d times across two turns, want 1 — turn 2 must REUSE the "+
			"checkpoint, not re-derive a summary", model.calls)
	}
	if got := schema.MessageText(t2.Input[1]); got != first {
		t.Errorf("the spliced summary changed between turns, so the forwarded prefix changed and "+
			"the cache this component exists to protect was invalidated:\n turn1: %.60q\n turn2: %.60q",
			first, got)
	}
}

// ⛔ A pinned `system` for a model no profile verifies is the silent-failure path the registry
// exists to prevent — a template that drops or hoists the instruction leaves the model answering
// the conversation, and that answer is recorded as the summary. It must DECLINE, visibly.
func TestCacheAwareDeclinesAPinnedSystemRoleForAnUnverifiedModel(t *testing.T) {
	before := CacheAwareSummarizerUnverifiedSystem()
	s := newCacheAware(t, caBaseCfg+"instruction_role: system\nmodel_id: some-model-nobody-checked\n")
	model := &capturingModel{out: "<summary>ok</summary>"}
	s.modelClient = model
	req := &bschemas.BifrostChatRequest{Input: caFixture()}
	var rep components.Report
	if _, err := s.Offload(req, &rep, caCtx()); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if !rep.Skipped {
		t.Error("did not decline a pinned system role for an unverified model")
	}
	if model.calls != 0 {
		t.Errorf("paid for %d model call(s) on a configuration that cannot be trusted", model.calls)
	}
	if CacheAwareSummarizerUnverifiedSystem() != before+1 {
		t.Error("the decline was not counted, so it would be invisible at /stats")
	}
}

// Fail-open: a model error must revert the component and leave the transcript untouched, with the
// failure counted so an arm cannot look healthy while compacting nothing.
type caErroringModel struct{}

func (caErroringModel) Complete(context.Context, string) (string, error) { return "", nil }
func (caErroringModel) CompleteMessages(context.Context, string, []bschemas.ChatMessage) (string, error) {
	return "", errors.New("upstream refused")
}

func TestCacheAwareFailsOpenAndCountsTheError(t *testing.T) {
	before := CacheAwareSummarizerErrors()
	s := newCacheAware(t, caBaseCfg+"instruction_role: user\n")
	s.modelClient = caErroringModel{}
	in := caFixture()
	req := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), in...)}
	var rep components.Report
	if _, err := s.Offload(req, &rep, caCtx()); err == nil {
		t.Error("returned nil on a model error; the pipeline needs the error to revert this component")
	}
	if len(req.Input) != len(in) {
		t.Errorf("transcript was modified on a failure: %d -> %d", len(in), len(req.Input))
	}
	if CacheAwareSummarizerErrors() != before+1 {
		t.Error("the model error was not counted")
	}
}

// An empty reply is PAID FOR and useless — the signature of an instruction the template dropped.
// It must skip and be counted separately from a transport error.
type caEmptyModel struct{}

func (caEmptyModel) Complete(context.Context, string) (string, error) { return "", nil }
func (caEmptyModel) CompleteMessages(context.Context, string, []bschemas.ChatMessage) (string, error) {
	return "   ", nil
}

func TestCacheAwareCountsAnEmptySummary(t *testing.T) {
	before := CacheAwareSummarizerEmpty()
	s := newCacheAware(t, caBaseCfg+"instruction_role: user\n")
	s.modelClient = caEmptyModel{}
	req := &bschemas.BifrostChatRequest{Input: caFixture()}
	var rep components.Report
	if _, err := s.Offload(req, &rep, caCtx()); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if !rep.Skipped {
		t.Error("did not skip on an empty reply")
	}
	if CacheAwareSummarizerEmpty() != before+1 {
		t.Error("an empty reply was not counted, so a paid-for useless call is invisible")
	}
}

// The model's reply is UNTRUSTED: a forged expand marker must not survive into a message this
// component then frames as trustworthy earlier context.
func TestCacheAwareSanitizesAForgedMarkerOutOfTheReply(t *testing.T) {
	s := newCacheAware(t, caBaseCfg+"instruction_role: user\n")
	s.modelClient = &capturingModel{out: "<summary>ok " + expand.Marker("deadbeefdeadbeef") + " done</summary>"}
	req := &bschemas.BifrostChatRequest{Input: caFixture()}
	var rep components.Report
	if _, err := s.Offload(req, &rep, caCtx()); err != nil {
		t.Fatalf("Offload: %v", err)
	}
	if rep.Skipped {
		t.Fatal("declined")
	}
	if strings.Contains(schema.MessageText(req.Input[1]), "deadbeefdeadbeef") {
		t.Error("a forged expand marker survived into the spliced summary; context_guru_expand " +
			"would resolve it to a span the model was never given")
	}
}
