package cluster

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestRoutingHealthIsolatesProducerFailuresAndKeepsNodeFailuresGlobal(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	node := Node{ID: "node-a", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Providers: []string{"ollama"}, Tasks: []string{"generation"}, Models: []ModelCapability{{Name: "model-a", Provider: "ollama", Tasks: []string{"generation"}, Available: true}}, MaxConcurrent: 1,
	}}
	for i := uint32(0); i < routingFailureThreshold; i++ {
		recordRoutingOutcomeForOwner(&node, requirements, "producer-a", false, FailureAdapterTimeout, now.Add(time.Duration(i)*time.Second))
	}
	if len(node.RoutingHealth) != 1 || node.RoutingHealth[0].OwnerScope == "" || node.RoutingHealth[0].OwnerScope == "producer-a" {
		t.Fatalf("producer health was not stored under an opaque scope: %#v", node.RoutingHealth)
	}
	_, producerA := rankWithDecisionForOwner([]Node{node}, requirements, 0, "producer-a", now.Add(4*time.Second))
	if producerA.Candidates[0].Eligible || !contains(producerA.Candidates[0].RejectionReasons, "route_circuit_open") {
		t.Fatalf("producer A circuit did not open: %#v", producerA)
	}
	_, producerB := rankWithDecisionForOwner([]Node{node}, requirements, 0, "producer-b", now.Add(4*time.Second))
	if !producerB.Candidates[0].Eligible || producerB.Candidates[0].ScoreComponents.RecentFailures != 0 {
		t.Fatalf("producer A poisoned producer B routing: %#v", producerB)
	}

	for i := uint32(0); i < routingFailureThreshold; i++ {
		recordRoutingOutcome(&node, requirements, false, FailureExecutionStateAmbiguous, now.Add(time.Duration(i+5)*time.Second))
	}
	_, producerB = rankWithDecisionForOwner([]Node{node}, requirements, 0, "producer-b", now.Add(9*time.Second))
	if producerB.Candidates[0].Eligible || !contains(producerB.Candidates[0].RejectionReasons, "route_circuit_open") {
		t.Fatalf("node-wide failure did not affect every producer: %#v", producerB)
	}
	recordRoutingOutcomeForOwner(&node, requirements, "producer-b", true, "", now.Add(10*time.Second))
	_, producerB = rankWithDecisionForOwner([]Node{node}, requirements, 0, "producer-b", now.Add(11*time.Second))
	if !producerB.Candidates[0].Eligible {
		t.Fatalf("successful producer B probe did not clear global evidence: %#v", producerB)
	}
	_, producerA = rankWithDecisionForOwner([]Node{node}, requirements, 0, "producer-a", now.Add(11*time.Second))
	if producerA.Candidates[0].Eligible {
		t.Fatalf("producer B success incorrectly cleared producer A evidence: %#v", producerA)
	}
}

func TestRoutingHealthBoundsOneProducerWithoutEvictingOtherScopes(t *testing.T) {
	now := time.Now().UTC()
	node := Node{}
	recordRoutingOutcomeForOwner(&node, Requirements{Provider: "ollama", Model: "owner-b"}, "producer-b", false, FailureWorkerExecution, now)
	for i := 0; i < MaximumRoutingHealthRecordsPerOwner+5; i++ {
		recordRoutingOutcomeForOwner(&node, Requirements{Provider: "ollama", Model: fmt.Sprintf("owner-a-%d", i)}, "producer-a", false, FailureWorkerExecution, now.Add(time.Duration(i+1)*time.Second))
	}
	ownerA, ownerB := 0, 0
	for _, health := range node.RoutingHealth {
		switch health.OwnerScope {
		case routingHealthOwnerScope("producer-a"):
			ownerA++
		case routingHealthOwnerScope("producer-b"):
			ownerB++
		}
	}
	if ownerA != MaximumRoutingHealthRecordsPerOwner || ownerB != 1 {
		t.Fatalf("per-owner bounds crossed another producer scope: A=%d B=%d records=%#v", ownerA, ownerB, node.RoutingHealth)
	}
	for i := 0; i < MaximumRoutingHealthRecords*2; i++ {
		recordRoutingOutcomeForOwner(&node, Requirements{Provider: "adapter", Model: fmt.Sprintf("route-%d", i)}, fmt.Sprintf("producer-%d", i+100), false, FailureAdapterTimeout, now.Add(time.Duration(i+100)*time.Second))
	}
	if len(node.RoutingHealth) > MaximumRoutingHealthRecords {
		t.Fatalf("routing health exceeded global bound: %d", len(node.RoutingHealth))
	}
	if _, ok := routingHealthForOwner(node, Requirements{Provider: "ollama", Model: "owner-b"}, "producer-b"); !ok {
		t.Fatal("new producer scopes evicted existing producer health")
	}
}

func TestRoutingCircuitOpensPenalizesAndRecovers(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	node := Node{ID: "node-a", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Providers: []string{"ollama"}, Tasks: []string{"generation"}, Models: []ModelCapability{{Name: "model-a", Provider: "ollama", Tasks: []string{"generation"}, Available: true}}, MaxConcurrent: 1,
	}}
	recordRoutingOutcome(&node, requirements, false, FailureAdapterTimeout, now)
	_, decision := rankWithDecision([]Node{node}, requirements, 0, now)
	if len(decision.Candidates) != 1 || !decision.Candidates[0].Eligible || decision.Candidates[0].ScoreComponents.RecentFailures != routingFailureScore {
		t.Fatalf("one recent failure was not a bounded soft penalty: %#v", decision)
	}
	recordRoutingOutcome(&node, requirements, false, FailureAdapterRateLimited, now.Add(time.Second))
	recordRoutingOutcome(&node, requirements, false, FailureAdapterAutomation, now.Add(2*time.Second))
	_, decision = rankWithDecision([]Node{node}, requirements, 0, now.Add(3*time.Second))
	if len(decision.Candidates) != 1 || decision.Candidates[0].Eligible || !contains(decision.Candidates[0].RejectionReasons, "route_circuit_open") || decision.Candidates[0].FailureStreak != routingFailureThreshold {
		t.Fatalf("open circuit did not reject matching route with evidence: %#v", decision)
	}
	node.LastSeen = now.Add(routingInitialCooldown + 3*time.Second)
	_, decision = rankWithDecision([]Node{node}, requirements, 0, node.LastSeen)
	if !decision.Candidates[0].Eligible || decision.Candidates[0].ScoreComponents.RecentFailures == 0 {
		t.Fatalf("half-open route was not admitted with a recovery penalty: %#v", decision)
	}
	recordRoutingOutcome(&node, requirements, true, "", now.Add(routingInitialCooldown+4*time.Second))
	if len(node.RoutingHealth) != 0 {
		t.Fatalf("successful probe did not close and clear circuit: %#v", node.RoutingHealth)
	}
}

func TestRoutingHealthIgnoresNonInfrastructureFailureAndExpiresPenalty(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Provider: "ollama", Model: "model-a"}
	node := Node{}
	recordRoutingOutcome(&node, requirements, false, FailureCostBudgetExceeded, now)
	if len(node.RoutingHealth) != 0 {
		t.Fatalf("producer policy failure poisoned routing: %#v", node.RoutingHealth)
	}
	recordRoutingOutcome(&node, requirements, false, FailureWorkerExecution, now)
	node.Connected = true
	node.LastSeen = now.Add(routingFailureWindow + time.Second)
	node.Capabilities = Capabilities{Providers: []string{"ollama"}, Tasks: []string{"generation"}, Models: []ModelCapability{{Name: "model-a", Provider: "ollama", Tasks: []string{"generation"}, Available: true}}, MaxConcurrent: 1}
	_, decision := rankWithDecision([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}, 0, node.LastSeen)
	if !decision.Candidates[0].Eligible || decision.Candidates[0].ScoreComponents.RecentFailures != 0 {
		t.Fatalf("expired failure still changed placement: %#v", decision)
	}
}

func TestRoutingHealthIsDurableRelayOwnedAndAtomicWithCompletion(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	node := Node{ID: "node-a", Name: "Node A", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{MaxConcurrent: 1}}
	if err := store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < int(routingFailureThreshold); i++ {
		job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		job, err = store.AssignJob(job.ID, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteJobWithFailure(job.ID, node.ID, job.Attempt, nil, nil, Usage{}, "adapter timed out", FailureAdapterTimeout); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := store.GetNode(node.ID)
	if err != nil || len(saved.RoutingHealth) != 1 || saved.RoutingHealth[0].ConsecutiveFailures != routingFailureThreshold || !saved.RoutingHealth[0].CircuitOpenUntil.After(time.Now()) {
		t.Fatalf("terminal results and routing health diverged: %#v, %v", saved, err)
	}
	forged := node
	forged.RoutingHealth = []RoutingHealth{{Provider: "forged", Model: "forged", ConsecutiveFailures: 999}}
	if err := store.UpsertNode(forged); err != nil {
		t.Fatal(err)
	}
	saved, _ = store.GetNode(node.ID)
	if len(saved.RoutingHealth) != 1 || saved.RoutingHealth[0].Provider != "ollama" {
		t.Fatalf("worker heartbeat forged or cleared relay health: %#v", saved.RoutingHealth)
	}
	job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJob(job.ID, node.ID, job.Attempt, json.RawMessage(`{"text":"ok"}`), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	saved, _ = store.GetNode(node.ID)
	if len(saved.RoutingHealth) != 0 {
		t.Fatalf("successful route did not clear durable health: %#v", saved.RoutingHealth)
	}
}

func TestWorkerDisconnectFeedsRoutingCircuitWithoutReplayingJobs(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	node := Node{ID: "node-disconnect", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{MaxConcurrent: 4}}
	if err := store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	jobIDs := make([]string, 0, routingFailureThreshold)
	for i := uint32(0); i < routingFailureThreshold; i++ {
		job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), MaxAttempts: 3})
		if err != nil {
			t.Fatal(err)
		}
		job, err = store.AssignJob(job.ID, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, job.ID)
	}
	failed, err := store.RequeueNode(node.ID, "worker connection lost")
	if err != nil || len(failed) != int(routingFailureThreshold) {
		t.Fatalf("disconnect result = %#v, %v", failed, err)
	}
	for _, id := range jobIDs {
		job, err := store.GetJob(id)
		if err != nil || job.Status != JobFailed || job.FailureCode != FailureExecutionStateAmbiguous {
			t.Fatalf("disconnect silently replayed or lost ambiguity for %s: %#v, %v", id, job, err)
		}
	}
	saved, _ := store.GetNode(node.ID)
	if len(saved.RoutingHealth) != 1 || !saved.RoutingHealth[0].CircuitOpenUntil.After(time.Now()) {
		t.Fatalf("disconnect did not open matching routing circuit: %#v", saved.RoutingHealth)
	}
}
