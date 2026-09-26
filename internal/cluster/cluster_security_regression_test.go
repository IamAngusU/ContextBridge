package cluster

import (
	"context"
	"encoding/json"
	"errors"
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

func TestWorkerHeartbeatRetainsTokenScopeAndProtocolCapacity(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
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
	pair, err := relay.store.CreatePairing(PairRequest{NodeName: "scoped-node", PublicKey: publicKey, Groups: []string{"trusted"}}, server.URL+"/#pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.DecidePairing(pair.UserCode, true); err != nil {
		t.Fatal(err)
	}
	state, pairing, token, err := relay.store.PollPairing(pair.DeviceCode)
	if err != nil || state != "approved" {
		t.Fatalf("pairing failed: %s, %v", state, err)
	}
	conn := dialTestWorker(t, server.URL, token)
	defer conn.CloseNow()
	hello := Node{
		ID: pairing.NodeID, Name: "scoped-node", PublicKey: publicKey,
		Capabilities: Capabilities{Groups: []string{"trusted", "forged"}, MaxConcurrent: MaximumWorkerConcurrency + 1000},
	}
	if err := conn.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &hello})); err != nil {
		t.Fatal(err)
	}
	readTestAuthority(t, conn)
	waitFor(t, 2*time.Second, func() bool {
		node, loadErr := relay.store.GetNode(pairing.NodeID)
		return loadErr == nil && node.Connected && node.Capabilities.MaxConcurrent == MaximumWorkerConcurrency && len(node.Capabilities.Groups) == 1 && node.Capabilities.Groups[0] == "trusted"
	}, "hello capability scope or capacity was not enforced")

	heartbeat := Capabilities{Groups: []string{"forged"}, MaxConcurrent: MaximumWorkerConcurrency + 5000}
	if err := conn.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "heartbeat", Capabilities: &heartbeat})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		node, loadErr := relay.store.GetNode(pairing.NodeID)
		return loadErr == nil && node.Capabilities.MaxConcurrent == MaximumWorkerConcurrency && len(node.Capabilities.Groups) == 0
	}, "heartbeat restored a group excluded by the node token")
	relay.mu.RLock()
	worker := relay.workers[pairing.NodeID]
	relay.mu.RUnlock()
	if worker == nil {
		t.Fatal("scoped worker connection disappeared")
	}
	_, capacity := worker.load()
	if capacity != MaximumWorkerConcurrency {
		t.Fatalf("live connection capacity = %d, want %d", capacity, MaximumWorkerConcurrency)
	}
}

func TestConfiguredAdminTokenRotationRetiresPreviousBootstrapOnly(t *testing.T) {
	database := filepath.Join(t.TempDir(), "relay.db")
	tokenA := "admin_A_012345678901234567890123456789012345"
	tokenB := "admin_B_012345678901234567890123456789012345"
	tokenC := "admin_C_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: database, AdminToken: tokenA}, nil)
	if err != nil {
		t.Fatal(err)
	}
	explicit, explicitRecord, err := relay.store.CreateToken("admin", "operator-admin", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}

	relay, err = NewRelay(RelayConfig{Database: database, AdminToken: tokenB}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := relay.store.Authenticate(tokenA); ok {
		t.Fatal("retired configured bootstrap token remained authorized")
	}
	if _, ok := relay.store.Authenticate(tokenB); !ok {
		t.Fatal("replacement configured bootstrap token was not authorized")
	}
	if record, ok := relay.store.Authenticate(explicit); !ok || record.ID != explicitRecord.ID {
		t.Fatal("explicitly issued administrator was changed by bootstrap rotation")
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}

	relay, err = NewRelay(RelayConfig{Database: database, AdminToken: tokenC}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if _, ok := relay.store.Authenticate(tokenA); ok {
		t.Fatal("first retired bootstrap token was reactivated")
	}
	if _, ok := relay.store.Authenticate(tokenB); ok {
		t.Fatal("second retired bootstrap token remained authorized")
	}
	if _, ok := relay.store.Authenticate(tokenC); !ok {
		t.Fatal("latest configured bootstrap token was not authorized")
	}
}

func TestExplicitTokenRevocationAndInventorySurviveStoreRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "relay.db")
	store, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	secret, record, err := store.CreateToken("admin", "incident-operator", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeToken(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, ok := store.Authenticate(secret); ok {
		t.Fatal("revoked credential reactivated after store restart")
	}
	inventory, err := store.ListTokens(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range inventory.Tokens {
		if item.ID == record.ID {
			found = item.Revoked && item.AuthHash == ""
		}
	}
	if !found {
		t.Fatalf("durable revoked credential missing from redacted inventory: %#v", inventory)
	}
}

func TestBootstrapAdminSecretCannotElevateExistingCredential(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret := "shared_secret_012345678901234567890123456789"
	if err := store.EnsureToken(secret, "producer", "application", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureBootstrapAdminToken(secret); err == nil {
		t.Fatal("existing non-admin credential was silently elevated by bootstrap configuration")
	}
	record, ok := store.Authenticate(secret)
	if !ok || record.Role != "producer" || record.Subject != "application" {
		t.Fatalf("failed bootstrap collision changed credential authority: %#v ok=%t", record, ok)
	}
}

func TestRevokedConnectedWorkerCannotReceiveNewAssignment(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}}, nil)
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
	nodeID := "revoked-live-node"
	nodeToken, tokenRecord, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	connection := dialTestWorker(t, server.URL, nodeToken)
	defer connection.CloseNow()
	node := Node{ID: nodeID, Name: nodeID, PublicKey: publicKey, Capabilities: Capabilities{
		Providers: []string{"ollama"}, Tasks: []string{"generation"}, MaxConcurrent: 1,
		Models: []ModelCapability{{Provider: "ollama", Name: "ready", Tasks: []string{"generation"}}},
	}}
	if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); err != nil {
		t.Fatal(err)
	}
	readTestAuthority(t, connection)
	waitFor(t, 2*time.Second, func() bool {
		saved, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && saved.Connected
	}, "worker did not connect")
	revokeRequest := httptest.NewRequest(http.MethodDelete, "/v1/cluster/tokens/"+tokenRecord.ID, nil)
	revokeRequest.Header.Set("Authorization", "Bearer "+admin)
	revokeResponse := httptest.NewRecorder()
	relay.Handler().ServeHTTP(revokeResponse, revokeRequest)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("node revocation returned %d: %s", revokeResponse.Code, revokeResponse.Body.String())
	}
	waitFor(t, 2*time.Second, func() bool {
		saved, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && !saved.Connected
	}, "revoked node connection was not removed immediately")
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer", Requirements: Requirements{Provider: "ollama", Model: "ready", Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	relay.dispatch()
	stored, err := relay.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobQueued {
		t.Fatalf("revoked live worker received job state %s", stored.Status)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, raw, readErr := connection.Read(readCtx); readErr == nil {
		t.Fatalf("revoked worker received a new frame: %s", raw)
	}
}

func TestCredentialRevokedBetweenUpgradeAndHelloNeverPublishesWorker(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
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
	nodeID := "revoked-before-hello"
	nodeToken, tokenRecord, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	connection := dialTestWorker(t, server.URL, nodeToken)
	defer connection.CloseNow()
	if err := relay.store.RevokeToken(tokenRecord.ID); err != nil {
		t.Fatal(err)
	}
	node := Node{ID: nodeID, Name: nodeID, PublicKey: publicKey, Capabilities: Capabilities{MaxConcurrent: 1}}
	if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := connection.Read(readCtx); err == nil {
		t.Fatal("worker remained connected after its credential was revoked before hello")
	}
	relay.mu.RLock()
	worker := relay.workers[nodeID]
	relay.mu.RUnlock()
	if worker != nil {
		t.Fatal("revoked pre-hello worker was published in the live worker map")
	}
}

func TestRevokedConnectedWorkerCannotCompleteExistingAssignment(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	_, publicKey, _ := NewIdentity()
	nodeID := "revoked-result-node"
	nodeToken, tokenRecord, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	connection := dialTestWorker(t, server.URL, nodeToken)
	defer connection.CloseNow()
	node := Node{ID: nodeID, Name: nodeID, PublicKey: publicKey, Capabilities: Capabilities{
		Providers: []string{"ollama"}, Tasks: []string{"generation"}, MaxConcurrent: 1,
		Models: []ModelCapability{{Provider: "ollama", Name: "ready", Tasks: []string{"generation"}}},
	}}
	if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); err != nil {
		t.Fatal(err)
	}
	readTestAuthority(t, connection)
	waitFor(t, 2*time.Second, func() bool {
		saved, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && saved.Connected
	}, "worker did not connect")
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer", Requirements: Requirements{Provider: "ollama", Model: "ready", Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	relay.dispatch()
	readCtx, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
	_, raw, err := connection.Read(readCtx)
	cancelRead()
	if err != nil {
		t.Fatal(err)
	}
	var assignment WireMessage
	if err := json.Unmarshal(raw, &assignment); err != nil || assignment.Type != "job" || assignment.Job == nil {
		t.Fatalf("worker did not receive assignment: %s err=%v", raw, err)
	}
	if err := relay.store.RevokeToken(tokenRecord.ID); err != nil {
		t.Fatal(err)
	}
	report := WireMessage{
		Version: ProtocolVersion, Type: "result", JobID: assignment.Job.ID, Attempt: assignment.Job.Attempt,
		Fence: assignment.Job.AssignmentFence, Result: json.RawMessage(`{"text":"must not be accepted"}`),
	}
	if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(report)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		stored, loadErr := relay.store.GetJob(job.ID)
		return loadErr == nil && stored.Status != JobCompleted
	}, "revoked worker result was accepted")
	readCtx, cancelRead = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRead()
	if _, _, err := connection.Read(readCtx); err == nil {
		t.Fatal("revoked worker connection remained authorized after a result message")
	}
	stored, err := relay.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == JobCompleted {
		t.Fatalf("revoked worker completed job: %#v", stored)
	}
}

func TestAuthorizedAssignmentRejectsExpiredCredentialAndAllowsValidCredential(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authority, err := store.AcquireRelayAuthority()
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "credential-node"
	if err := store.UpsertNode(Node{ID: nodeID, Name: nodeID}); err != nil {
		t.Fatal(err)
	}
	expiredToken, _, err := store.CreateToken("node", nodeID, nil, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	expired, _ := store.Authenticate(expiredToken)
	// Authenticate can race a very short expiry, so retain the durable hash
	// explicitly for the assignment-boundary check.
	expiredHash := tokenHash(expiredToken)
	if expired.AuthHash != "" {
		expiredHash = expired.AuthHash
	}
	time.Sleep(time.Millisecond)
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	decision := RoutingDecision{RouteKey: "", SelectedNodeID: nodeID, Candidates: []RoutingCandidateDecision{{NodeID: nodeID, Eligible: true}}}
	if _, err := store.AssignJobAuthorizedFencedWithDecision(job.ID, nodeID, expiredHash, decision, authority); !errors.Is(err, ErrWorkerCredentialInvalid) {
		t.Fatalf("expired credential assignment returned %v", err)
	}
	validToken, _, err := store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	valid, ok := store.Authenticate(validToken)
	if !ok {
		t.Fatal("valid node token did not authenticate")
	}
	if _, err := store.AssignJobAuthorizedFencedWithDecision(job.ID, nodeID, valid.AuthHash, decision, authority); err != nil {
		t.Fatalf("valid credential assignment failed: %v", err)
	}
}

func TestAuthorizedNodeUpsertRejectsRevokedCredentialWithoutMutation(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := "atomic-upsert-node"
	token, record, err := store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	authenticated, ok := store.Authenticate(token)
	if !ok {
		t.Fatal("node credential did not authenticate")
	}
	if err := store.RevokeToken(record.ID); err != nil {
		t.Fatal(err)
	}
	_, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	node := Node{ID: nodeID, Name: nodeID, PublicKey: publicKey, Connected: true, State: "online"}
	if err := store.UpsertNodePinnedAuthorized(node, authenticated.AuthHash); !errors.Is(err, ErrWorkerCredentialInvalid) {
		t.Fatalf("revoked credential upsert returned %v", err)
	}
	if stored, err := store.GetNode(nodeID); err == nil {
		t.Fatalf("revoked credential mutated durable node state: %#v", stored)
	}
}

func TestPairedWorkerCannotRotatePublicKeyWithBearer(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	_, pairedKey, _ := NewIdentity()
	_, replacementKey, _ := NewIdentity()
	pair, err := relay.store.CreatePairing(PairRequest{NodeName: "pinned-node", PublicKey: pairedKey}, server.URL+"/#pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.DecidePairing(pair.UserCode, true); err != nil {
		t.Fatal(err)
	}
	state, pairing, token, err := relay.store.PollPairing(pair.DeviceCode)
	if err != nil || state != "approved" {
		t.Fatalf("pairing failed: %s, %v", state, err)
	}

	conn := dialTestWorker(t, server.URL, token)
	defer conn.CloseNow()
	forged := Node{ID: pairing.NodeID, Name: "stolen-token", PublicKey: replacementKey, Capabilities: Capabilities{MaxConcurrent: 1}}
	if err := conn.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &forged})); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("worker with a replacement key remained connected")
	}
	saved, err := relay.store.GetNode(pairing.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.PublicKey != pairedKey || saved.Connected {
		t.Fatalf("paired identity was replaced: %#v", saved)
	}
}

func dialTestWorker(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/v1/cluster/workers/connect"
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestAtomicPerOwnerQueueAdmissionAndFairDispatchWindow(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const ownerCapacity = 5
	const contenders = 32
	start := make(chan struct{})
	var admitted atomic.Int32
	var limited atomic.Int32
	var unexpected atomic.Value
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, createErr := store.CreateJobAdmitted(SubmitRequest{ID: fmt.Sprintf("owner-a-%d", index), OwnerSubject: "owner-a", Payload: json.RawMessage(`{}`)}, 100, ownerCapacity)
			switch {
			case createErr == nil:
				admitted.Add(1)
			case errors.Is(createErr, ErrOwnerQueueCapacity):
				limited.Add(1)
			default:
				unexpected.Store(createErr)
			}
		}()
	}
	close(start)
	wait.Wait()
	if value := unexpected.Load(); value != nil {
		t.Fatalf("unexpected admission error: %v", value)
	}
	if admitted.Load() != ownerCapacity || limited.Load() != contenders-ownerCapacity {
		t.Fatalf("per-owner queue admission = %d admitted, %d limited", admitted.Load(), limited.Load())
	}
	if _, err := store.CreateJobAdmitted(SubmitRequest{ID: "owner-b-1", OwnerSubject: "owner-b", Payload: json.RawMessage(`{}`)}, 100, ownerCapacity); err != nil {
		t.Fatalf("one producer exhausted another's queue share: %v", err)
	}

	// Use a fresh store to make the expected same-priority round robin clear.
	fairStore, err := OpenStore(filepath.Join(t.TempDir(), "fair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fairStore.Close()
	for _, id := range []string{"a1", "a2", "a3"} {
		if _, err := fairStore.CreateJob(SubmitRequest{ID: id, OwnerSubject: "owner-a", Priority: 10, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"b1", "b2"} {
		if _, err := fairStore.CreateJob(SubmitRequest{ID: id, OwnerSubject: "owner-b", Priority: 10, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fairStore.CreateJob(SubmitRequest{ID: "high", OwnerSubject: "owner-c", Priority: 20, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	queued, err := fairStore.QueuedJobsFair(6)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"high", "a1", "b1", "a2", "b2", "a3"}
	for index, id := range want {
		if queued[index].ID != id {
			t.Fatalf("fair queue order[%d] = %q, want %q (all: %#v)", index, queued[index].ID, id, queued)
		}
	}
}

func TestFairQueueCursorPreventsRepeatedOneSlotStarvation(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "fair-repeat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, item := range []struct {
		id, owner string
	}{
		{id: "a1", owner: "owner-a"}, {id: "a2", owner: "owner-a"}, {id: "a3", owner: "owner-a"},
		{id: "b1", owner: "owner-b"},
	} {
		if _, err := store.CreateJob(SubmitRequest{ID: item.id, OwnerSubject: item.owner, Priority: 10, Requirements: Requirements{Provider: "ollama"}, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	cursor := map[int]string{}
	want := []string{"a1", "b1", "a2"}
	for _, id := range want {
		queued, err := store.QueuedJobsFairAfter(1, cursor)
		if err != nil || len(queued) != 1 {
			t.Fatalf("one-slot fair scan failed: %#v, %v", queued, err)
		}
		if queued[0].ID != id {
			t.Fatalf("one-slot fair dispatch selected %q, want %q", queued[0].ID, id)
		}
		if _, err := store.AssignJob(queued[0].ID, "node"); err != nil {
			t.Fatal(err)
		}
		cursor[queued[0].Priority] = queued[0].OwnerSubject
	}
}

func TestRelayDispatchRotatesPastLargeIncompatibleQueuePrefix(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}}, nil)
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
	nodeID := "rotating-scan-node"
	nodeToken, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	connection, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/cluster/workers/connect", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + nodeToken}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	node := Node{ID: nodeID, Name: nodeID, PublicKey: publicKey, Capabilities: Capabilities{
		Providers: []string{"ollama"}, Tasks: []string{"generation"}, MaxConcurrent: 1,
		Models: []ModelCapability{{Provider: "ollama", Name: "available", Tasks: []string{"generation"}}},
	}}
	if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); err != nil {
		t.Fatal(err)
	}
	readTestAuthority(t, connection)
	waitFor(t, 2*time.Second, func() bool {
		saved, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && saved.Connected
	}, "worker did not connect")

	for round := 0; round < 50; round++ {
		for owner := 0; owner < 4; owner++ {
			id := fmt.Sprintf("blocked-%d-%02d", owner, round)
			_, err := relay.store.CreateJob(SubmitRequest{ID: id, OwnerSubject: fmt.Sprintf("owner-%d", owner), Priority: 10, Requirements: Requirements{Provider: "ollama", Task: "generation", Model: "missing"}, Payload: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	const routeableID = "routeable-after-200"
	if _, err := relay.store.CreateJob(SubmitRequest{ID: routeableID, OwnerSubject: "owner-0", Priority: 10, Requirements: Requirements{Provider: "ollama", Task: "generation", Model: "available"}, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}

	relay.dispatch()
	queued, err := relay.store.GetJob(routeableID)
	if err != nil || queued.Status != JobQueued {
		t.Fatalf("first 200-candidate page unexpectedly changed routeable job: %#v, %v", queued, err)
	}
	relay.dispatch()
	readContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, raw, err := connection.Read(readContext)
	if err != nil {
		t.Fatalf("second dispatch page never reached routeable job: %v", err)
	}
	var message WireMessage
	if json.Unmarshal(raw, &message) != nil || message.Job == nil || message.Job.ID != routeableID {
		t.Fatalf("wrong job dispatched after rotating scan: %s", raw)
	}
}

func TestAtomicPipelineAdmissionReleasesOnTerminalState(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const ownerCapacity = 3
	const globalCapacity = 6
	const contenders = 24
	start := make(chan struct{})
	var admitted atomic.Int32
	var ownerLimited atomic.Int32
	var unexpected atomic.Value
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := store.CreatePipelineRunAdmitted(PipelineRun{ID: fmt.Sprintf("run-a-%d", index), Pipeline: "test", OwnerSubject: "owner-a", Status: "running", CreatedAt: time.Now().UTC()}, globalCapacity, ownerCapacity)
			switch {
			case err == nil:
				admitted.Add(1)
			case errors.Is(err, ErrOwnerPipelineCapacity):
				ownerLimited.Add(1)
			default:
				unexpected.Store(err)
			}
		}()
	}
	close(start)
	wait.Wait()
	if value := unexpected.Load(); value != nil {
		t.Fatalf("unexpected pipeline admission error: %v", value)
	}
	if admitted.Load() != ownerCapacity || ownerLimited.Load() != contenders-ownerCapacity {
		t.Fatalf("pipeline owner admission = %d admitted, %d limited", admitted.Load(), ownerLimited.Load())
	}
	for index := 0; index < ownerCapacity; index++ {
		if err := store.CreatePipelineRunAdmitted(PipelineRun{ID: fmt.Sprintf("run-b-%d", index), Pipeline: "test", OwnerSubject: "owner-b", Status: "running", CreatedAt: time.Now().UTC()}, globalCapacity, ownerCapacity); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreatePipelineRunAdmitted(PipelineRun{ID: "run-c-full", Pipeline: "test", OwnerSubject: "owner-c", Status: "running", CreatedAt: time.Now().UTC()}, globalCapacity, ownerCapacity); !errors.Is(err, ErrPipelineCapacity) {
		t.Fatalf("global active-pipeline bound returned %v", err)
	}
	runs, err := store.ListPipelineRuns(100)
	if err != nil {
		t.Fatal(err)
	}
	terminal := runs[0]
	terminal.Status = "failed"
	terminal.Error = "finished"
	terminal.FinishedAt = time.Now().UTC()
	if err := store.SavePipelineRun(terminal); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePipelineRunAdmitted(PipelineRun{ID: "run-c-after-terminal", Pipeline: "test", OwnerSubject: "owner-c", Status: "running", CreatedAt: time.Now().UTC()}, globalCapacity, ownerCapacity); err != nil {
		t.Fatalf("terminal pipeline did not release active capacity: %v", err)
	}
	failed, err := store.FailActivePipelineRuns("relay restarted")
	if err != nil || failed != globalCapacity {
		t.Fatalf("startup recovery closed %d active runs, want %d: %v", failed, globalCapacity, err)
	}
	if err := store.CreatePipelineRunAdmitted(PipelineRun{ID: "run-after-recovery", Pipeline: "test", OwnerSubject: "owner-a", Status: "running", CreatedAt: time.Now().UTC()}, globalCapacity, ownerCapacity); err != nil {
		t.Fatalf("startup recovery did not release pipeline capacity: %v", err)
	}
}

func TestRelayStartupFailsAmbiguousExecutionsAndMarksNodesOffline(t *testing.T) {
	database := filepath.Join(t.TempDir(), "relay.db")
	admin := "admin_012345678901234567890123456789012345"
	store, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	node := Node{
		ID: "restart-node", Name: "Restart node", Connected: true, State: "online",
		Capabilities: Capabilities{MaxConcurrent: 4, Running: 2},
	}
	if err := store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	running, err := store.CreateJob(SubmitRequest{
		Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"already dispatched"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err = store.AssignJob(running.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(running.ID, node.ID, running.Attempt); err != nil {
		t.Fatal(err)
	}
	queued, err := store.CreateJob(SubmitRequest{
		Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"not dispatched"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	relay, err := NewRelay(RelayConfig{Database: database, AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	recoveredNode, err := relay.store.GetNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredNode.Connected || recoveredNode.State != "offline" || recoveredNode.Capabilities.Running != 0 {
		t.Fatalf("persisted node stayed live after relay restart: %#v", recoveredNode)
	}
	recoveredJob, err := relay.store.GetJob(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredJob.Status != JobFailed || !strings.Contains(recoveredJob.Error, "explicit resubmission required") || recoveredJob.Progress != nil {
		t.Fatalf("ambiguous execution did not fail closed at startup: %#v", recoveredJob)
	}
	untouched, err := relay.store.GetJob(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Status != JobQueued {
		t.Fatalf("never-dispatched job changed during restart recovery: %#v", untouched)
	}
}

func TestFailActivePipelineRunsAtomicallyStopsQueuedChildren(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run := PipelineRun{ID: "run-restart", Pipeline: "linear", OwnerSubject: "producer-a", Status: "running", CreatedAt: time.Now().UTC()}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateJob(SubmitRequest{ParentID: run.ID, Pipeline: run.Pipeline, Step: "first", OwnerSubject: run.OwnerSubject, Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	independent, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-b", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.FailActivePipelineRuns("relay restarted before pipeline completion")
	if err != nil || failed != 1 {
		t.Fatalf("pipeline recovery failed %d run(s): %v", failed, err)
	}
	child, err = store.GetJob(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != JobFailed || child.FailureCode != FailurePipelineParentTerminal {
		t.Fatalf("orphaned queued child remained executable: %#v", child)
	}
	if _, err := store.AssignJob(child.ID, "node-a"); err == nil {
		t.Fatal("terminal pipeline child became assignable")
	}
	independent, err = store.GetJob(independent.ID)
	if err != nil || independent.Status != JobQueued {
		t.Fatalf("independent queued job changed during pipeline recovery: %#v, %v", independent, err)
	}
	queued, err := store.QueuedJobs(10)
	if err != nil || len(queued) != 1 || queued[0].ID != independent.ID {
		t.Fatalf("queue was not repaired atomically: %#v, %v", queued, err)
	}
}

func TestAssignmentRejectsQueuedChildAfterParentBecomesTerminal(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run := PipelineRun{ID: "run-race", Pipeline: "linear", OwnerSubject: "producer-a", Status: "running", CreatedAt: time.Now().UTC()}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateJob(SubmitRequest{ParentID: run.ID, Pipeline: run.Pipeline, Step: "first", OwnerSubject: run.OwnerSubject, Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "failed"
	run.Error = "stopped"
	run.FinishedAt = time.Now().UTC()
	if err := store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(child.ID, "node-a"); !errors.Is(err, ErrPipelineParentTerminal) {
		t.Fatalf("terminal-parent assignment returned %v", err)
	}
}

func TestTimeoutDisconnectReleasesOnlyMatchingAdapterSessionLock(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", SessionID: "timeout-session"}
	first, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	first, err = store.AssignAdapterJob(first.ID, "node-a", 41, false)
	if err != nil {
		t.Fatal(err)
	}
	first.AssignedAt = time.Now().Add(-time.Hour)
	if err := store.SaveJob(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverStaleJobs(time.Now().UTC(), time.Minute, time.Minute); err != nil {
		t.Fatal(err)
	}
	if busy, err := store.AdapterSessionBusy("producer-a", requirements, ""); err != nil || !busy {
		t.Fatalf("timeout released lock before execution ended: busy=%v err=%v", busy, err)
	}
	if _, err := store.RequeueNode("node-a", "worker disconnected"); err != nil {
		t.Fatal(err)
	}
	if busy, err := store.AdapterSessionBusy("producer-a", requirements, ""); err != nil || busy {
		t.Fatalf("disconnect did not release timed-out execution lock: busy=%v err=%v", busy, err)
	}
	second, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignAdapterJob(second.ID, "node-b", 42, false); err != nil {
		t.Fatalf("fresh worker could not continue released session: %v", err)
	}
}

func TestPipelineCloneDoesNotMutateSharedRequestScope(t *testing.T) {
	original := Pipeline{Steps: []PipelineStep{{Name: "one", Requirements: Requirements{RequiredTags: []string{"tag-a"}, PreferredNodes: []string{"node-a"}, InputImageMediaTypes: []string{"image/png"}}}}}
	cloned := clonePipeline(original)
	if err := scopeRequirements(&cloned.Steps[0].Requirements, TokenRecord{Role: "producer", Groups: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}
	cloned.Steps[0].Requirements.RequiredTags[0] = "changed"
	cloned.Steps[0].Requirements.PreferredNodes[0] = "changed"
	cloned.Steps[0].Requirements.InputImageMediaTypes[0] = "changed"
	if original.Steps[0].Requirements.Group != "" || original.Steps[0].Requirements.RequiredTags[0] != "tag-a" || original.Steps[0].Requirements.PreferredNodes[0] != "node-a" || original.Steps[0].Requirements.InputImageMediaTypes[0] != "image/png" {
		t.Fatalf("request scoping mutated configured pipeline: %#v", original)
	}
}

func TestPipelineHandlerScopesIndependentCopiesPerProducer(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	pipeline := Pipeline{Steps: []PipelineStep{{Name: "one", Requirements: Requirements{Task: "generation"}, Input: `${missing}`}}}
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:    admin,
		AllowedTasks:  []string{"generation"},
		MaxQueuedJobs: 100,
		Pipelines:     map[string]Pipeline{"scoped": pipeline},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	tokenA, _, err := relay.store.CreateToken("producer", "producer-a", []string{"group-a"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, _, err := relay.store.CreateToken("producer", "producer-b", []string{"group-b"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{tokenA, tokenB} {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/cluster/pipelines/scoped/run", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("independent producer pipeline returned %s", response.Status)
		}
	}
	if got := relay.cfg.Pipelines["scoped"].Steps[0].Requirements.Group; got != "" {
		t.Fatalf("shared pipeline group was mutated to %q", got)
	}
	waitFor(t, 2*time.Second, func() bool {
		runs, listErr := relay.store.ListPipelineRuns(10)
		if listErr != nil || len(runs) != 2 {
			return false
		}
		for _, run := range runs {
			if run.Status == "running" {
				return false
			}
		}
		return true
	}, "scoped test pipelines did not reach a terminal state")
}

func TestPairingGCRemovesOnlyExpiredDeliveryState(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	expired, err := store.CreatePairing(PairRequest{NodeName: "expired", PublicKey: publicKey}, "https://relay.test/pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.CreatePairing(PairRequest{NodeName: "active", PublicKey: publicKey}, "https://relay.test/pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expiredHash := tokenHash(expired.DeviceCode)
	activeHash := tokenHash(active.DeviceCode)
	var expiredCode string
	if err := store.db.Update(func(tx *bolt.Tx) error {
		var pairing Pairing
		if err := getJSON(tx.Bucket(bucketPairings), expiredHash, &pairing); err != nil {
			return err
		}
		expiredCode = pairing.UserCode
		pairing.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		return putJSON(tx.Bucket(bucketPairings), expiredHash, pairing)
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := store.GarbageCollectPairings(time.Now().UTC())
	if err != nil || removed != 1 {
		t.Fatalf("pairing GC removed %d records, want 1: %v", removed, err)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketPairings).Get([]byte(expiredHash)) != nil || tx.Bucket(bucketPairCodes).Get([]byte(expiredCode)) != nil {
			return errors.New("expired pairing delivery state remains")
		}
		if tx.Bucket(bucketPairings).Get([]byte(activeHash)) == nil {
			return errors.New("active pairing was removed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.DecidePairing(active.UserCode, true); err != nil {
		t.Fatal(err)
	}
	state, pairing, nodeToken, err := store.PollPairing(active.DeviceCode)
	if err != nil || state != "approved" || pairing.NodeID == "" || nodeToken == "" {
		t.Fatalf("approved pairing delivery failed: %s %#v %v", state, pairing, err)
	}
	if _, ok := store.Authenticate(nodeToken); !ok {
		t.Fatal("pairing GC removed the issued node token")
	}
	if _, err := store.GetNode(pairing.NodeID); err != nil {
		t.Fatalf("pairing GC removed the paired node: %v", err)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		var delivered Pairing
		if err := getJSON(tx.Bucket(bucketPairings), activeHash, &delivered); err != nil {
			return errors.New("token-free pairing poll state was removed before expiry")
		}
		if delivered.PendingToken != "" {
			return errors.New("one-time node token remains in pairing state")
		}
		if tx.Bucket(bucketPairCodes).Get([]byte(active.UserCode)) != nil {
			return errors.New("used pairing approval code remains")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
