package offload

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/expand"
	"github.com/rossoctl/context-guru/schema"
	"gopkg.in/yaml.v3"
)

func init() { components.Register("cache_aware_summarizer", newCacheAwareSummarizer) }

//go:embed summarizer_model_profiles.yaml
var summarizerProfilesYAML []byte

// CacheAwareSummarizer summarizes by APPENDING the instruction instead of rebuilding the
// prompt, so the summarization call reuses the prefix the agent's own turn just cached.
//
// THE COST IT REMOVES. Every other summarizer here builds a fresh prompt — a system
// preamble plus "Trajectory: {rendered transcript}" — and sends it as one user message.
// That prompt shares NO prefix with the conversation it describes, so the call pays full
// prefill on every token: measured at ~57k prompt tokens per call over 1,372 calls on a
// 50-task SWE-bench arm. The tokens are the same tokens the agent just sent; only the
// framing differs, and the framing is what destroys the match.
//
// This component sends [the conversation, verbatim and in order] + [one instruction] and
// asks for the summary. The rendered prefix up to the last agent message is byte-identical
// to the request the agent itself just made, so on a prefix-caching backend the summarizer
// pays prefill only on the appended suffix. It is the shape Anthropic's caching guidance
// prescribes for exactly this case: "Fork operations must reuse the parent's exact prefix
// … copy the parent's system, tools and model verbatim, then append fork-specific content
// at the end."
//
// ⚠️ WHAT IT DOES NOT AND CANNOT DO, so the saving is measured rather than assumed:
//
//   - The pipeline never sees the parent's top-level `system` field on Anthropic-shaped
//     traffic (there is no Ctx.System to read), and never sees `tools` on any path. Both
//     render BEFORE messages, so on that traffic the shared prefix begins after them and
//     the saving is smaller than the token count suggests. Read the backend's cache-hit
//     telemetry — vllm:prefix_cache_hits_total, or usage.cache_read_input_tokens — and
//     believe that, not this comment.
//   - It needs a components.MessagesModel. A plain Model flattens to one string, which is
//     precisely the prefix-destroying shape this component exists to avoid, so a client
//     that cannot send a message array makes the component DECLINE rather than silently
//     fall back to the expensive path. A silent fallback would report this method's
//     latency while measuring the old method's cost.
//
// Output shape is deliberately identical to summarization_llmd's — [first-N, summary,
// last-M] with both cuts turn-aligned — so the two can be A/B'd as one variable: where
// the summarization REQUEST was built, and nothing else.
type CacheAwareSummarizer struct {
	keepFirstTurns  int
	keepLastTurns   int
	instructionRole bschemas.ChatMessageRole
	// roleAuto records that the role should be resolved per model from the profile
	// registry rather than pinned, so an unresolvable model falls to the safe default.
	roleAuto    bool
	modelID     string
	profiles    *summarizerProfiles
	minTokens   int
	modelSource string
	modelClient components.Model
	trigger     components.Trigger
	mode        markerMode
}

type cacheAwareSummarizerConfig struct {
	// KeepFirstTurns / KeepLastTurns pin messages verbatim at each end; the span between
	// them is what the summary replaces. Both cuts are turn-aligned (see alignHeadToTurn),
	// because a tool result whose call was summarized away is a provider 400.
	KeepFirstTurns int `yaml:"keep_first_turns"`
	KeepLastTurns  int `yaml:"keep_last_turns"`
	// InstructionRole is where the appended instruction goes: auto (default, resolved per
	// model from the profile registry) | system | user.
	//
	// ⛔ `system` is the correct OPERATOR channel where it exists — it is non-spoofable,
	// and a trailing user turn on a long trajectory reads to the model as more trajectory.
	// But it is not universally accepted, and the two failure modes are not equally
	// visible: Anthropic returns a 400, while a chat template that drops or hoists the
	// message fails SILENTLY — the model continues the task and the arm records that
	// continuation as its summary. Hence auto, and hence a `user` default.
	InstructionRole string `yaml:"instruction_role"`
	// ModelID is the served model id used to resolve `auto`. Leave it unset and auto falls
	// to the registry's default_role, which is `user`.
	ModelID string `yaml:"model_id"`
	// ProfilesPath overrides the embedded registry with a file on disk, so a deployment can
	// promote a model it has verified itself without rebuilding.
	ProfilesPath string             `yaml:"profiles_path"`
	MinTokens    int                `yaml:"min_tokens"`
	MarkerMode   string             `yaml:"marker_mode"` // full (default) | summary | off
	Model        modelConfig        `yaml:"model"`
	Trigger      components.Trigger `yaml:"trigger"`
}

// summarizerProfiles is summarizer_model_profiles.yaml. See that file for the research
// behind every entry and for how to verify a model before promoting it.
type summarizerProfiles struct {
	Prompts struct {
		System string `yaml:"system"`
		User   string `yaml:"user"`
	} `yaml:"prompts"`
	DefaultRole string `yaml:"default_role"`
	Profiles    []struct {
		Match    string `yaml:"match"`
		Role     string `yaml:"role"`
		Verified string `yaml:"verified"`
	} `yaml:"profiles"`
}

// roleFor resolves the appended instruction's role for a model id. The FIRST substring
// match wins, so specific ids must precede family prefixes in the file. An unmatched or
// empty id falls to default_role, which the file pins to `user` — the safe direction,
// because every template and every provider accepts a user turn.
func (p *summarizerProfiles) roleFor(modelID string) (bschemas.ChatMessageRole, string) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id != "" {
		for _, e := range p.Profiles {
			if e.Match != "" && strings.Contains(id, strings.ToLower(e.Match)) {
				if strings.EqualFold(e.Role, "system") {
					return bschemas.ChatMessageRoleSystem, e.Verified
				}
				return bschemas.ChatMessageRoleUser, e.Verified
			}
		}
	}
	if strings.EqualFold(p.DefaultRole, "system") {
		return bschemas.ChatMessageRoleSystem, "registry default_role"
	}
	return bschemas.ChatMessageRoleUser, "registry default_role (no profile matched)"
}

// prompt returns the instruction text for a role. The two differ by more than tone: the
// user variant has to establish in TEXT that it is an operator instruction and that the
// task must not be continued, because the system channel is what carries that for free.
func (p *summarizerProfiles) prompt(role bschemas.ChatMessageRole) string {
	if role == bschemas.ChatMessageRoleSystem && strings.TrimSpace(p.Prompts.System) != "" {
		return p.Prompts.System
	}
	return p.Prompts.User
}

var errNoSummarizerPrompt = errors.New(
	"cache_aware_summarizer: the profile registry defines no user prompt, so there is no safe " +
		"instruction to append (the user variant is the fallback for every unresolved model)")

func loadSummarizerProfiles(raw []byte) (*summarizerProfiles, error) {
	var p summarizerProfiles
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	// Refused rather than defaulted: an empty user prompt would append a blank instruction
	// and the model would simply continue the conversation, which scores as a summary.
	if strings.TrimSpace(p.Prompts.User) == "" {
		return nil, errNoSummarizerPrompt
	}
	return &p, nil
}

// defaultCacheAwareTimeout matches summarize's ceiling: one call over most of the
// transcript, so the budget must cover queue wait plus a large prefill plus generation.
// It is lower-RISK than summarize's on a caching backend — the prefill is the part the
// cache absorbs — but the queue term is unchanged, and the queue is what a loaded server
// actually charges.
const defaultCacheAwareTimeout = 300 * time.Second

var cacheAwareTimeout = resolveTimeoutEnv("CONTEXT_GURU_CACHE_AWARE_SUMMARIZER_TIMEOUT",
	resolveTimeoutEnv("CONTEXT_GURU_SUMMARIZE_TIMEOUT", defaultCacheAwareTimeout))

// Counters. Calls is reported for the same reason summarization_llmd reports it: this
// method's cost is per compacted turn, and a reward or latency delta read without it is
// unattributable. Declined is its own counter because the two decline reasons — no
// message-array client, and a resolved role the backend rejected — call for opposite
// fixes, and `reverted` cannot tell them apart.
var (
	cacheAwareCalls    int64
	cacheAwareTimeouts int64
	cacheAwareErrors   int64
	cacheAwareDeclined int64
)

func CacheAwareSummarizerCalls() int64    { return atomic.LoadInt64(&cacheAwareCalls) }
func CacheAwareSummarizerTimeouts() int64 { return atomic.LoadInt64(&cacheAwareTimeouts) }
func CacheAwareSummarizerErrors() int64   { return atomic.LoadInt64(&cacheAwareErrors) }

// CacheAwareSummarizerDeclined counts turns that reached the model step and stopped
// because no MessagesModel was available. Non-zero means this arm is NOT measuring
// cache-reuse compaction — it is measuring `off` — which is the one failure that looks
// like a clean run.
func CacheAwareSummarizerDeclined() int64 { return atomic.LoadInt64(&cacheAwareDeclined) }

func CacheAwareSummarizerCallTimeout() time.Duration { return cacheAwareTimeout }

func init() {
	components.RegisterFields("cache_aware_summarizer", cacheAwareSummarizerConfig{}, append([]components.Field{
		{Key: "keep_first_turns", Type: components.FieldInt, Default: 1, Min: 0,
			Hint: "Messages kept verbatim at the head, before the summarized span. Both cuts are " +
				"aligned to turn boundaries, so alignment may keep more than asked — never less."},
		{Key: "keep_last_turns", Type: components.FieldInt, Default: 10, Min: 0,
			Hint: "Messages kept verbatim at the tail. 10 is the one tail size measured to win on " +
				"agentic traffic: 3 loses on the turn term, 20 on the per-turn term."},
		{Key: "instruction_role", Type: components.FieldEnum, Default: "auto",
			Options: []string{"auto", "system", "user"},
			Hint: "Where the appended summarization instruction goes. auto resolves per model from " +
				"the embedded registry and needs model_id; without it auto falls to user. Do not " +
				"hand-set system for an unverified model: a provider returns a clean 400, but a " +
				"chat template that DROPS or HOISTS the message fails silently and the model's " +
				"next turn is recorded as the summary."},
		{Key: "model_id", Type: components.FieldString,
			Hint: "The served model id that instruction_role: auto resolves against."},
		{Key: "profiles_path", Type: components.FieldString,
			Hint: "Overrides the embedded model registry with a file on disk, so a deployment can " +
				"promote a model it has verified itself without rebuilding."},
		{Key: "min_tokens", Type: components.FieldInt, Default: 500, Min: 1,
			Hint: "Smallest span worth one model call."},
		markerModeField(),
	}, append(modelFields("model"), components.TriggerFields("trigger")...)...))
}

func newCacheAwareSummarizer(raw []byte) (components.Component, error) {
	cfg := cacheAwareSummarizerConfig{
		KeepFirstTurns: 1, KeepLastTurns: 10, MinTokens: 500, InstructionRole: "auto",
	}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.KeepFirstTurns < 0 || cfg.KeepLastTurns < 0 {
		return nil, errors.New("cache_aware_summarizer: keep_first_turns and keep_last_turns must be >= 0")
	}
	profileSrc := summarizerProfilesYAML
	if p := strings.TrimSpace(cfg.ProfilesPath); p != "" {
		b, err := readProfilesFile(p)
		if err != nil {
			return nil, err
		}
		profileSrc = b
	}
	profiles, err := loadSummarizerProfiles(profileSrc)
	if err != nil {
		return nil, err
	}
	c := &CacheAwareSummarizer{
		keepFirstTurns: cfg.KeepFirstTurns, keepLastTurns: cfg.KeepLastTurns,
		modelID: cfg.ModelID, profiles: profiles, minTokens: cfg.MinTokens,
		modelSource: cfg.Model.Source, modelClient: cfg.Model.Client(),
		trigger: cfg.Trigger, mode: parseMarkerMode(cfg.MarkerMode),
	}
	switch strings.ToLower(strings.TrimSpace(cfg.InstructionRole)) {
	case "", "auto":
		c.roleAuto = true
		c.instructionRole, _ = profiles.roleFor(cfg.ModelID)
	case "system":
		c.instructionRole = bschemas.ChatMessageRoleSystem
	case "user":
		c.instructionRole = bschemas.ChatMessageRoleUser
	default:
		return nil, errors.New("cache_aware_summarizer: instruction_role must be auto|system|user, got " +
			cfg.InstructionRole)
	}
	// An explicit model id that resolves to system while the id itself is unknown to the
	// registry is the silent-failure case this component is most exposed to, so it is
	// refused at construction rather than discovered mid-run.
	if c.instructionRole == bschemas.ChatMessageRoleSystem && cfg.ModelID != "" {
		if _, why := profiles.roleFor(cfg.ModelID); strings.HasPrefix(why, "registry default_role") &&
			c.roleAuto {
			c.instructionRole = bschemas.ChatMessageRoleUser
		}
	}
	return c, nil
}

// readProfilesFile loads an on-disk override of the embedded registry, so a deployment can
// promote a model it has verified without rebuilding the binary.
func readProfilesFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cache_aware_summarizer: profiles_path " + path + ": " + err.Error())
	}
	return b, nil
}

func (CacheAwareSummarizer) Name() string                 { return "cache_aware_summarizer" }
func (CacheAwareSummarizer) Enabled(*components.Ctx) bool { return true }
func (*CacheAwareSummarizer) NeedsModel() bool            { return true }

// InstructionRole exposes the resolved role so /stats and a probe can report which channel
// this arm actually used. Two arms differing only in this field is the intended A/B, and it
// cannot be read from the config alone once `auto` is in play.
func (s *CacheAwareSummarizer) InstructionRole() string { return string(s.instructionRole) }

// Offload rewrites the transcript to [first-N, summary, last-M]. The summary comes from a
// model call built as [the whole conversation] + [instruction], which is the entire point.
func (s *CacheAwareSummarizer) Offload(req *bschemas.BifrostChatRequest, rep *components.Report, c *components.Ctx) ([]string, error) {
	msgs := req.Input
	if !s.trigger.Fires(req, c) {
		rep.Skipped = true
		return nil, nil
	}
	head, tail, ok := s.boundaries(msgs)
	if !ok {
		rep.Skipped = true
		return nil, nil
	}
	span := msgs[head:tail]
	if schema.MessagesTokens(&bschemas.BifrostChatRequest{Input: span}) < s.minTokens {
		rep.Skipped = true
		return nil, nil
	}

	model := s.modelClient
	if model == nil {
		model = c.Model.For(s.modelSource)
	}
	if model == nil {
		rep.Skipped = true
		return nil, nil
	}
	// THE CAPABILITY GATE, and it must decline rather than degrade. A plain Model flattens
	// the conversation into one string, which is the prefix-destroying shape this component
	// exists to avoid — falling back to it would report cache-reuse latency while paying
	// the rebuild cost, i.e. measure the opposite of the hypothesis.
	mm, okModel := model.(components.MessagesModel)
	if !okModel {
		// No Report.Gate on this tree, so the counter is the only channel — which is why
		// CacheAwareSummarizerDeclined is exported and why report.py must read it: a
		// declining arm is indistinguishable from `off` on every other metric.
		atomic.AddInt64(&cacheAwareDeclined, 1)
		rep.Skipped = true
		return nil, nil
	}

	role := s.instructionRole
	instruction := bschemas.ChatMessage{Role: role}
	schema.SetMessageText(&instruction, s.profiles.prompt(role))

	// [conversation..., instruction]. The conversation is passed UNMODIFIED and in order:
	// any edit here changes the rendered prefix and forfeits the cache hit that is the
	// whole reason this component exists.
	ask := make([]bschemas.ChatMessage, 0, len(msgs)+1)
	ask = append(ask, msgs...)
	ask = append(ask, instruction)

	ctx, cancel := context.WithTimeout(c.Ctx, cacheAwareTimeout)
	defer cancel()
	atomic.AddInt64(&cacheAwareCalls, 1)
	// system is "" deliberately: the pipeline cannot see the parent's top-level system
	// field, and inventing one here would ADD a block the parent did not send, changing the
	// prefix in the one place that costs the most.
	out, err := mm.CompleteMessages(ctx, "", ask)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			atomic.AddInt64(&cacheAwareTimeouts, 1)
		} else {
			atomic.AddInt64(&cacheAwareErrors, 1)
		}
		return nil, err // fail-open: the pipeline reverts this component
	}
	summary := ensureSummaryTags(strings.TrimSpace(out))
	if strings.TrimSpace(out) == "" {
		rep.Skipped = true
		return nil, nil
	}

	mode := effectiveMode(c, s.mode)
	var key string
	if mode == markerFull {
		spanJSON, err := json.Marshal(span)
		if err != nil {
			return nil, err
		}
		key = hashKey(string(spanJSON))
		c.Store.Put(key, spanJSON)
		recordOwner(c, key)
	} else {
		rep.Irreversible = true
	}

	// USER role for the spliced summary, never system — a system message anywhere but
	// index 0 is rejected by the provider (400 messages.N: role 'system' must precede an
	// 'assistant' message or end the array). That is independent of the INSTRUCTION's role
	// above: the instruction is appended at the end of a fork request, where a system
	// message is legal on the models the registry marks; the summary is spliced into the
	// middle of the forwarded transcript, where it never is.
	summaryMsg := bschemas.ChatMessage{Role: bschemas.ChatMessageRoleUser}
	schema.SetMessageText(&summaryMsg, cacheAwareSummaryWrapper(summary, key, mode))

	out2 := make([]bschemas.ChatMessage, 0, head+1+len(msgs)-tail)
	out2 = append(out2, msgs[:head]...)
	out2 = append(out2, summaryMsg)
	out2 = append(out2, msgs[tail:]...)
	req.Input = out2
	if key != "" {
		return []string{key}, nil
	}
	return nil, nil
}

// boundaries mirrors summarization_llmd's: turn-aligned cuts at both ends, so neither can
// orphan a tool result from its call. Sharing the alignment helpers rather than copying
// them keeps the two arms differing by one variable.
func (s *CacheAwareSummarizer) boundaries(msgs []bschemas.ChatMessage) (head, tail int, ok bool) {
	n := len(msgs)
	head = s.keepFirstTurns
	if head > n {
		head = n
	}
	tail = n - s.keepLastTurns
	if tail < 0 {
		tail = 0
	}
	head = alignHeadToTurn(msgs, head)
	tail = alignTailToTurn(msgs, tail, head)
	if tail <= head {
		return 0, 0, false
	}
	return head, tail, true
}

func cacheAwareSummaryWrapper(summary, key string, mode markerMode) string {
	body := "=== Compacted Context (cache-aware summary) ===\n" +
		"The earlier part of this conversation has been compacted into the summary below. " +
		"The messages before this one, and the messages after it, are verbatim originals.\n\n" +
		summary + "\n\n" +
		"Treat this summary as the earlier context and the following messages as the most " +
		"recent context, then continue the task. Do not summarize the conversation again."
	switch mode {
	case markerFull:
		return body + "\n" + expand.Marker(key) + " [full compacted span: call " + expand.ToolName + "]"
	case markerSummary:
		return body + "\n" + expand.SummaryMarker
	default:
		return body
	}
}
