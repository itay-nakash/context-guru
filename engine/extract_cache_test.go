package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/kagenti/lab-context-engineering/canon"
	"github.com/kagenti/lab-context-engineering/config"
)

// recordingModel returns the first two records of the JSON array embedded in the
// prompt (a contained subset, so extraction succeeds and is cached) and records
// every prompt it is asked to complete. The recorded count makes a wrongly-shared
// cache observable: a body-only key calls the model once across two goals; a
// goal-aware key calls it once per goal.
type recordingModel struct {
	mu      sync.Mutex
	prompts []string
}

func (m *recordingModel) Complete(_ context.Context, prompt string) (string, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	m.mu.Unlock()

	const marker = "INPUT (return a smaller value of this same shape):\n"
	i := strings.Index(prompt, marker)
	if i < 0 {
		return "", nil
	}
	rest := prompt[i+len(marker):]
	if e := strings.Index(rest, "\n\n"); e >= 0 {
		rest = rest[:e]
	}
	var arr []any
	if json.Unmarshal([]byte(rest), &arr) == nil && len(arr) >= 2 {
		b, _ := json.Marshal(arr[:2])
		return string(b), nil
	}
	return "", nil
}

func (m *recordingModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prompts)
}

// reqWithGoal builds a request whose tool_result body is the shared large array
// and whose trailing user text is the goal. Using the "single" strategy forces the
// model to receive the body inline (so recordingModel can echo a contained subset)
// and keeps the per-request model-call count deterministic at one.
func reqWithGoal(t *testing.T, arr, goal string) canon.Request {
	t.Helper()
	body := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"list_items","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":` + jsonStr(arr) + `}]},
		{"role":"user","content":[{"type":"text","text":` + jsonStr(goal) + `}]}
	]}`)
	req, err := canon.Decode(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return req
}

// TestExtractionCacheIsGoalAware asserts that the same tool-output body re-read under
// a DIFFERENT agent goal does NOT reuse the first goal's filtered result. Observed at
// the cache level: with a body-only key the model is called once (goal B reuses goal
// A's entry); with the goal-aware key it is called twice (once per goal).
func TestExtractionCacheIsGoalAware(t *testing.T) {
	var recs []string
	for i := 0; i < 400; i++ {
		recs = append(recs, `{"id":`+itoa(i)+`,"name":"item_`+itoa(i)+`","detail":"some detail text for this record"}`)
	}
	arr := "[" + strings.Join(recs, ",") + "]"

	model := &recordingModel{}
	s := config.Default()
	s.ProtectRecent = 1
	eng := New(s, nil, nil)
	// "single" forces the inline-body strategy so each goal makes exactly one model
	// call; the cache key is what decides whether goal B reuses goal A.
	cfg := DefaultExtractConfig()
	cfg.Mode = "single"
	eng.EnableExtract(model, cfg)

	reqA := reqWithGoal(t, arr, "goal A: inspect the items for duplicate ids")
	reqB := reqWithGoal(t, arr, "goal B: summarize the names of every record")

	if _, rep := eng.Transform(context.Background(), reqA); len(rep.Candidates) == 0 {
		t.Fatalf("goal A: expected at least one extraction candidate")
	}
	afterA := model.calls()
	if afterA == 0 {
		t.Fatalf("goal A: expected the model to be called")
	}

	if _, rep := eng.Transform(context.Background(), reqB); len(rep.Candidates) == 0 {
		t.Fatalf("goal B: expected at least one extraction candidate")
	}
	afterB := model.calls()

	if afterB <= afterA {
		t.Fatalf("goal B reused goal A's cached result (cache is body-only, not goal-aware): "+
			"model calls after A=%d, after B=%d", afterA, afterB)
	}
}
