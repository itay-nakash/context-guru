package kvcache

// StickySession1h commits ONCE PER CONVERSATION — at the first request it ever sees for a
// given (user, conversation, model) key — to holding that whole session at either the
// 1-hour tier or the 5-minute tier, and never revisits the choice afterward.
//
// # Why sticky, and not HistoricalProbability run every turn
//
// A session that switches tiers mid-stream is not something a real deployment can do
// faithfully: once a conversation has committed to a tier, the entry it is holding was
// created at that tier, and every later request in it can only ever be served against that
// same entry — there is no operation that downgrades or upgrades an existing hold in place.
// A strategy that re-decides on fresher statistics every turn is answering a question this
// deployment cannot actually act on. This arm is the one-shot decision that models that
// constraint: whatever it chooses at turn one is what every later turn in that conversation
// gets, however much the account's own statistics move in the meantime.
//
// # The decision itself
//
// It reuses the same write-versus-recreate comparison this repo already derives in two
// other places rather than inventing a third: components/reformat/cacheinject.go's own TTL
// doc comment ("only pays when p = P(gap > 5 min) exceeds (2.0-1.25)/(1.25-0.1) = 65.2%")
// and dash/kvcachesim.go's Raise5mTo1h/SavedPerMiss. Committing to the 1-hour tier costs
// (Write1h-Write5m) extra on THIS write; the 5-minute tier's alternative, if the gap turns
// out to exceed five minutes, is not a cheap read but a full RECREATE at the Write5m rate —
// so the per-token amount at stake is (Write5m-CacheRead), and the two ratios give a
// break-even probability rather than a hand-picked threshold:
//
//	breakeven = (Write1h - Write5m) / (Write5m - CacheRead)
//
// The account's own P(return within 1h) — Stats.ReuseWithin at Horizon1h, with Stats's usual
// fallback ladder — is compared against it. Below minCell observations at every level
// (LevelNone) or with no priced rates, the decision defaults to the 5-minute tier: the least
// surprising action, and what the traffic would have got with no strategy in play at all.
type StickySession1h struct {
	// committed remembers, per conversation, whether the whole session was committed to the
	// 1-hour tier. Absence means "not decided yet" — which can only be true of a
	// conversation's first request, because every later Decide for the same key finds an
	// entry here and returns it unchanged.
	committed map[Conversation]bool
}

// NewStickySession1h builds an arm with no sessions decided yet.
func NewStickySession1h() *StickySession1h {
	return &StickySession1h{committed: map[Conversation]bool{}}
}

func (s *StickySession1h) Name() string { return StrategyStickySession1h }

func (s *StickySession1h) Describe() string {
	return "Decide once, at a conversation's first request, whether to hold that whole " +
		"session at the 1-hour tier or the 5-minute tier — then never revisit it, because a " +
		"real deployment cannot downgrade or upgrade an existing hold in place. Commits to " +
		"1h only when the account's own P(return within 1h) clears " +
		"(write_1h_rate - write_5m_rate) / (write_5m_rate - cache_read_rate), the same " +
		"write-versus-recreate break-even cacheinject's own TTL derivation uses."
}

// Decide looks up this conversation's standing commitment, making it now if this is the
// first request seen for the key — which, since Simulate never calls Decide out of
// chronological order, is exactly Observation.Turn == 1.
func (s *StickySession1h) Decide(o Observation) Action {
	key := Conversation{User: o.User, Conversation: o.Conversation, Model: o.Model}
	commit1h, ok := s.committed[key]
	if !ok {
		commit1h = decideSticky1h(o)
		s.committed[key] = commit1h
	}
	if commit1h {
		return ActionWrite1h
	}
	return ActionWrite5m
}

// decideSticky1h is the one-shot break-even comparison, made once at turn one and then held
// for the rest of the conversation by Decide's map.
func decideSticky1h(o Observation) bool {
	if !o.Pricing.Known || o.Stats == nil {
		return false
	}
	breakeven := sticky1hBreakeven(o.Pricing)
	if breakeven < 0 {
		return false
	}
	p, n, level := o.Stats.ReuseWithin(o.User, o.Model, o.Bucket, Horizon1h)
	if n == 0 || level == LevelNone {
		return false
	}
	return p > breakeven
}

// sticky1hBreakeven is the fraction of gaps that must exceed five minutes before committing
// to the 1-hour tier up front is cheaper than committing to five minutes and paying a full
// recreate whenever the gap runs past it. Negative when Write5m does not exceed CacheRead —
// which Pricing's own construction should never produce, but a hand-typed override can — so
// the caller treats it as "never worth it" rather than dividing into a meaningless ratio.
func sticky1hBreakeven(p Pricing) float64 {
	denom := p.Write5m - p.CacheRead
	if denom <= 0 {
		return -1
	}
	return (p.Write1h - p.Write5m) / denom
}
