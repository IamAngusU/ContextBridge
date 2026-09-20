package cluster

import (
	"testing"
	"time"
)

// FuzzSchedulerNeverScoresPastHardConstraints proves that load scoring cannot
// rescue a node that failed connectivity, freshness, capacity, group,
// provider, or explicit VRAM gates.
func FuzzSchedulerNeverScoresPastHardConstraints(f *testing.F) {
	for _, seed := range []struct {
		connected                                               bool
		age, running, capacity, free, required, group, provider uint8
	}{
		{true, 0, 0, 4, 10, 5, 1, 1},
		{false, 0, 0, 4, 10, 5, 1, 1},
		{true, 255, 0, 4, 10, 5, 1, 1},
		{true, 0, 4, 4, 10, 5, 1, 1},
		{true, 0, 0, 4, 1, 10, 1, 1},
		{true, 0, 0, 4, 10, 5, 0, 1},
		{true, 0, 0, 4, 10, 5, 1, 0},
	} {
		f.Add(seed.connected, seed.age, seed.running, seed.capacity, seed.free, seed.required, seed.group, seed.provider)
	}
	f.Fuzz(func(t *testing.T, connected bool, ageCode, runningCode, capacityCode, freeCode, requiredCode, groupCode, providerCode uint8) {
		now := time.Unix(1_800_000_000, 0).UTC()
		capacity := int(capacityCode % 8)
		running := int(runningCode % 10)
		free := uint64(freeCode) << 20
		required := uint64(requiredCode) << 20
		groups := []string{}
		if groupCode%2 == 1 {
			groups = []string{"private"}
		}
		providers := []string{}
		if providerCode%2 == 1 {
			providers = []string{"ollama"}
		}
		node := Node{ID: "fuzz-node", Name: "fuzz-node", Connected: connected,
			LastSeen: now.Add(-time.Duration(ageCode) * time.Second), Capabilities: Capabilities{
				Groups: groups, Providers: providers, MaxConcurrent: capacity, Running: running,
				GPUs: []GPUCapability{{Name: "fuzz-gpu", MemoryTotal: 256 << 20, MemoryFree: free}},
			}}
		requirements := Requirements{Provider: "ollama", Group: "private", MinFreeVRAM: required}
		candidates, decision := rankWithDecision([]Node{node}, requirements, 0, now)
		hardEligible := connected && time.Duration(ageCode)*time.Second <= NodeFreshnessWindow
		effectiveCapacity := capacity
		if effectiveCapacity <= 0 {
			effectiveCapacity = 1
		}
		hardEligible = hardEligible && running < effectiveCapacity && len(groups) == 1 && len(providers) == 1
		if required > 0 {
			hardEligible = hardEligible && boundedFreeMemory(free, 256<<20) >= required
		}
		if (len(candidates) == 1) != hardEligible {
			t.Fatalf("scheduler eligibility=%v, want %v; decision=%+v", len(candidates) == 1, hardEligible, decision.Candidates)
		}
		if !hardEligible && decision.SelectedNodeID != "" {
			t.Fatalf("ineligible node won by score: %+v", decision)
		}
	})
}
