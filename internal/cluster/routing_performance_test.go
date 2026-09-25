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
		recordRoutingPerformance(&fast, requirements, "", 1400, now.Add(time.Duration(i)*time.Minute))
		recordRoutingPerformance(&slow, requirements, "", 40000, now.Add(time.Duration(i)*time.Minute))
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

func TestPerformanceLearningRequiresFreshBoundedEvidence(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "same"}
	node := Node{ID: "node", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, MaxConcurrent: 1,
	}}
	policy := DefaultPlacementPolicy()
	for i := 0; i < 2; i++ {
		recordRoutingPerformance(&node, requirements, "", 1000, now.Add(time.Duration(i)*time.Second))
	}
	if _, ok := routingPerformanceFor(node, requirements, policy, now.Add(time.Minute)); ok {
		t.Fatal("insufficient samples became routing authority")
	}
	recordRoutingPerformance(&node, requirements, "", 1000, now.Add(2*time.Second))
	if _, ok := routingPerformanceFor(node, requirements, policy, now.Add(8*24*time.Hour)); ok {
		t.Fatal("stale performance evidence did not expire")
	}

	disabled := policy
	disabled.PerformanceLearning = false
	if _, ok := routingPerformanceFor(node, requirements, disabled, now.Add(time.Minute)); ok {
		t.Fatal("disabled performance learning still affected routing")
	}
}

func TestPerformanceEWMAIsBoundedAndRecordsAreCapped(t *testing.T) {
	now := time.Now().UTC()
	node := Node{}
	base := Requirements{Task: "generation", Provider: "ollama", Model: "base"}
	for i := 0; i < 3; i++ {
		recordRoutingPerformance(&node, base, "", 1000, now.Add(time.Duration(i)*time.Second))
	}
	recordRoutingPerformance(&node, base, "", math.MaxUint64, now.Add(4*time.Second))
	if got := node.RoutingPerformance[0].EWMAComputeMS; got > 2000 {
		t.Fatalf("one extreme observation poisoned EWMA: %d", got)
	}
	for i := 0; i < MaximumRoutingPerformanceRecords+20; i++ {
		requirements := Requirements{Task: "generation", Provider: "ollama", Model: string(rune('a' + i))}
		recordRoutingPerformance(&node, requirements, "", uint64(i+1), now.Add(time.Duration(i+10)*time.Second))
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
		recordRoutingPerformance(&existing, requirements, "", 40000, now.Add(time.Duration(i)*time.Second))
	}
	incoming := Node{ID: "node", RoutingPerformance: []RoutingPerformance{{RouteKey: "forged", Samples: math.MaxUint32, EWMAComputeMS: 1}}}
	mergeStoredNodeState(&incoming, existing)
	if len(incoming.RoutingPerformance) != 1 || incoming.RoutingPerformance[0].EWMAComputeMS == 1 {
		t.Fatalf("worker forged relay-owned performance: %#v", incoming.RoutingPerformance)
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
