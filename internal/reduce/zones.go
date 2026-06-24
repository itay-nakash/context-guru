package reduce

const compactionRatio = 0.8

// ComputeZones derives the frozen-prefix / live-zone split. At compaction (input
// near the limit) frozen_count is forced to 0 since the cache is rebuilt regardless.
func ComputeZones(numMessages, inputTokens, contextLimit, frozenCount int) Zones {
	atCompaction := contextLimit > 0 && float64(inputTokens) >= compactionRatio*float64(contextLimit)
	if atCompaction {
		frozenCount = 0
	}
	if frozenCount < 0 {
		frozenCount = 0
	}
	if frozenCount > numMessages {
		frozenCount = numMessages
	}
	return Zones{FrozenCount: frozenCount, AtCompaction: atCompaction}
}
