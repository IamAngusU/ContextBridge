package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	bolt "go.etcd.io/bbolt"
)

func capacityPolicy(borrow bool) InteractiveCapacity {
	return InteractiveCapacity{MinPriority: 80, Scopes: []InteractiveCapacityScope{{Group: "voice", Task: "generation", Slots: 1, BorrowIdle: borrow}}}
}

func TestInteractiveCapacityRelayDispatchBorrowAndReclaim(t *testing.T) {
	for _, borrow := range []bool{false, true} {
		t.Run(fmt.Sprintf("borrow=%t", borrow), func(t *testing.T) {
			const admin = "admin_012345678901234567890123456789012345"
			relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}, InteractiveCapacity: capacityPolicy(borrow)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer relay.Close()
			server := httptest.NewServer(relay.Handler())
			defer server.Close()
			_, publicKey, err := NewIdentity()
			if err != nil {
				t.Fatal(err)
			}
			node := capacityNode(0)
			node.PublicKey = publicKey
			node.Name = "Reserve worker"
			node.Capabilities.Providers = []string{"ollama"}
			node.Capabilities.Models = []ModelCapability{{Name: "test-model", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}}
			token, _, err := relay.store.CreateToken("node", node.ID, []string{"voice"}, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			connection := dialTestWorker(t, server.URL, token)
			defer connection.CloseNow()
			if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); err != nil {
				t.Fatal(err)
			}
			readTestAuthority(t, connection)
			waitFor(t, 2*time.Second, func() bool { nodes, _ := relay.routingNodes(); return len(nodes) == 1 && nodes[0].Connected }, "worker did not connect")
			submit := func(priority int) Job {
				var response Job
				postTest(t, server.URL+"/v1/cluster/jobs", admin, SubmitRequest{Requirements: Requirements{Group: "voice", Task: "generation", Provider: "ollama", Model: "test-model"}, Priority: priority, Payload: json.RawMessage(`{}`)}, &response)
				return response
			}
			readJob := func() Job {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, raw, err := connection.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var message WireMessage
				if err := json.Unmarshal(raw, &message); err != nil || message.Job == nil {
					t.Fatalf("expected job: %s (%v)", raw, err)
				}
				return *message.Job
			}
			first, second := submit(0), submit(0)
			relay.dispatch()
			dispatched := readJob()
			if dispatched.ID != first.ID {
				t.Fatal("batch FIFO changed")
			}
			if borrow {
				if j := readJob(); j.ID != second.ID {
					t.Fatal("idle reserve not borrowed")
				}
			}
			interactive := submit(80)
			relay.dispatch()

			if borrow {
				stored, _ := relay.store.GetJob(interactive.ID)
				if stored.Status != JobQueued {
					t.Fatal("running borrower preempted")
				}
				// A fenced worker result proves execution ended and releases the
				// borrowed slot through the real wire lifecycle.
				if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "result", JobID: dispatched.ID, Attempt: dispatched.Attempt, Fence: dispatched.AssignmentFence, Result: json.RawMessage(`{"text":"done"}`)})); err != nil {
					t.Fatal(err)
				}
				waitFor(t, 2*time.Second, func() bool { stored, _ := relay.store.GetJob(first.ID); return stored.Status == JobCompleted }, "borrower result did not finish")
				third := submit(0)
				relay.dispatch()
				j := readJob()
				if j.ID != interactive.ID || j.RoutingDecision == nil || j.RoutingDecision.Candidates[0].ReservationOutcome != "interactive" {
					t.Fatal("released borrowed slot did not go to interactive work")
				}
				stored, _ = relay.store.GetJob(third.ID)
				if stored.Status != JobQueued {
					t.Fatal("batch stole reclaimed slot")
				}
			} else {
				j := readJob()
				if j.ID != interactive.ID {
					t.Fatal("protected slot did not serve interactive job")
				}
				stored, _ := relay.store.GetJob(second.ID)
				if stored.Status != JobQueued {
					t.Fatal("batch consumed protected slot")
				}
			}
		})
	}
}

func capacityNode(running int) Node {
	return Node{ID: "worker", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{Groups: []string{"voice"}, Tasks: []string{"generation"}, MaxConcurrent: 2, Running: running}}
}

func TestInteractiveCapacitySaturationBorrowAndReclaim(t *testing.T) {
	requirements := Requirements{Group: "voice", Task: "generation"}
	for _, tc := range []struct {
		name                           string
		borrow, pending, unknown       bool
		priority, running, interactive int
		eligible                       bool
		outcome                        string
	}{
		{"protected batch", false, false, false, 0, 1, 0, false, "protected"},
		{"interactive headroom", false, false, false, 80, 1, 0, true, "interactive"},
		{"no oversubscription", true, false, false, 80, 2, 0, false, "interactive"},
		{"idle borrowing", true, false, false, 0, 1, 0, true, "borrow_idle"},
		{"reclaim released slot", true, true, false, 0, 1, 0, false, "reclaim"},
		{"reclaim does not preempt", true, true, false, 80, 2, 0, false, "interactive"},
		{"unknown demand protects", true, false, true, 0, 1, 0, false, "demand_unknown"},
		{"interactive occupies reserve", false, true, false, 0, 1, 1, true, "protected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := capacityNode(tc.running)
			node.interactiveRunning = tc.interactive
			demand := interactiveDemand{pending: map[string]bool{node.ID: tc.pending}, unknown: tc.unknown}
			candidates, decision := rankWithInteractiveCapacity([]Node{node}, requirements, 0, "owner", time.Now().UTC(), DefaultPlacementPolicy(), capacityPolicy(tc.borrow), tc.priority, demand)
			if (len(candidates) == 1) != tc.eligible || decision.Candidates[0].Eligible != tc.eligible || decision.Candidates[0].ReservationOutcome != tc.outcome {
				t.Fatalf("unexpected capacity decision: %#v", decision)
			}
			if node.Capabilities.Running != tc.running {
				t.Fatal("ranking changed a running execution")
			}
		})
	}
}

func TestInteractiveCapacityDefaultsScopesAndOverlaps(t *testing.T) {
	node := capacityNode(1)
	requirements := Requirements{Task: "generation"}
	rank := func(p InteractiveCapacity) RoutingCandidateDecision {
		_, d := rankWithInteractiveCapacity([]Node{node}, requirements, 0, "", time.Now().UTC(), DefaultPlacementPolicy(), p, 0, interactiveDemand{})
		return d.Candidates[0]
	}
	if d := rank(InteractiveCapacity{}); !d.Eligible || d.ReservedSlots != 0 {
		t.Fatal(d)
	}
	for _, scope := range []InteractiveCapacityScope{
		{Group: "other", Task: "generation", Slots: 1},
		{Group: "voice", Task: "embedding", Slots: 1},
		{Group: "voice", Task: "generation", Provider: "adapter", Slots: 1},
	} {
		if d := rank(InteractiveCapacity{MinPriority: 80, Scopes: []InteractiveCapacityScope{scope}}); !d.Eligible || d.ReservedSlots != 0 {
			t.Fatal(d)
		}
	}
	p := capacityPolicy(true)
	p.Scopes = append(p.Scopes, InteractiveCapacityScope{Group: "voice", Task: "generation", Slots: 10})
	if d := rank(p); d.Eligible || d.ReservedSlots != 2 || d.ReservationOutcome != "protected" {
		t.Fatal(d)
	}
	for _, invalid := range []InteractiveCapacity{
		{MinPriority: 0, Scopes: p.Scopes}, {MinPriority: 101, Scopes: p.Scopes},
		{MinPriority: 80, Scopes: []InteractiveCapacityScope{{Task: "generation", Slots: 1}}},
		{MinPriority: 80, Scopes: []InteractiveCapacityScope{{Group: "voice", Task: "generation", Slots: -1}}},
	} {
		if invalid.Validate() == nil {
			t.Fatalf("accepted invalid policy: %#v", invalid)
		}
	}
}

func TestInteractiveCapacityLiveSlotRaceAndTerminalReclaim(t *testing.T) {
	w := newWorkerConnection(nil, 4)
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if w.reserveCapacity(fmt.Sprintf("batch-%d", i), 1, false) {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 3 {
		t.Fatalf("batch stole protected capacity: %d", won.Load())
	}
	if !w.reserveCapacity("interactive", 1, true) || !w.beginDispatch("interactive", 1) {
		t.Fatal("interactive could not use reserve")
	}
	w.markStoreTerminal("interactive")
	if w.interactiveLoad() != 1 || w.reserveCapacity("replacement", 1, true) {
		t.Fatal("terminal store state reclaimed running capacity")
	}
	w.release("interactive")
	if w.reserveCapacity("batch-replacement", 1, false) || !w.reserveCapacity("interactive-replacement", 1, true) {
		t.Fatal("released slot was not reclaimed for interactive work")
	}
	// Borrowers retain their slots when demand returns, even after timeout.
	b := newWorkerConnection(nil, 2)
	if !b.reserveCapacity("borrow-1", 0, false) || !b.reserveCapacity("borrow-2", 0, false) {
		t.Fatal("idle slots not borrowable")
	}
	b.markStoreTerminal("borrow-1")
	if b.reserveCapacity("interactive", 1, true) {
		t.Fatal("borrower was preempted")
	}
	b.release("borrow-1")
	if b.reserveCapacity("batch", 1, false) || !b.reserveCapacity("interactive", 1, true) {
		t.Fatal("borrowed slot not reclaimed on release")
	}
}

func TestInteractiveDemandAcrossRotatingWindowAndBindings(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node := capacityNode(1)
	for i := 0; i < 250; i++ {
		if _, err := store.CreateJob(SubmitRequest{Priority: 0, Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	interactive, err := store.CreateJob(SubmitRequest{Priority: 80, OwnerSubject: "voice-producer", Requirements: Requirements{Task: "generation", Group: "voice"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	window, _, err := store.QueuedJobsFairWindow(200, queueKey(interactive), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range window {
		if j.ID == interactive.ID {
			t.Fatal("test did not hide interactive job behind rotating window")
		}
	}
	demand, err := store.interactiveDemandSnapshot([]Node{node}, capacityPolicy(true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !demand.pending[node.ID] || demand.unknown {
		t.Fatal("demand was missed outside rotating window")
	}
	merged := mergeInteractiveQueue(window, demand.jobs, nil)
	if len(merged) != 201 || merged[0].ID != interactive.ID {
		t.Fatal("interactive work did not precede batch window")
	}
	node.Capabilities.Groups = []string{"other"}
	demand, err = store.interactiveDemandSnapshot([]Node{node}, capacityPolicy(true), nil)
	if err != nil || demand.pending[node.ID] {
		t.Fatal("demand leaked across worker groups", err)
	}
}

func TestInteractiveDemandScanLimitFailsClosed(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Use the durable projection writer in one transaction to avoid thousands
	// of fsyncs while exercising the real priority index and bounded scan.
	err = store.db.Update(func(tx *bolt.Tx) error {
		for i := 0; i <= maximumInteractiveDemandScan; i++ {
			job := Job{ID: fmt.Sprintf("interactive-%d", i), Priority: 80, CreatedAt: time.Now().UTC(), Requirements: Requirements{Group: "other", Task: "generation"}}
			if err := putQueueEntry(tx, job); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	demand, err := store.interactiveDemandSnapshot([]Node{capacityNode(1)}, capacityPolicy(true), nil)
	if err != nil || !demand.unknown {
		t.Fatal("truncated scan claimed idle", err)
	}
}

func TestRouteExplainInteractiveCapacityPriorityAndPrivacy(t *testing.T) {
	const admin = "admin-token-with-enough-entropy-000000000000"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}, InteractiveCapacity: capacityPolicy(false)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	node := capacityNode(1)
	if err := relay.store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	w := newWorkerConnection(nil, 2)
	w.reserve("private-running-job")
	relay.workers[node.ID] = w
	zero := 0
	token, _, err := relay.store.CreateTokenWithPolicies("producer", "private-owner", []string{"voice"}, time.Hour, ProducerLimits{MaxPriority: &zero}, ObserverLimits{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	request := RouteExplainRequest{AssignmentRequest: AssignmentRequest{Requirements: Requirements{Task: "generation", Group: "voice"}}}
	var decision RoutingDecision
	postTest(t, server.URL+"/v1/cluster/routes/explain", token, request, &decision)
	if decision.SelectedNodeID != "" || decision.Candidates[0].ReservationOutcome != "protected" {
		t.Fatal(decision)
	}
	raw, _ := json.Marshal(decision)
	if strings.Contains(string(raw), "private-owner") || strings.Contains(string(raw), "private-running-job") {
		t.Fatal("private capacity evidence leaked")
	}
	request.Priority = 80
	postTest(t, server.URL+"/v1/cluster/routes/explain", admin, request, &decision)
	if decision.SelectedNodeID != node.ID || decision.Candidates[0].ReservationOutcome != "interactive" {
		t.Fatal(decision)
	}
	raw, _ = json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/routes/explain", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	relay.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), AdmissionCodePriorityForbidden) {
		t.Fatal(response.Body.String())
	}
}
