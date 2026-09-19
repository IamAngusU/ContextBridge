package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRateLimitClientKeyTrustsOnlyLoopbackProxy(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://relay.test/v1/pair/request", nil)
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("X-Real-IP", "203.0.113.7")
	if got := rateLimitClientKey(request); got != "203.0.113.7" {
		t.Fatalf("loopback proxy client key = %q", got)
	}
	request.RemoteAddr = "198.51.100.8:43210"
	request.Header.Set("X-Real-IP", "203.0.113.7")
	if got := rateLimitClientKey(request); got != "198.51.100.8" {
		t.Fatalf("direct client spoofed proxy identity: %q", got)
	}
	request.RemoteAddr = "[::1]:43210"
	request.Header.Set("X-Real-IP", "not-an-ip")
	if got := rateLimitClientKey(request); got != "::1" {
		t.Fatalf("invalid forwarded address replaced peer identity: %q", got)
	}
}

func TestPublicNodeResponseRedactsBrowserSessionKeys(t *testing.T) {
	nodes := []Node{{ID: "node-a", Capabilities: Capabilities{BrowserSessions: []BrowserSessionCapability{{
		TabID: 7, Profile: "chatgpt", SessionKey: "cb:" + strings.Repeat("a", 64), SessionKeySupported: true,
	}}}}}
	redactNodeRoutingEvidence(nodes)
	if nodes[0].Capabilities.BrowserSessions[0].SessionKey != "" {
		t.Fatal("routing-only browser session key remained in a public node response")
	}
	if !nodes[0].Capabilities.BrowserSessions[0].SessionKeySupported {
		t.Fatal("redaction removed non-sensitive compatibility metadata")
	}
}

func TestDecodeJSONEnforcesExactBodyLimit(t *testing.T) {
	raw := []byte(`{"ok":true}`)
	var exact struct {
		OK bool `json:"ok"`
	}
	if err := decodeJSON(bytes.NewReader(raw), &exact, int64(len(raw))); err != nil || !exact.OK {
		t.Fatalf("exact JSON body limit was rejected: %#v, %v", exact, err)
	}
	var over struct {
		OK bool `json:"ok"`
	}
	if err := decodeJSON(bytes.NewReader(append(append([]byte{}, raw...), ' ')), &over, int64(len(raw))); err == nil {
		t.Fatal("one byte beyond the JSON body limit was accepted")
	}
}

func TestCreateTokenRejectsLifetimeBeforeDurationConversion(t *testing.T) {
	adminToken := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: adminToken}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	for _, lifetime := range []string{"-1", "87601", "9223372036854775807"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/cluster/tokens", strings.NewReader(`{"role":"producer","subject":"test","lifetime_hours":`+lifetime+`}`))
		request.Header.Set("Authorization", "Bearer "+adminToken)
		response := httptest.NewRecorder()
		relay.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("lifetime %s returned %d: %s", lifetime, response.Code, response.Body.String())
		}
	}
}

func TestWorkerConnectionRejectsDuplicateReservation(t *testing.T) {
	worker := newWorkerConnection(nil, 2)
	if !worker.reserve("job-one") {
		t.Fatal("first reservation was rejected")
	}
	if worker.reserve("job-one") {
		t.Fatal("duplicate in-flight reservation was accepted")
	}
	running, capacity := worker.load()
	if running != 1 || capacity != 2 {
		t.Fatalf("duplicate reservation changed worker load: %d/%d", running, capacity)
	}
	worker.release("job-one")
	if !worker.reserve("job-one") {
		t.Fatal("released job could not be reserved again")
	}
}

func TestWorkerReservationCancellationBeforeDispatchSuppressesBothFrames(t *testing.T) {
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve("job-before-dispatch") {
		t.Fatal("test reservation was rejected")
	}
	if worker.markStoreTerminal("job-before-dispatch") {
		t.Fatal("pre-dispatch reservation was falsely reported as worker execution")
	}
	if worker.beginDispatch("job-before-dispatch", 1) {
		t.Fatal("terminalized pre-dispatch reservation still allowed a job frame")
	}
	running, capacity := worker.load()
	if running != 1 || capacity != 1 {
		t.Fatalf("terminalized reservation was released without dispatch proof: %d/%d", running, capacity)
	}
	worker.release("job-before-dispatch")
}

func TestWorkerReservationCancellationAfterDispatchRequiresWorkerCancel(t *testing.T) {
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve("job-after-dispatch") || !worker.beginDispatch("job-after-dispatch", 3) {
		t.Fatal("test dispatch could not begin")
	}
	if !worker.markStoreTerminal("job-after-dispatch") {
		t.Fatal("dispatched reservation did not retain worker-cancel evidence")
	}
	if worker.needsStaleRecovery() {
		t.Fatal("terminalized dispatched reservation remained a stale-store candidate")
	}
	running, capacity := worker.load()
	if running != 1 || capacity != 1 {
		t.Fatalf("dispatched reservation released before worker completion: %d/%d", running, capacity)
	}
	if worker.matchesDispatch("job-after-dispatch", 2) || !worker.matchesDispatch("job-after-dispatch", 3) {
		t.Fatal("reservation did not preserve the exact dispatched assignment attempt")
	}
}

func TestRelayCancelBeforeDispatchReleasesBrowserSessionLock(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	producerToken, _, err := relay.store.CreateToken("producer", "producer-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", SessionID: "cancel-before-dispatch"}
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = relay.store.AssignBrowserJob(job.ID, "node-a", 42, false)
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve(job.ID) {
		t.Fatal("test worker slot could not be reserved")
	}
	relay.mu.Lock()
	relay.workers["node-a"] = worker
	relay.mu.Unlock()

	request := httptest.NewRequest(http.MethodDelete, "/v1/cluster/jobs/"+job.ID, nil)
	request.Header.Set("Authorization", "Bearer "+producerToken)
	response := httptest.NewRecorder()
	relay.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("cancel returned %d: %s", response.Code, response.Body.String())
	}
	if busy, err := relay.store.BrowserSessionBusy("producer-a", requirements, ""); err != nil || busy {
		t.Fatalf("never-dispatched cancellation retained the session lock: busy=%v err=%v", busy, err)
	}
	if worker.beginDispatch(job.ID, job.Attempt) {
		t.Fatal("cancelled pre-dispatch reservation still began dispatch")
	}
}

func TestMatchingResultReleasesReservationAfterCancelledRecordIsPruned(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Provider: "ollama"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve(job.ID) || !worker.beginDispatch(job.ID, job.Attempt) {
		t.Fatal("test dispatch could not begin")
	}
	cancelled, err := store.CancelJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled.FinishedAt = time.Now().UTC().Add(-time.Minute)
	if err := store.SaveJob(cancelled); err != nil {
		t.Fatal(err)
	}

	newer, err := store.CreateJob(SubmitRequest{Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelJob(newer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneRetention(time.Now().UTC(), RetentionPolicy{
		MaxAge: 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetJob(job.ID); err == nil {
		t.Fatal("older cancelled job was not pruned by the bounded retention policy")
	}
	if _, err := store.CompleteJob(job.ID, "node-a", job.Attempt, nil, nil, Usage{}, "cancelled"); err == nil {
		t.Fatal("pruned job unexpectedly accepted a persisted result")
	}
	if !worker.matchesDispatch(job.ID, job.Attempt) {
		t.Fatal("matching result lost the in-memory proof needed to release the slot")
	}
	worker.release(job.ID)
	if running, _ := worker.load(); running != 0 {
		t.Fatal("matching result could not release the retained slot")
	}
}

func TestCompactJobResponseOmitsKnownInputButKeepsResult(t *testing.T) {
	job := Job{ID: "job-1", Payload: json.RawMessage(`{"prompt":"private"}`), SealedPayload: &SealedEnvelope{Ciphertext: "secret"}, Result: json.RawMessage(`{"output":{"text":"answer"}}`), Status: JobCompleted}
	full := jobResponse(job, false)
	if len(full.Payload) == 0 || full.SealedPayload == nil {
		t.Fatal("ordinary job response was unexpectedly compacted")
	}
	compact := jobResponse(job, true)
	if len(compact.Payload) != 0 || compact.SealedPayload != nil || len(compact.Result) == 0 || compact.ID != "job-1" {
		t.Fatalf("compact job response lost output or retained input: %#v", compact)
	}
}

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
	assignmentRequest := AssignmentRequest{TenantID: "tenant-a", Requirements: Requirements{Task: "generation", Group: "fast"}}
	postTest(t, server.URL+"/v1/cluster/assign", producer, assignmentRequest, &assignment)
	encryptionContext, err := ValidateAssignmentResponse(assignmentRequest, assignment, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if assignment.Assignment.OwnerSubject != "test" || assignment.Assignment.TenantID != "tenant-a" || assignment.Assignment.Attempt != 1 {
		t.Fatalf("reservation did not bind authenticated namespace and first attempt: %#v", assignment.Assignment)
	}
	plain := json.RawMessage(`{"prompt":"relay must not see this"}`)
	envelope, shared, err := SealFor(assignment.Assignment.PublicKey, plain, JobAAD(encryptionContext))
	if err != nil {
		t.Fatal(err)
	}
	var submitted Job
	postTest(t, server.URL+"/v1/cluster/jobs", producer, SubmitRequest{TenantID: assignment.Assignment.TenantID, Requirements: assignment.Assignment.Requirements, Sealed: envelope, AssignmentID: assignment.Assignment.ID, AssignmentSecret: assignment.Secret}, &submitted)
	if err := ValidateEncryptedJobContext(encryptionContext, submitted); err != nil {
		t.Fatal(err)
	}
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
	workerContext, contextErr := wire.Job.EncryptionContextForNode(node.ID)
	if contextErr != nil || !workerContext.Equal(encryptionContext) {
		t.Fatalf("worker received a different encryption context: %#v, %v", workerContext, contextErr)
	}
	decrypted, workerShared, err := OpenWith(privateKey, wire.Job.SealedPayload, JobAAD(workerContext))
	if err != nil || !bytes.Equal(decrypted, plain) {
		t.Fatalf("worker could not decrypt: %v", err)
	}
	sealedResult, err := SealResponse(workerShared, []byte(`{"answer":"done"}`), ResultAAD(workerContext))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "result", JobID: wire.Job.ID, Attempt: wire.Job.Attempt, SealedResult: sealedResult, Usage: Usage{InputTokens: 4, OutputTokens: 2}})); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := relay.store.GetJob(wire.Job.ID)
		if job.Status == JobCompleted {
			opened, err := OpenResponse(shared, job.SealedResult, ResultAAD(encryptionContext))
			if err != nil || string(opened) != `{"answer":"done"}` {
				t.Fatalf("producer could not decrypt: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}

func TestNewWorkerConnectionSurvivesReplacedConnectionCleanup(t *testing.T) {
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
	nodeID := "reconnecting-worker"
	token, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	node := Node{ID: nodeID, Name: nodeID, PublicKey: publicKey, Capabilities: Capabilities{Tasks: []string{"generation"}, Providers: []string{"browser"}, MaxConcurrent: 1}}
	connect := func() *websocket.Conn {
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/cluster/workers/connect"
		conn, _, dialErr := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		if writeErr := conn.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); writeErr != nil {
			t.Fatal(writeErr)
		}
		return conn
	}

	first := connect()
	defer first.CloseNow()
	waitFor(t, 2*time.Second, func() bool {
		saved, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && saved.Connected
	}, "first worker did not connect")
	second := connect()
	defer second.CloseNow()
	time.Sleep(200 * time.Millisecond)
	saved, err := relay.store.GetNode(nodeID)
	if err != nil || !saved.Connected {
		t.Fatalf("replacement worker was marked offline by old cleanup: %#v, %v", saved, err)
	}
	relay.mu.RLock()
	current := relay.workers[nodeID]
	relay.mu.RUnlock()
	if current == nil {
		t.Fatal("replacement worker connection was removed")
	}
}

func TestRelayCancellationDoesNotSendPhantomCancelForQueuedSealedBinding(t *testing.T) {
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
	nodeID := "queued-sealed-cancel-worker"
	nodeToken, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/cluster/workers/connect"
	connection, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + nodeToken}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	node := Node{ID: nodeID, Name: nodeID, PublicKey: publicKey, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 1}}
	if err := connection.Write(context.Background(), websocket.MessageText, mustJSON(WireMessage{Version: ProtocolVersion, Type: "hello", Node: &node})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		saved, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && saved.Connected
	}, "worker did not connect")

	job, err := relay.store.CreateJob(SubmitRequest{
		OwnerSubject: "producer",
		Requirements: Requirements{Task: "generation"},
		Sealed:       &SealedEnvelope{Algorithm: sealedAlgorithm},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Consuming an E2EE reservation creates this durable queue binding before
	// dispatch. It names a worker but deliberately has no live slot reservation.
	job.AssignedNode = nodeID
	if err := relay.store.SaveJob(job); err != nil {
		t.Fatal(err)
	}

	request, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/cluster/jobs/"+job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+admin)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", response.StatusCode)
	}

	readContext, cancelRead := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelRead()
	if _, raw, readErr := connection.Read(readContext); readErr == nil {
		t.Fatalf("queued sealed binding emitted a phantom worker message: %s", raw)
	}
	cancelled, err := relay.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != JobCancelled {
		t.Fatalf("job status = %q, want %q", cancelled.Status, JobCancelled)
	}
}

func TestRelayMaintenanceCadenceAvoidsIdleWriteTransactions(t *testing.T) {
	cfg := RelayConfig{
		DispatchEvery: 250 * time.Millisecond,
		AssignmentTTL: 2 * time.Minute,
		JobTimeout:    15 * time.Minute,
	}
	if got := relayMaintenanceInterval(cfg); got != 5*time.Second {
		t.Fatalf("default maintenance interval = %s, want 5s", got)
	}

	short := cfg
	short.AssignmentTTL = time.Second
	if got := relayMaintenanceInterval(short); got != 250*time.Millisecond {
		t.Fatalf("short reservation maintenance interval = %s, want dispatch cadence", got)
	}

	relay := &Relay{cfg: cfg}
	now := time.Now().UTC()
	if !relay.maintenanceDue(now) {
		t.Fatal("first maintenance opportunity was skipped")
	}
	if relay.maintenanceDue(now.Add(time.Second)) {
		t.Fatal("idle dispatcher repeated maintenance before its cadence")
	}
	if !relay.maintenanceDue(now.Add(5 * time.Second)) {
		t.Fatal("maintenance did not become due at its cadence")
	}
}

func TestRelayIdempotencyPreventsDuplicateAdmissionAndScopesKeysByProducer(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_token_long_enough_for_idempotency",
		AllowedTasks: []string{"generation"}, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	producerA, _, err := relay.store.CreateToken("producer", "producer-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	producerB, _, err := relay.store.CreateToken("producer", "producer-b", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	submit := func(token, key, prompt string, duplicateHeader bool) (*httptest.ResponseRecorder, Job) {
		t.Helper()
		input := SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":` + fmt.Sprintf("%q", prompt) + `}`)}
		raw, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/cluster/jobs?compact=1", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Add("Idempotency-Key", key)
		if duplicateHeader {
			request.Header.Add("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		relay.Handler().ServeHTTP(response, request)
		var job Job
		if response.Code >= 200 && response.Code < 300 {
			if decodeErr := json.Unmarshal(response.Body.Bytes(), &job); decodeErr != nil {
				t.Fatal(decodeErr)
			}
		}
		return response, job
	}

	firstResponse, first := submit(producerA, "checkout-42", "run once", false)
	if firstResponse.Code != http.StatusAccepted || first.ID == "" {
		t.Fatalf("first admission returned %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	replayResponse, replay := submit(producerA, "checkout-42", "run once", false)
	if replayResponse.Code != http.StatusOK || replayResponse.Header().Get("Idempotency-Replayed") != "true" || replay.ID != first.ID {
		t.Fatalf("retry returned %d, header %q, job %#v", replayResponse.Code, replayResponse.Header().Get("Idempotency-Replayed"), replay)
	}
	conflict, _ := submit(producerA, "checkout-42", "different work", false)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed request returned %d: %s", conflict.Code, conflict.Body.String())
	}
	otherResponse, other := submit(producerB, "checkout-42", "different work", false)
	if otherResponse.Code != http.StatusAccepted || other.ID == first.ID {
		t.Fatalf("producer-scoped key returned %d, job %#v", otherResponse.Code, other)
	}
	invalid, _ := submit(producerA, "contains space", "invalid", false)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid key returned %d: %s", invalid.Code, invalid.Body.String())
	}
	multiple, _ := submit(producerA, "twice", "invalid", true)
	if multiple.Code != http.StatusBadRequest {
		t.Fatalf("multiple key headers returned %d: %s", multiple.Code, multiple.Body.String())
	}
	events, err := relay.store.ListEvents(20)
	if err != nil {
		t.Fatal(err)
	}
	queuedEvents := 0
	for _, event := range events {
		if event.Kind == "job.queued" {
			queuedEvents++
		}
	}
	if queuedEvents != 2 {
		t.Fatalf("replay emitted a duplicate queue event: %d", queuedEvents)
	}
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
