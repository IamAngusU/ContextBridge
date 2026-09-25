package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
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

func TestRoutingHealthNeverEvictsAnActiveRecoveryProbe(t *testing.T) {
	now := time.Now().UTC()
	owner := "producer-a"
	ownerScope := routingHealthOwnerScope(owner)
	node := Node{}
	for i := 0; i < MaximumRoutingHealthRecordsPerOwner; i++ {
		requirements := Requirements{Provider: "ollama", Model: fmt.Sprintf("model-%d", i)}
		routeKey, provider, model := routingHealthKey(requirements)
		health := RoutingHealth{OwnerScope: ownerScope, RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: 1, LastFailureAt: now.Add(time.Duration(i) * time.Second)}
		if i == 0 {
			health.ProbeJobID = "active-probe"
			health.ProbeOwnerScope = ownerScope
		}
		node.RoutingHealth = append(node.RoutingHealth, health)
	}
	recordRoutingOutcomeForOwner(&node, Requirements{Provider: "ollama", Model: "replacement"}, owner, false, FailureWorkerExecution, now.Add(time.Hour))
	if len(node.RoutingHealth) != MaximumRoutingHealthRecordsPerOwner {
		t.Fatalf("unexpected bounded health count: %d", len(node.RoutingHealth))
	}
	for _, health := range node.RoutingHealth {
		if health.ProbeJobID == "active-probe" {
			return
		}
	}
	t.Fatal("bounded health eviction removed an active recovery probe")
}

func TestRoutingHealthKeepsDistinctAdapterRoutesIndependent(t *testing.T) {
	now := time.Now().UTC()
	profileA := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-a", Model: "model-x", Reasoning: "high", SessionID: "session-a", AdapterEndpointID: 1}
	profileB := profileA
	profileB.AdapterProfile = "profile-b"
	profileB.SessionID = "session-b"
	profileB.AdapterEndpointID = 2
	node := Node{}
	for i := uint32(0); i < routingFailureThreshold; i++ {
		recordRoutingOutcomeForOwner(&node, profileA, "producer-a", false, FailureAdapterTimeout, now.Add(time.Duration(i)*time.Second))
	}
	if _, ok := routingHealthForOwnerAt(node, profileA, "producer-a", now.Add(3*time.Second)); !ok {
		t.Fatal("failing adapter route lost its own circuit evidence")
	}
	if health, ok := routingHealthForOwnerAt(node, profileB, "producer-a", now.Add(3*time.Second)); ok {
		t.Fatalf("profile B inherited profile A routing health: %#v", health)
	}
	if len(node.RoutingHealth) != 1 || node.RoutingHealth[0].RouteKey == "" || strings.Contains(node.RoutingHealth[0].RouteKey, "profile-a") || strings.Contains(node.RoutingHealth[0].RouteKey, "session-a") {
		t.Fatalf("route identity was not stored as an opaque fingerprint: %#v", node.RoutingHealth)
	}
}

func TestAdapterAssignmentPreservesAdmittedHealthRouteIdentity(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-a", Model: "model-x", Reasoning: "high", SessionID: "session-a"}
	node := Node{ID: "adapter-node", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Providers: []string{"adapter"}, Tasks: []string{"generation"}, MaxConcurrent: 4, AdapterEndpoints: 1,
		Models:          []ModelCapability{{Name: "model-x", Provider: "adapter", Tasks: []string{"generation"}, Available: true}},
		AdapterSessions: []AdapterSessionCapability{{EndpointID: 42, Profile: "profile-a", State: "waiting", CurrentModel: "model-x", CurrentReasoning: "high"}},
	}}
	if err := store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < routingFailureThreshold; i++ {
		_, decision := rankWithDecisionForOwner([]Node{node}, requirements, 0, "producer-a", now.Add(time.Duration(i)*time.Second))
		job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: "producer-a", MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		job, err = store.AssignAdapterJobWithDecision(job.ID, node.ID, 42, false, decision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteJobWithFailure(job.ID, node.ID, job.Attempt, nil, nil, Usage{}, "timeout", FailureAdapterTimeout); err != nil {
			t.Fatal(err)
		}
		node, err = store.GetNode(node.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, decision := rankWithDecisionForOwner([]Node{node}, requirements, 0, "producer-a", now.Add(4*time.Second))
	if len(decision.Candidates) != 1 || decision.Candidates[0].Eligible || !contains(decision.Candidates[0].RejectionReasons, "route_circuit_open") {
		t.Fatalf("selected endpoint mutation changed the admitted health identity: %#v", decision)
	}
}

func TestRoutingHealthAggregationSelectsOneCoherentRecord(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{RoutingHealth: []RoutingHealth{
		{RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: routingFailureThreshold, LastFailureAt: now.Add(-routingFailureWindow - time.Minute)},
		{OwnerScope: routingHealthOwnerScope("producer-a"), RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: 1, LastFailureCode: FailureAdapterTimeout, LastFailureAt: now},
	}}
	health, ok := routingHealthForOwnerAt(node, requirements, "producer-a", now)
	if !ok {
		t.Fatal("fresh owner-scoped routing evidence was not found")
	}
	if health.ConsecutiveFailures != 1 || !health.LastFailureAt.Equal(now) || health.LastFailureCode != FailureAdapterTimeout {
		t.Fatalf("aggregation synthesized fields from different records: %#v", health)
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

func TestRoutingRecoveryProbationAllowsOnlyOneDurableProbe(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{ID: "node-probe", Connected: true, LastSeen: now, RoutingHealth: []RoutingHealth{{
		RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: routingFailureThreshold,
		LastFailureAt: now.Add(-time.Minute), CircuitOpenUntil: now.Add(-time.Second),
	}}}
	if err := store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketNodes), node.ID, node) }); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: "producer-a", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: "producer-a", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err = store.AssignJob(first.ID, node.ID)
	if err != nil {
		t.Fatalf("first recovery probe was not assigned: %v", err)
	}
	if _, err := store.AssignJob(second.ID, node.ID); !errors.Is(err, ErrRouteProbeInFlight) {
		t.Fatalf("second concurrent probe error = %v, want %v", err, ErrRouteProbeInFlight)
	}
	saved, err := store.GetNode(node.ID)
	if err != nil || len(saved.RoutingHealth) != 1 || saved.RoutingHealth[0].ProbeJobID != first.ID {
		t.Fatalf("durable probe lease missing: %#v, %v", saved.RoutingHealth, err)
	}
	if _, err := store.CompleteJob(first.ID, node.ID, first.Attempt, json.RawMessage(`{"text":"ok"}`), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(second.ID, node.ID); err != nil {
		t.Fatalf("successful probe did not release route: %v", err)
	}
}

func TestCancelledRoutingRecoveryProbeWaitsForExecutionEnd(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{ID: "node-cancelled-probe", Connected: true, LastSeen: now, RoutingHealth: []RoutingHealth{{
		RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: routingFailureThreshold,
		LastFailureAt: now.Add(-time.Minute), CircuitOpenUntil: now.Add(-time.Second),
	}}}
	if err := store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketNodes), node.ID, node) }); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: "producer-a", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: "producer-a", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err = store.AssignJob(first.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(second.ID, node.ID); !errors.Is(err, ErrRouteProbeInFlight) {
		t.Fatalf("cancelled but unacknowledged probe error = %v, want %v", err, ErrRouteProbeInFlight)
	}
	if resolved, err := store.ResolveRoutingRecoveryProbe(node.ID, first.ID, ""); err != nil || !resolved {
		t.Fatalf("execution-ended proof did not release probe: resolved=%v err=%v", resolved, err)
	}
	if _, err := store.AssignJob(second.ID, node.ID); err != nil {
		t.Fatalf("route did not admit a new probe after execution-ended proof: %v", err)
	}
}

func TestOldNodeEvidenceCannotReleaseNewNodeProbeForSameJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routeKey := strings.Repeat("a", 64)
	for _, nodeID := range []string{"node-old", "node-new"} {
		node := Node{ID: nodeID, RoutingHealth: []RoutingHealth{{
			RouteKey: routeKey, ConsecutiveFailures: routingFailureThreshold, CircuitOpenUntil: time.Now().UTC().Add(-time.Second), ProbeJobID: "job-reassigned",
		}}}
		if err := store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketNodes), node.ID, node) }); err != nil {
			t.Fatal(err)
		}
	}
	if resolved, err := store.ResolveRoutingRecoveryProbe("node-old", "job-reassigned", ""); err != nil || !resolved {
		t.Fatalf("old node probe was not released: resolved=%v err=%v", resolved, err)
	}
	oldNode, _ := store.GetNode("node-old")
	newNode, _ := store.GetNode("node-new")
	if oldNode.RoutingHealth[0].ProbeJobID != "" || newNode.RoutingHealth[0].ProbeJobID != "job-reassigned" {
		t.Fatalf("old evidence crossed assignment nodes: old=%#v new=%#v", oldNode.RoutingHealth[0], newNode.RoutingHealth[0])
	}
}

func TestUnrelatedOutcomeCannotReleaseActiveRouteProbe(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{RoutingHealth: []RoutingHealth{{
		RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: routingFailureThreshold,
		LastFailureAt: now.Add(-time.Minute), CircuitOpenUntil: now.Add(-time.Second), ProbeJobID: "probe-job",
	}}}
	recordRoutingOutcomeForOwnerRoute(&node, requirements, "", routeKey, "older-job", true, "", now)
	if len(node.RoutingHealth) != 1 || node.RoutingHealth[0].ProbeJobID != "probe-job" {
		t.Fatalf("unrelated success released the active probe: %#v", node.RoutingHealth)
	}
	recordRoutingOutcomeForOwnerRoute(&node, requirements, "", routeKey, "probe-job", true, "", now)
	if len(node.RoutingHealth) != 0 {
		t.Fatalf("matching probe success did not clear recovered route: %#v", node.RoutingHealth)
	}
}

func TestDisconnectReopensCancelledRoutingRecoveryProbe(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{ID: "node-disconnect-probe", Connected: true, LastSeen: now, RoutingHealth: []RoutingHealth{{
		RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: routingFailureThreshold,
		LastFailureAt: now.Add(-time.Minute), CircuitOpenUntil: now.Add(-time.Second),
	}}}
	if err := store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketNodes), node.ID, node) }); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: "producer-a", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequeueNode(node.ID, "worker disconnected"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetNode(node.ID)
	if err != nil || len(saved.RoutingHealth) != 1 {
		t.Fatalf("routing health missing after disconnect: %#v, %v", saved.RoutingHealth, err)
	}
	health := saved.RoutingHealth[0]
	if health.ProbeJobID != "" || !health.CircuitOpenUntil.After(time.Now().UTC()) || health.LastFailureCode != FailureExecutionStateAmbiguous || health.ConsecutiveFailures != routingFailureThreshold+1 {
		t.Fatalf("disconnect did not release and reopen cancelled probe: %#v", health)
	}
}

func TestRelayRestartReopensRoutingRecoveryProbe(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	owner := "producer-a"
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{ID: "node-restart-probe", Connected: true, LastSeen: now, RoutingHealth: []RoutingHealth{{
		OwnerScope: routingHealthOwnerScope(owner), RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: routingFailureThreshold,
		LastFailureAt: now.Add(-time.Minute), CircuitOpenUntil: now.Add(-time.Second),
	}}}
	if err := store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketNodes), node.ID, node) }); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: owner, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(job.ID, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverRelayRestart("relay restarted"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetNode(node.ID)
	if err != nil || len(saved.RoutingHealth) != 2 {
		t.Fatalf("routing health missing after restart: %#v, %v", saved.RoutingHealth, err)
	}
	seenOwner, seenGlobal := false, false
	for _, health := range saved.RoutingHealth {
		if health.ProbeJobID != "" {
			t.Fatalf("restart did not release probe: %#v", health)
		}
		if health.OwnerScope == routingHealthOwnerScope(owner) && !health.CircuitOpenUntil.After(time.Now().UTC()) {
			t.Fatalf("restart did not reopen owner probe: %#v", health)
		}
		if health.OwnerScope == "" && (health.ConsecutiveFailures != 1 || health.LastFailureCode != FailureExecutionStateAmbiguous) {
			t.Fatalf("restart did not record coherent global failure evidence: %#v", health)
		}
		seenGlobal = seenGlobal || health.OwnerScope == ""
		seenOwner = seenOwner || health.OwnerScope == routingHealthOwnerScope(owner)
	}
	if !seenOwner || !seenGlobal {
		t.Fatalf("restart did not retain owner evidence and add global failure: %#v", saved.RoutingHealth)
	}
}

func TestHistoricalCancellationDoesNotPoisonRouteOnTeardown(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Store, string) error
	}{
		{name: "disconnect", run: func(store *Store, nodeID string) error {
			_, err := store.RequeueNode(nodeID, "worker disconnected")
			return err
		}},
		{name: "restart", run: func(store *Store, _ string) error { _, err := store.RecoverRelayRestart("relay restarted"); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			nodeID := "node-history"
			if err := store.UpsertNode(Node{ID: nodeID, Name: nodeID}); err != nil {
				t.Fatal(err)
			}
			job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}, Payload: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AssignJob(job.ID, nodeID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CancelJob(job.ID); err != nil {
				t.Fatal(err)
			}
			if err := test.run(store, nodeID); err != nil {
				t.Fatal(err)
			}
			node, err := store.GetNode(nodeID)
			if err != nil || len(node.RoutingHealth) != 0 {
				t.Fatalf("historical cancellation created routing failure evidence: %#v, %v", node.RoutingHealth, err)
			}
		})
	}
}

func TestFailedProducerProbeReopensGlobalCircuit(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{ID: "node-global-probe", Connected: true, LastSeen: now, RoutingHealth: []RoutingHealth{{
		RouteKey: routeKey, Provider: provider, Model: model, ConsecutiveFailures: routingFailureThreshold,
		LastFailureAt: now.Add(-time.Minute), CircuitOpenUntil: now.Add(-time.Second),
	}}}
	if err := store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketNodes), node.ID, node) }); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: "producer-a", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJobWithFailure(job.ID, node.ID, job.Attempt, nil, nil, Usage{}, "timeout", FailureAdapterTimeout); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetNode(node.ID)
	if err != nil || len(saved.RoutingHealth) < 1 {
		t.Fatalf("global circuit disappeared: %#v, %v", saved.RoutingHealth, err)
	}
	global := saved.RoutingHealth[0]
	if global.OwnerScope != "" || global.ProbeJobID != "" || !global.CircuitOpenUntil.After(time.Now().UTC()) {
		t.Fatalf("failed probe did not reopen and release global circuit: %#v", global)
	}
}

func TestFailedOwnerProbeKeepsItsCircuitOpen(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	owner := "producer-a"
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "model-a"}
	routeKey, provider, model := routingHealthKey(requirements)
	node := Node{ID: "node-owner-probe", Connected: true, LastSeen: now, RoutingHealth: []RoutingHealth{{
		OwnerScope: routingHealthOwnerScope(owner), RouteKey: routeKey, Provider: provider, Model: model,
		ConsecutiveFailures: routingFailureThreshold, LastFailureAt: now.Add(-routingFailureWindow - time.Minute), CircuitOpenUntil: now.Add(-time.Second),
	}}}
	if err := store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketNodes), node.ID, node) }); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(SubmitRequest{Requirements: requirements, Payload: json.RawMessage(`{}`), OwnerSubject: owner, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJobWithFailure(job.ID, node.ID, job.Attempt, nil, nil, Usage{}, "timeout", FailureAdapterTimeout); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetNode(node.ID)
	if err != nil || len(saved.RoutingHealth) != 1 {
		t.Fatalf("owner circuit disappeared: %#v, %v", saved.RoutingHealth, err)
	}
	health := saved.RoutingHealth[0]
	if health.ProbeJobID != "" || !health.CircuitOpenUntil.After(time.Now().UTC()) || health.ConsecutiveFailures <= routingFailureThreshold {
		t.Fatalf("failed owner probe did not advance its circuit: %#v", health)
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
