package cluster

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExplainRoutingMatchesSchedulerAndUsesStableReasons(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", PreferredNodes: []string{"ready"}}
	model := ModelCapability{Name: "text", Provider: "ollama", Tasks: []string{"generation"}, Available: true, Loaded: true, CapabilitiesVerified: true}
	nodes := []Node{
		{ID: "ready", Name: "Ready", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Providers: []string{"ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, Models: []ModelCapability{model},
			MaxConcurrent: 2, MemoryTotal: 16 << 30, MemoryFree: 12 << 30,
		}},
		{ID: "wrong-provider", Connected: true, LastSeen: now, Capabilities: Capabilities{Providers: []string{"adapter"}, Tasks: []string{"generation"}, MaxConcurrent: 2}},
		{ID: "stale", Connected: true, LastSeen: now.Add(-2 * NodeFreshnessWindow), Capabilities: Capabilities{
			Providers: []string{"ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, Models: []ModelCapability{model}, MaxConcurrent: 2,
		}},
		{ID: "busy", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Providers: []string{"ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, Models: []ModelCapability{model}, MaxConcurrent: 1, Running: 1,
		}},
		{ID: "draining", Connected: true, Draining: true, LastSeen: now, Capabilities: Capabilities{
			Providers: []string{"ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, Models: []ModelCapability{model}, MaxConcurrent: 2,
		}},
	}

	ranked := RankWithEstimate(nodes, requirements, 0)
	decision := ExplainRouting(nodes, requirements, 0)
	if len(ranked) != 1 || decision.SelectedNodeID != ranked[0].Node.ID || decision.SelectedNodeID != "ready" {
		t.Fatalf("explanation diverged from scheduler: ranked=%#v decision=%#v", ranked, decision)
	}
	byID := map[string]RoutingCandidateDecision{}
	for _, candidate := range decision.Candidates {
		byID[candidate.NodeID] = candidate
	}
	if !contains(byID["wrong-provider"].RejectionReasons, "provider_not_available") {
		t.Fatalf("provider reason missing: %#v", byID["wrong-provider"])
	}
	if !contains(byID["stale"].RejectionReasons, "worker_telemetry_stale") {
		t.Fatalf("stale reason missing: %#v", byID["stale"])
	}
	if !contains(byID["busy"].RejectionReasons, "worker_at_capacity") {
		t.Fatalf("capacity reason missing: %#v", byID["busy"])
	}
	if !contains(byID["draining"].RejectionReasons, "worker_draining") {
		t.Fatalf("drain reason missing: %#v", byID["draining"])
	}
	selected := byID["ready"]
	components := selected.ScoreComponents
	sum := components.ActiveLoad + components.QueueDepth + components.MemoryPressure + components.CPUPressure +
		components.GPUPressure + components.VRAMHeadroom + components.AdapterPressure + components.LoadedModel +
		components.EstimatedVRAMFit + components.PreferredNode + components.RecentFailures
	if math.Abs(sum-selected.Score) > 0.000001 {
		t.Fatalf("score components do not sum to score: sum=%v score=%v components=%#v", sum, selected.Score, components)
	}
}

func TestRouteExplainEndpointScopesProducerAndDoesNotSubmit(t *testing.T) {
	const admin = "admin-token-with-enough-entropy-000000000000"
	const producer = "producer-token-with-enough-entropy-000000000"
	relay, err := NewRelay(RelayConfig{Database: t.TempDir() + "/relay.db", AdminToken: admin, AllowedTasks: []string{"generation"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if err := relay.store.EnsureToken(producer, "producer", "producer-a", []string{"blue"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, node := range []Node{
		{ID: "blue-node", Name: "Blue", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, Groups: []string{"blue"}, MaxConcurrent: 1}},
		{ID: "red-node", Name: "Red", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, Groups: []string{"red"}, MaxConcurrent: 1}},
	} {
		if err := relay.store.UpsertNode(node); err != nil {
			t.Fatal(err)
		}
	}
	relay.workers["blue-node"] = newWorkerConnection(nil, 1)
	relay.workers["red-node"] = newWorkerConnection(nil, 1)
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	var decision RoutingDecision
	postTest(t, server.URL+"/v1/cluster/routes/explain", producer, AssignmentRequest{Requirements: Requirements{Task: "generation"}}, &decision)
	if !decision.Preview || decision.JobID != "" || decision.Requirements.Group != "blue" || decision.SelectedNodeID != "blue-node" {
		t.Fatalf("producer-scoped preview is wrong: %#v", decision)
	}
	if decision.PolicyDecision == nil || decision.PolicyDecision.Schema != PolicyDecisionV1 || decision.PolicyDecision.Outcome != "allow" || len(decision.PolicyDecision.ReasonCodes) != 1 || decision.PolicyDecision.ReasonCodes[0] != PolicyCodeDisabled {
		t.Fatalf("route preview lost the authoritative policy decision: %#v", decision.PolicyDecision)
	}
	if count, err := relay.store.CountJobs(""); err != nil || count != 0 {
		t.Fatalf("route preview submitted a job: count=%d err=%v", count, err)
	}
}

func TestRouteExplainDoesNotShareProducerCircuitState(t *testing.T) {
	const admin = "admin-token-with-enough-entropy-000000000000"
	const tokenA = "producer-a-token-with-enough-entropy-000000"
	const tokenB = "producer-b-token-with-enough-entropy-000000"
	relay, err := NewRelay(RelayConfig{Database: t.TempDir() + "/relay.db", AdminToken: admin, AllowedTasks: []string{"generation"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if err := relay.store.EnsureToken(tokenA, "producer", "producer-a", nil); err != nil {
		t.Fatal(err)
	}
	if err := relay.store.EnsureToken(tokenB, "producer", "producer-b", nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	node := Node{ID: "shared-node", Name: "Shared", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Providers: []string{"ollama"}, Tasks: []string{"generation"}, Models: []ModelCapability{{Name: "model-a", Provider: "ollama", Tasks: []string{"generation"}, Available: true}}, MaxConcurrent: 4,
	}}
	if err := relay.store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	relay.workers[node.ID] = newWorkerConnection(nil, 4)
	for i := uint32(0); i < routingFailureThreshold; i++ {
		job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		job, err = relay.store.AssignJob(job.ID, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := relay.store.CompleteJobWithFailure(job.ID, node.ID, job.Attempt, nil, nil, Usage{}, "provider timed out", FailureAdapterTimeout); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	var decisionA RoutingDecision
	postTest(t, server.URL+"/v1/cluster/routes/explain", tokenA, AssignmentRequest{Requirements: requirements}, &decisionA)
	if len(decisionA.Candidates) != 1 || decisionA.Candidates[0].Eligible || !contains(decisionA.Candidates[0].RejectionReasons, "route_circuit_open") {
		t.Fatalf("producer A preview ignored its open circuit: %#v", decisionA)
	}
	var decisionB RoutingDecision
	postTest(t, server.URL+"/v1/cluster/routes/explain", tokenB, AssignmentRequest{Requirements: requirements}, &decisionB)
	if decisionB.SelectedNodeID != node.ID || len(decisionB.Candidates) != 1 || !decisionB.Candidates[0].Eligible || decisionB.Candidates[0].FailureStreak != 0 {
		t.Fatalf("producer A circuit leaked into producer B preview: %#v", decisionB)
	}
}

func TestDurableRouteEndpointEnforcesJobStateAndOwnership(t *testing.T) {
	const admin = "admin-token-with-enough-entropy-000000000000"
	const producerA = "producer-a-token-with-enough-entropy-000000"
	const producerB = "producer-b-token-with-enough-entropy-000000"
	relay, err := NewRelay(RelayConfig{Database: t.TempDir() + "/relay.db", AdminToken: admin, AllowedTasks: []string{"generation"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if err := relay.store.EnsureToken(producerA, "producer", "producer-a", nil); err != nil {
		t.Fatal(err)
	}
	if err := relay.store.EnsureToken(producerB, "producer", "producer-b", nil); err != nil {
		t.Fatal(err)
	}
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	target := server.URL + "/v1/cluster/jobs/" + job.ID + "/route"
	if status := authenticatedGETStatus(t, target, producerA, nil); status != http.StatusConflict {
		t.Fatalf("queued route status = %d, want %d", status, http.StatusConflict)
	}
	decision := RoutingDecision{SelectedNodeID: "node-a", Candidates: []RoutingCandidateDecision{{NodeID: "node-a", Eligible: true}}}
	if _, err := relay.store.AssignJobWithDecision(job.ID, "node-a", decision); err != nil {
		t.Fatal(err)
	}
	var stored RoutingDecision
	if status := authenticatedGETStatus(t, target, producerA, &stored); status != http.StatusOK || stored.JobID != job.ID || stored.SelectedNodeID != "node-a" {
		t.Fatalf("owner route response = status %d decision %#v", status, stored)
	}
	if status := authenticatedGETStatus(t, target, producerB, nil); status != http.StatusForbidden {
		t.Fatalf("other producer route status = %d, want %d", status, http.StatusForbidden)
	}
}

func authenticatedGETStatus(t *testing.T, target, token string, output interface{}) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if output != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestExplainRoutingBoundsPersistedCandidateEvidence(t *testing.T) {
	now := time.Now().UTC()
	nodes := make([]Node, MaximumRoutingDecisionCandidates+12)
	for index := range nodes {
		nodes[index] = Node{ID: "node-" + strings.Repeat("x", index%3) + string(rune('a'+index%26)), Connected: true, LastSeen: now,
			Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 1}}
	}
	decision := ExplainRouting(nodes, Requirements{Task: "generation"}, 0)
	if decision.CandidateCount != len(nodes) || len(decision.Candidates) != MaximumRoutingDecisionCandidates || decision.CandidatesTruncated != 12 {
		t.Fatalf("routing evidence was not bounded: %#v", decision)
	}
}

func TestRoutingNodesUseLiveConnectionsAsAuthority(t *testing.T) {
	const admin = "admin-token-with-enough-entropy-000000000000"
	relay, err := NewRelay(RelayConfig{Database: t.TempDir() + "/relay.db", AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	node := Node{ID: "node-a", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{MaxConcurrent: 4, Running: 3}}
	if err := relay.store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	nodes, err := relay.routingNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Connected {
		t.Fatalf("stored connected state survived without a live relay connection: %#v", nodes)
	}
	worker := newWorkerConnection(nil, 2)
	if !worker.reserve("job-a") {
		t.Fatal("could not create live worker load")
	}
	relay.workers[node.ID] = worker
	nodes, err = relay.routingNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !nodes[0].Connected || nodes[0].Capabilities.Running != 1 || nodes[0].Capabilities.MaxConcurrent != 2 {
		t.Fatalf("live relay load did not override stored telemetry: %#v", nodes)
	}
}

func TestRoutingDecisionPersistsAtomicallyWithAssignment(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requirements := Requirements{Task: "generation"}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{"prompt":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	node := Node{ID: "node-a", Name: "Node A", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 1}}
	decision := ExplainRouting([]Node{node}, requirements, 0)
	assigned, err := store.AssignJobWithDecision(job.ID, node.ID, decision)
	if err != nil {
		t.Fatal(err)
	}
	if assigned.RoutingDecision == nil || assigned.RoutingDecision.ID == "" || assigned.RoutingDecision.JobID != job.ID || assigned.RoutingDecision.SelectedNodeID != node.ID {
		t.Fatalf("assignment lost routing decision: %#v", assigned.RoutingDecision)
	}
	stored, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RoutingDecision == nil || stored.RoutingDecision.ID != assigned.RoutingDecision.ID {
		t.Fatalf("routing decision was not durable: %#v", stored.RoutingDecision)
	}
}

func TestRoutingDecisionCannotBeAttachedToAnotherNode(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	decision := RoutingDecision{SelectedNodeID: "node-a", Requirements: job.Requirements}
	if _, err := store.AssignJobWithDecision(job.ID, "node-b", decision); err == nil || !strings.Contains(err.Error(), "another node") {
		t.Fatalf("mismatched route decision was accepted: %v", err)
	}
	stored, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobQueued || stored.RoutingDecision != nil {
		t.Fatalf("failed assignment mutated the job: %#v", stored)
	}
}

func TestRoutingDecisionRequiresSelectedEligibleCandidate(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	decision := RoutingDecision{
		SelectedNodeID: "node-a",
		Candidates:     []RoutingCandidateDecision{{NodeID: "node-a", Eligible: false, RejectionReasons: []string{"worker_at_capacity"}}},
	}
	if _, err := store.AssignJobWithDecision(job.ID, "node-a", decision); err == nil || !strings.Contains(err.Error(), "selected eligible node") {
		t.Fatalf("ineligible selected candidate was accepted: %v", err)
	}
	stored, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobQueued || stored.RoutingDecision != nil {
		t.Fatalf("failed evidence validation mutated the job: %#v", stored)
	}
}

func TestApplyRoutingNodeConstraintsExplainsAffinity(t *testing.T) {
	decision := RoutingDecision{Candidates: []RoutingCandidateDecision{
		{NodeID: "node-a", Eligible: true},
		{NodeID: "node-b", Eligible: true},
	}}
	applyRoutingNodeConstraints(&decision, "node-b", "")
	byID := map[string]RoutingCandidateDecision{}
	for _, candidate := range decision.Candidates {
		byID[candidate.NodeID] = candidate
	}
	if decision.SelectedNodeID != "node-b" || byID["node-a"].Eligible || !contains(byID["node-a"].RejectionReasons, "session_affinity_node_mismatch") {
		t.Fatalf("session affinity was not represented in the decision: %#v", decision)
	}
}

func TestRoutingConstraintSelectionSurvivesCandidateBound(t *testing.T) {
	now := time.Now().UTC()
	nodes := make([]Node, MaximumRoutingDecisionCandidates+1)
	for index := range nodes {
		nodes[index] = Node{
			ID:        "node-" + strings.Repeat("z", index/26) + string(rune('a'+index%26)),
			Connected: true,
			LastSeen:  now,
			Capabilities: Capabilities{
				Tasks: []string{"generation"}, MaxConcurrent: 1,
			},
		}
	}
	required := nodes[len(nodes)-1].ID
	_, decision := rankWithDecision(nodes, Requirements{Task: "generation"}, 0, now)
	applyRoutingNodeConstraints(&decision, required, "")
	boundRoutingDecision(&decision)
	if decision.SelectedNodeID != required || len(decision.Candidates) != MaximumRoutingDecisionCandidates || decision.Candidates[0].NodeID != required || !decision.Candidates[0].Eligible {
		t.Fatalf("hard-constrained selection was lost during bounding: required=%s decision=%#v", required, decision)
	}
}

func TestRejectRoutingCandidatePromotesNextChoice(t *testing.T) {
	decision := RoutingDecision{Candidates: []RoutingCandidateDecision{
		{NodeID: "node-a", Eligible: true, Score: 1},
		{NodeID: "node-b", Eligible: true, Score: 2},
	}}
	rejectRoutingCandidate(&decision, "node-a", "worker_at_capacity")
	if decision.SelectedNodeID != "node-b" || decision.Candidates[0].NodeID != "node-b" || !contains(decision.Candidates[1].RejectionReasons, "worker_at_capacity") {
		t.Fatalf("admission race was not reflected in route evidence: %#v", decision)
	}
}

func TestRoutingReasonCodesRemainJSONSafe(t *testing.T) {
	decision := ExplainRouting([]Node{{ID: "offline", LastSeen: time.Now().UTC()}}, Requirements{Task: "generation"}, 0)
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"worker_not_connected"`) {
		t.Fatalf("stable reason code missing from JSON: %s", raw)
	}
}
