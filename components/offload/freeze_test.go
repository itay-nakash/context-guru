package offload

import (
	"strings"
	"testing"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

func tool(s string) bschemas.ChatMessage {
	t := s
	return bschemas.ChatMessage{Role: bschemas.ChatMessageRoleTool,
		Content: &bschemas.ChatMessageContent{ContentStr: &t}}
}

// maskFor builds a mask offloader with a low floor so short fixtures still qualify.
func maskFor(t *testing.T) *Mask {
	t.Helper()
	comp, err := newMask([]byte("keep_recent: 1\nmin_tokens: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	return comp.(*Mask)
}

// A long session replays a frozen mask on every turn. With a write-only TTL the
// decision died mid-session and the message flipped masked→full inside the provider's
// cached prefix; with the sliding TTL it must stay byte-identical for the whole run.
func TestFrozenMaskSurvivesLongSession(t *testing.T) {
	now := time.Unix(0, 0)
	st := store.NewMemory(store.Options{TTLSeconds: 10})
	st.SetClock(func() time.Time { return now })
	m := maskFor(t)
	body := strings.Repeat("verbose tool output line\n", 30)

	var first string
	for turn := 0; turn < 200; turn++ {
		// Every turn the agent re-sends the ORIGINAL history plus a new tail message.
		req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{
			tool(body), tool("new tail output"),
		}}
		// Turn 0 is the tail turn (nothing cached yet) — that is where a NEW mask is
		// allowed. Every later turn has the output in the already-cached prefix, so only
		// the frozen replay can keep it masked.
		maxCached := 0
		if turn == 0 {
			maxCached = -1
		}
		c := &components.Ctx{Session: "s", Store: st, CacheAware: true, MaxCachedIdx: maxCached}
		var rep components.Report
		if _, err := m.Offload(req, &rep, c); err != nil {
			t.Fatal(err)
		}
		got := schema.MessageText(req.Input[0])
		if turn == 0 {
			if got == body {
				t.Fatal("turn 0 must mask the older output")
			}
			first = got
		}
		if got != first {
			t.Fatalf("turn %d flipped representation (cache-destructive):\n want %q\n got  %q",
				turn, first, got)
		}
		now = now.Add(9 * time.Second) // ~9s/turn, 200 turns = 1800s ≫ the 10s TTL
	}
}

// TestFrozenMaskNewDecisionStillTailGated: the repair path must not become a general
// license to mutate at depth — content that was NEVER frozen stays verbatim in the
// cached prefix.
func TestNewMaskStillTailGated(t *testing.T) {
	st := store.NewMemory(store.Options{})
	m := maskFor(t)
	body := strings.Repeat("verbose tool output line\n", 30)
	req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{tool(body), tool("tail")}}
	c := &components.Ctx{Session: "s", Store: st, CacheAware: true, MaxCachedIdx: 0}
	var rep components.Report
	if _, err := m.Offload(req, &rep, c); err != nil {
		t.Fatal(err)
	}
	if schema.MessageText(req.Input[0]) != body {
		t.Fatal("a never-frozen message inside the cached prefix must stay verbatim")
	}
}

// The core of part C: when the store LOSES an established frozen decision, the provider
// still holds the masked bytes. Re-deriving them is cache-preserving; leaving the message
// verbatim is the destructive flip. So a forced store miss must not flip representation.
func TestForcedStoreMissDoesNotFlipEstablishedCompaction(t *testing.T) {
	now := time.Unix(0, 0)
	st := store.NewMemory(store.Options{TTLSeconds: 10})
	st.SetClock(func() time.Time { return now })
	m := maskFor(t)
	body := strings.Repeat("verbose tool output line\n", 30)

	run := func(maxCached int) string {
		req := &bschemas.BifrostChatRequest{Input: []bschemas.ChatMessage{tool(body), tool("tail")}}
		c := &components.Ctx{Session: "s", Store: st, CacheAware: true, MaxCachedIdx: maxCached}
		var rep components.Report
		if _, err := m.Offload(req, &rep, c); err != nil {
			t.Fatal(err)
		}
		return schema.MessageText(req.Input[0])
	}

	masked := run(-1) // turn 1: the output is in the tail, so it gets masked and frozen
	if masked == body {
		t.Fatal("turn 1 must mask")
	}

	// Force the loss: nothing reads the entry for longer than the TTL. From here on the
	// message sits in the CACHED PREFIX (MaxCachedIdx=0), which the tail gate forbids
	// mutating — the exact situation that used to revert masked→full.
	now = now.Add(11 * time.Second)
	if got := run(0); got != masked {
		t.Fatalf("a lost freeze flipped an established compaction:\n want %q\n got  %q", masked, got)
	}
	// And the repair is recorded as a repair, not as an unrepaired flip.
	dropped, repaired := st.FrozenLossStats()
	if dropped == 0 || dropped != repaired {
		t.Fatalf("want every dropped decision repaired, got dropped=%d repaired=%d", dropped, repaired)
	}
}

// A Nop store (no FrozenLoser) must degrade to the legacy behavior rather than panic or
// mutate at depth on every message.
func TestRepairLostFreezeNoopStore(t *testing.T) {
	c := &components.Ctx{Session: "s", Store: store.Nop{}}
	if repairLostFreeze(c, "mask", "anything") {
		t.Fatal("a store that cannot report losses must not authorize depth mutation")
	}
}

// The replay counters have to move, or the fix is unverifiable in a benchmark run.
func TestFrozenCountersMove(t *testing.T) {
	h0, m0 := FrozenStats()
	st := store.NewMemory(store.Options{})
	c := &components.Ctx{Session: "sCount", Store: st}
	msg := tool("some tool output")
	reapplyFrozen(c, "mask", &msg) // miss: nothing frozen yet
	freeze(c, "mask", "some tool output", "short")
	msg2 := tool("some tool output")
	reapplyFrozen(c, "mask", &msg2) // hit
	h1, m1 := FrozenStats()
	if h1 <= h0 || m1 <= m0 {
		t.Fatalf("hits/misses must both advance: %d->%d, %d->%d", h0, h1, m0, m1)
	}
}
