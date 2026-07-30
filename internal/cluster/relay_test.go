package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRelayDispatchAndEncryptedRoundTrip(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	defer stopDispatch()
	go relay.dispatchLoop(dispatchCtx)
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	privateKey, publicKey, _ := NewIdentity()
	pair, err := relay.store.CreatePairing(PairRequest{NodeName: "gpu-node", PublicKey: publicKey, Groups: []string{"fast"}}, server.URL+"/#pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.DecidePairing(pair.UserCode, true); err != nil {
		t.Fatal(err)
	}
	state, pairing, nodeToken, err := relay.store.PollPairing(pair.DeviceCode)
	if err != nil || state != "approved" {
		t.Fatal("pairing not approved")
	}
	producer, _, err := relay.store.CreateToken("producer", "test", []string{"fast"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/cluster/workers/connect"
	header := http.Header{"Authorization": []string{"Bearer " + nodeToken}}
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	node := Node{ID: pairing.NodeID, Name: "gpu-node", PublicKey: publicKey, Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{Tasks: []string{"generation"}, Groups: []string{"fast"}, MaxConcurrent: 1, GPUs: []GPUCapability{{MemoryTotal: 8 << 30, MemoryFree: 7 << 30}}}}
	if err := conn.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		saved, _ := relay.store.GetNode(node.ID)
		if saved.Connected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var assignment AssignmentResponse
	postTest(t, server.URL+"/v1/cluster/assign", producer, Requirements{Task: "generation", Group: "fast"}, &assignment)
	plain := json.RawMessage(`{"prompt":"relay must not see this"}`)
	envelope, shared, err := SealFor(assignment.Assignment.PublicKey, plain, jobAAD(assignment.Assignment.JobID, assignment.Assignment.NodeID))
	if err != nil {
		t.Fatal(err)
	}
	var submitted Job
	postTest(t, server.URL+"/v1/cluster/jobs", producer, SubmitRequest{Requirements: assignment.Assignment.Requirements, Sealed: envelope, AssignmentID: assignment.Assignment.ID, AssignmentSecret: assignment.Secret}, &submitted)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wire WireMessage
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Job == nil {
		t.Fatalf("invalid assignment: %v", err)
	}
	if bytes.Contains(wire.Job.Payload, []byte("relay must not see")) || wire.Job.SealedPayload == nil {
		t.Fatal("relay exposed the sealed payload")
	}
	decrypted, workerShared, err := OpenWith(privateKey, wire.Job.SealedPayload, jobAAD(wire.Job.ID, node.ID))
	if err != nil || !bytes.Equal(decrypted, plain) {
		t.Fatalf("worker could not decrypt: %v", err)
	}
	sealedResult, err := SealResponse(workerShared, []byte(`{"answer":"done"}`), resultAAD(wire.Job.ID, node.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "result", JobID: wire.Job.ID, SealedResult: sealedResult, Usage: Usage{InputTokens: 4, OutputTokens: 2}})); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := relay.store.GetJob(wire.Job.ID)
		if job.Status == JobCompleted {
			opened, err := OpenResponse(shared, job.SealedResult, resultAAD(job.ID, node.ID))
			if err != nil || string(opened) != `{"answer":"done"}` {
				t.Fatalf("producer could not decrypt: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}

func postTest(t *testing.T, target, token string, input, output interface{}) {
	t.Helper()
	raw, _ := json.Marshal(input)
	req, _ := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("POST %s returned %s", target, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
		t.Fatal(err)
	}
}
