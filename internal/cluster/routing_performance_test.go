package cluster

import (
	"math"
	"testing"
	"time"
)

func TestPerformanceAwareRankingBalancesHistoryAndLiveLoad(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	model := ModelCapability{Name: "qwen-test", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}
	fast := Node{ID: "fast", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, Models: []ModelCapability{model}, MaxConcurrent: 4, Running: 1,
	}}
	slow := Node{ID: "slow", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, Models: []ModelCapability{model}, MaxConcurrent: 4,
	}}
	for i := 0; i < 4; i++ {
		recordRoutingPerformance(&fast, requirements, "", 1400, now.Add(time.Duration(i)*time.Minute), "")
		recordRoutingPerformance(&slow, requirements, "", 40000, now.Add(time.Duration(i)*time.Minute), "")
	}
	rankNow := now.Add(5 * time.Minute)
	fast.LastSeen, slow.LastSeen = rankNow, rankNow
	policy := DefaultPlacementPolicy()
	ranked, decision := rankWithDecisionForOwnerPolicy([]Node{slow, fast}, requirements, 0, "producer", rankNow, policy)
	if len(ranked) != 2 || ranked[0].Node.ID != "fast" {
		t.Fatalf("learned fast route was not preferred despite available capacity: ranked=%#v decision=%#v", ranked, decision)
	}
	byID := map[string]RoutingCandidateDecision{}
	for _, candidate := range decision.Candidates {
		byID[candidate.NodeID] = candidate
	}
	if byID["slow"].ScoreComponents.HistoricalLatency <= 0 || byID["fast"].PerformanceSamples != 4 {
		t.Fatalf("performance evidence is not explainable: %#v", decision.Candidates)
	}

	// The learned winner is not fixed truth. High current CPU/GPU/RAM and slot
	// pressure can make the historically slower idle node the better choice.
	fast.Capabilities.Running = 3
	fast.Capabilities.CPUUtilization = 100
	fast.Capabilities.MemoryTotal = 16 << 30
	fast.Capabilities.MemoryFree = 1 << 30
	fast.Capabilities.GPUs = []GPUCapability{{MemoryTotal: 12 << 30, MemoryFree: 1 << 30, Utilization: 100}}
	ranked, decision = rankWithDecisionForOwnerPolicy([]Node{slow, fast}, requirements, 0, "producer", rankNow, policy)
	if len(ranked) != 2 || ranked[0].Node.ID != "slow" {
		t.Fatalf("live load did not outweigh historical speed: ranked=%#v decision=%#v", ranked, decision)
	}

	// Historical speed remains soft evidence. Once the fast node has no free
	// slot it is a hard rejection and the slower compatible node is selected.
	fast.Capabilities.Running = fast.Capabilities.MaxConcurrent
	ranked, _ = rankWithDecisionForOwnerPolicy([]Node{slow, fast}, requirements, 0, "producer", rankNow, policy)
	if len(ranked) != 1 || ranked[0].Node.ID != "slow" {
		t.Fatalf("historical speed overrode live capacity: %#v", ranked)
	}
}

func TestPerformanceLearningUsesCurrentLoadContextWithoutMakingItTruth(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	model := ModelCapability{Name: "qwen-test", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}
	burst := Node{ID: "burst", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, Models: []ModelCapability{model}, MaxConcurrent: 4,
	}}
	steady := Node{ID: "steady", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, Models: []ModelCapability{model}, MaxConcurrent: 4,
	}}
	for i := 0; i < 4; i++ {
		completedAt := now.Add(time.Duration(i) * time.Minute)
		recordRoutingPerformance(&burst, requirements, "", 1000, completedAt, "idle:cold")
		recordRoutingPerformance(&burst, requirements, "", 12000, completedAt, "moderate:cold")
		recordRoutingPerformance(&steady, requirements, "", 2000, completedAt, "idle:cold")
		recordRoutingPerformance(&steady, requirements, "", 3000, completedAt, "moderate:cold")
	}
	rankNow := now.Add(5 * time.Minute)
	burst.LastSeen, steady.LastSeen = rankNow, rankNow
	policy := DefaultPlacementPolicy()

	ranked, decision := rankWithDecisionForOwnerPolicy([]Node{steady, burst}, requirements, 0, "producer", rankNow, policy)
	if len(ranked) != 2 || ranked[0].Node.ID != "burst" {
		t.Fatalf("idle load curve did not prefer burst node: ranked=%#v decision=%#v", ranked, decision)
	}

	// Equal current pressure removes the ordinary live-load score as a
	// differentiator. The learned moderate-load curves should now prefer the
	// node that remains responsive under that same class of pressure.
	burst.Capabilities.CPUUtilization = 50
	steady.Capabilities.CPUUtilization = 50
	ranked, decision = rankWithDecisionForOwnerPolicy([]Node{steady, burst}, requirements, 0, "producer", rankNow, policy)
	if len(ranked) != 2 || ranked[0].Node.ID != "steady" {
		t.Fatalf("contextual load curve was ignored: ranked=%#v decision=%#v", ranked, decision)
	}
	for _, candidate := range decision.Candidates {
		if candidate.PerformanceContext != "moderate:cold" || candidate.PerformanceSource != routingPerformanceSourceContext || candidate.PerformanceSamples != 4 {
			t.Fatalf("contextual evidence is not explainable: %#v", candidate)
		}
	}
}

func TestPerformanceContextFallsBackUntilItHasEnoughEvidence(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "same"}
	node := Node{ID: "node"}
	for i := 0; i < 3; i++ {
		recordRoutingPerformance(&node, requirements, "", 1000, now.Add(time.Duration(i)*time.Second), "idle:cold")
	}
	for i := 0; i < 2; i++ {
		recordRoutingPerformance(&node, requirements, "", 5000, now.Add(time.Duration(i+3)*time.Second), "moderate:cold")
	}
	estimate, ok := routingPerformanceEstimateFor(node, requirements, "moderate:cold", DefaultPlacementPolicy(), now.Add(time.Minute))
	if !ok || estimate.Source != routingPerformanceSourceRouteBaseline || estimate.Samples != 5 {
		t.Fatalf("unproven load context did not fall back to route evidence: %#v ok=%v", estimate, ok)
	}
}

func TestPerformanceContextIncludesBoundedLiveResourcesAndWarmth(t *testing.T) {
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "same"}
	node := Node{Capabilities: Capabilities{
		MaxConcurrent: 4,
		MemoryTotal:   16 << 30,
		MemoryFree:    15 << 30,
		Models:        []ModelCapability{{Name: "same", Provider: "ollama", Tasks: []string{"generation"}, Loaded: true}},
	}}
	if got := routingPerformanceContext(node, requirements, 0); got != "idle:warm" {
		t.Fatalf("unexpected idle/warm context: %q", got)
	}
	node.Capabilities.Running = 2
	node.Capabilities.CPUUtilization = 55
	node.Capabilities.GPUs = []GPUCapability{{MemoryTotal: 12 << 30, MemoryFree: 6 << 30, Utilization: 60}}
	if got := routingPerformanceContext(node, requirements, 0); got != "moderate:warm" {
		t.Fatalf("unexpected moderate/warm context: %q", got)
	}
	node.Capabilities.MemoryFree = 1 << 30
	if got := routingPerformanceContext(node, requirements, 0); got != "high:warm" {
		t.Fatalf("RAM pressure was not reflected in context: %q", got)
	}
	node.Capabilities.MemoryFree = 15 << 30
	node.Capabilities.GPUs[0].MemoryFree = 1 << 30
	node.Capabilities.GPUs[0].Utilization = 10
	if got := routingPerformanceContext(node, requirements, 0); got != "high:warm" {
		t.Fatalf("VRAM pressure was not reflected in context: %q", got)
	}
}

func TestPerformanceLearningRequiresFreshBoundedEvidence(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "same"}
	node := Node{ID: "node", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, MaxConcurrent: 1,
	}}
	policy := DefaultPlacementPolicy()
	for i := 0; i < 2; i++ {
		recordRoutingPerformance(&node, requirements, "", 1000, now.Add(time.Duration(i)*time.Second), "")
	}
	if _, ok := routingPerformanceFor(node, requirements, policy, now.Add(time.Minute)); ok {
		t.Fatal("insufficient samples became routing authority")
	}
	recordRoutingPerformance(&node, requirements, "", 1000, now.Add(2*time.Second), "")
	if _, ok := routingPerformanceFor(node, requirements, policy, now.Add(8*24*time.Hour)); ok {
		t.Fatal("stale performance evidence did not expire")
	}

	disabled := policy
	disabled.PerformanceLearning = false
	if _, ok := routingPerformanceFor(node, requirements, disabled, now.Add(time.Minute)); ok {
		t.Fatal("disabled performance learning still affected routing")
	}
}

func TestPlacementLearningDoesNotReviveExpiredAggregateState(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "same"}
	node := Node{ID: "node"}
	for index := 0; index < 5; index++ {
		recordRoutingPerformance(&node, requirements, "", 40000, now.Add(-30*24*time.Hour+time.Duration(index)*time.Minute), "idle:cold")
	}
	recordRoutingPerformance(&node, requirements, "", 1000, now, "idle:cold")
	policy := DefaultPlacementPolicy()
	if _, ok := routingPerformanceEstimateFor(node, requirements, "idle:cold", policy, now.Add(time.Minute)); ok {
		t.Fatal("one fresh completion revived expired placement evidence")
	}
	for index := 0; index < int(policy.MinimumSamples)-1; index++ {
		recordRoutingPerformance(&node, requirements, "", 1000, now.Add(time.Duration(index+1)*time.Second), "idle:cold")
	}
	estimate, ok := routingPerformanceEstimateFor(node, requirements, "idle:cold", policy, now.Add(time.Minute))
	if !ok || estimate.Samples != policy.MinimumSamples || estimate.EWMAComputeMS != 1000 {
		t.Fatalf("fresh placement evidence was unavailable or contaminated: %#v ok=%v", estimate, ok)
	}
}

func TestPerformanceEWMAIsBoundedAndRecordsAreCapped(t *testing.T) {
	now := time.Now().UTC()
	node := Node{}
	base := Requirements{Task: "generation", Provider: "ollama", Model: "base"}
	for i := 0; i < 3; i++ {
		recordRoutingPerformance(&node, base, "", 1000, now.Add(time.Duration(i)*time.Second), "")
	}
	recordRoutingPerformance(&node, base, "", math.MaxUint64, now.Add(4*time.Second), "")
	if got := node.RoutingPerformance[0].EWMAComputeMS; got > 2000 {
		t.Fatalf("one extreme observation poisoned EWMA: %d", got)
	}
	for i := 0; i < MaximumRoutingPerformanceRecords+20; i++ {
		requirements := Requirements{Task: "generation", Provider: "ollama", Model: string(rune('a' + i))}
		recordRoutingPerformance(&node, requirements, "", uint64(i+1), now.Add(time.Duration(i+10)*time.Second), "")
	}
	if len(node.RoutingPerformance) != MaximumRoutingPerformanceRecords {
		t.Fatalf("routing performance records are unbounded: %d", len(node.RoutingPerformance))
	}
}

func TestWorkerHeartbeatCannotForgeRoutingPerformance(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model"}
	existing := Node{ID: "node"}
	for i := 0; i < 3; i++ {
		recordRoutingPerformance(&existing, requirements, "", 40000, now.Add(time.Duration(i)*time.Second), "idle:cold")
	}
	incoming := Node{ID: "node", RoutingPerformance: []RoutingPerformance{{
		RouteKey: "forged", Samples: math.MaxUint32, EWMAComputeMS: 1, RecentSuccessMS: []uint64{1},
		LoadProfiles: []RoutingLoadPerformance{{ContextClass: "idle:cold", Samples: math.MaxUint32, EWMAComputeMS: 1, RecentSuccessMS: []uint64{1}}},
	}}}
	mergeStoredNodeState(&incoming, existing)
	if len(incoming.RoutingPerformance) != 1 || incoming.RoutingPerformance[0].EWMAComputeMS == 1 || incoming.RoutingPerformance[0].RecentSuccessMS[0] == 1 || len(incoming.RoutingPerformance[0].LoadProfiles) != 1 || incoming.RoutingPerformance[0].LoadProfiles[0].EWMAComputeMS == 1 || incoming.RoutingPerformance[0].LoadProfiles[0].RecentSuccessMS[0] == 1 {
		t.Fatalf("worker forged relay-owned performance: %#v", incoming.RoutingPerformance)
	}
}

func TestBoundedPerformanceProfilesRejectUnknownAndDuplicateClasses(t *testing.T) {
	records := []RoutingPerformance{{RouteKey: "route", RecentSuccessMS: []uint64{1000, 1100}, LoadProfiles: []RoutingLoadPerformance{
		{ContextClass: "idle:cold", Samples: 3, EWMAComputeMS: 1000, RecentSuccessMS: []uint64{1000, 1100}},
		{ContextClass: "attacker-controlled", Samples: math.MaxUint32, EWMAComputeMS: 1},
		{ContextClass: "idle:cold", Samples: math.MaxUint32, EWMAComputeMS: 1},
	}}}
	bounded := boundedRoutingPerformance(records)
	if len(bounded) != 1 || len(bounded[0].LoadProfiles) != 1 || bounded[0].LoadProfiles[0].EWMAComputeMS != 1000 {
		t.Fatalf("profile boundary accepted unknown or duplicate classes: %#v", bounded)
	}
	bounded[0].LoadProfiles[0].EWMAComputeMS = 2
	bounded[0].RecentSuccessMS[0] = 2
	bounded[0].LoadProfiles[0].RecentSuccessMS[0] = 2
	if records[0].LoadProfiles[0].EWMAComputeMS != 1000 {
		t.Fatal("bounded profile retained an alias into caller state")
	}
	if records[0].RecentSuccessMS[0] != 1000 || records[0].LoadProfiles[0].RecentSuccessMS[0] != 1000 {
		t.Fatal("bounded duration history retained an alias into caller state")
	}
}

func TestRoutingObservedComputeTimeUsesRelayTimestamps(t *testing.T) {
	started := time.Now().UTC()
	job := Job{StartedAt: started, FinishedAt: started.Add(1400 * time.Millisecond), Usage: Usage{ComputeMS: 1}}
	if got := routingObservedComputeMS(job); got != 1400 {
		t.Fatalf("routing observation trusted worker accounting instead of relay time: %d", got)
	}
	job.StartedAt = time.Time{}
	if got := routingObservedComputeMS(job); got != 0 {
		t.Fatalf("missing relay start evidence produced an estimate: %d", got)
	}
}

func TestJobPerformanceContextRequiresSelectedRelayEvidence(t *testing.T) {
	job := Job{AssignedNode: "node-a", RoutingDecision: &RoutingDecision{
		SelectedNodeID: "node-a",
		Candidates:     []RoutingCandidateDecision{{NodeID: "node-a", Eligible: true, PerformanceContext: "light:warm"}},
	}}
	if got := jobRoutingPerformanceContext(job); got != "light:warm" {
		t.Fatalf("selected context was not retained: %q", got)
	}
	job.RoutingDecision.Candidates[0].PerformanceContext = "attacker-controlled"
	if got := jobRoutingPerformanceContext(job); got != "" {
		t.Fatalf("invalid context reached performance state: %q", got)
	}
}
