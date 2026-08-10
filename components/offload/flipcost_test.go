package offload

import (
	"fmt"
	"strings"
	"testing"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/schema"
	"github.com/rossoctl/context-guru/store"
)

// replaySession drives a long-horizon session through `mask` and counts the provider
// cache-write it would incur from REPRESENTATION FLIPS: a message inside the
// already-cached prefix whose forwarded bytes differ from the bytes sent last turn
// invalidates the cache from its index onward, so the whole suffix is re-written.
//
// ttl/slide select the store behavior under test. This is the issue's cost hypothesis
// (~15 reverts ≈ 2.5M cache-write tokens) reduced to something deterministic and
// measurable in CI: no gateway, no model, just the replay bookkeeping the fix changes.
func replaySession(t *testing.T, ttlSeconds int, slide bool, turns, secsPerTurn int) (flips, writeTokens int) {
	t.Helper()
	st := store.NewMemory(store.Options{TTLSeconds: ttlSeconds})
	now := time.Unix(0, 0)
	st.SetClock(func() time.Time { return now })
	if !slide {
		st.DisableSlidingTTLForTest()
	}
	// keep_recent: 0 so the NEWEST (uncached) tool output is itself a mask candidate —
	// with a keep-recent window the tail is never masked, so no decision is ever frozen
	// and there is nothing for the TTL to lose.
	comp, err := newMask([]byte("keep_recent: 0\nmin_tokens: 100\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := comp.(*Mask)

	var hist []bschemas.ChatMessage
	prev := map[int]string{}
	for turn := 0; turn < turns; turn++ {
		hist = append(hist, tool(fmt.Sprintf("output %d\n", turn)+
			strings.Repeat("verbose tool output line\n", 60)))
		// The agent re-sends the ORIGINAL history verbatim every turn, plus a new tail.
		req := &bschemas.BifrostChatRequest{Input: append([]bschemas.ChatMessage(nil), hist...)}
		c := &components.Ctx{Session: "s", Store: st, CacheAware: true,
			MaxCachedIdx: len(hist) - 2} // all but the newest message is already cached
		var rep components.Report
		if _, err := m.Offload(req, &rep, c); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(req.Input)-1; i++ {
			got := schema.MessageText(req.Input[i])
			if was, seen := prev[i]; seen && was != got {
				flips++
				for j := i; j < len(req.Input); j++ {
					writeTokens += schema.TextTokens(schema.MessageText(req.Input[j]))
				}
			}
			prev[i] = got
		}
		now = now.Add(time.Duration(secsPerTurn) * time.Second)
	}
	return flips, writeTokens
}

// The headline claim: over a session longer than the old TTL, the write-only TTL flips
// already-cached messages and the sliding TTL does not. Numbers are logged so the PR can
// quote them, and asserted so a regression fails the build.
func TestFlipCostOverLongSession(t *testing.T) {
	const turns, secsPerTurn = 120, 26 // 120 turns x 26 s/req = 3120 s > the old 1800 s TTL
	oldFlips, oldWrite := replaySession(t, 1800, false, turns, secsPerTurn)
	newFlips, newWrite := replaySession(t, 1800, true, turns, secsPerTurn)

	t.Logf("write-only TTL (old): flips=%d cache-write=%d tokens  premium@$2.30/M=$%.2f",
		oldFlips, oldWrite, float64(oldWrite)*2.30/1e6)
	t.Logf("sliding TTL   (new): flips=%d cache-write=%d tokens  premium@$2.30/M=$%.2f",
		newFlips, newWrite, float64(newWrite)*2.30/1e6)

	if oldFlips == 0 {
		t.Fatal("the old write-only TTL must reproduce the flips this issue is about")
	}
	if newFlips != 0 {
		t.Fatalf("the sliding TTL must eliminate representation flips, got %d (%d tokens)",
			newFlips, newWrite)
	}
}
