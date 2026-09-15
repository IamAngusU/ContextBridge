package cluster

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCleanLabelPreservesUTF8AtByteBoundary(t *testing.T) {
	for _, limit := range []int{3, 4, 5} {
		got := cleanLabel("ab😀cd", limit)
		if !utf8.ValidString(got) || len(got) > limit || got != "ab" {
			t.Fatalf("cleanLabel at %d bytes = %q", limit, got)
		}
	}
	if got := cleanLabel("ab😀cd", 6); got != "ab😀" {
		t.Fatalf("cleanLabel at rune boundary = %q", got)
	}
}

func TestTokenIdentityRejectsSharedEmptyProducerAndDisplayControls(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, test := range []struct {
		role    string
		subject string
		groups  []string
	}{
		{role: "producer"},
		{role: "node"},
		{role: "producer", subject: "producer\u202eadmin"},
		{role: "producer", subject: "producer", groups: []string{"safe", "bad\nline"}},
	} {
		if _, _, err := store.CreateToken(test.role, test.subject, test.groups, time.Hour); err == nil {
			t.Fatalf("accepted invalid token identity: %#v", test)
		}
	}
	if _, _, err := store.CreateToken("producer", "producer-a", []string{"default"}, time.Hour); err != nil {
		t.Fatalf("valid producer identity rejected: %v", err)
	}
}

func TestEstimateVRAMIgnoresLegacyNodeWideMeasurements(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index := 0; index < 3; index++ {
		job, createErr := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Model: "model-a"}, Payload: json.RawMessage(`{}`)})
		if createErr != nil {
			t.Fatal(createErr)
		}
		assigned, assignErr := store.AssignJob(job.ID, "node-a")
		if assignErr != nil {
			t.Fatal(assignErr)
		}
		if _, completeErr := store.CompleteJob(job.ID, "node-a", assigned.Attempt, json.RawMessage(`{}`), nil, Usage{PeakVRAMBytes: 20 << 30}, ""); completeErr != nil {
			t.Fatal(completeErr)
		}
	}
	if got := store.EstimateVRAM(Requirements{Task: "generation", Model: "model-a"}); got != 0 {
		t.Fatalf("legacy node-wide VRAM was reused as a job estimate: %d", got)
	}
	for index := 0; index < 3; index++ {
		job, createErr := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Model: "model-b"}, Payload: json.RawMessage(`{}`)})
		if createErr != nil {
			t.Fatal(createErr)
		}
		assigned, assignErr := store.AssignJob(job.ID, "node-b")
		if assignErr != nil {
			t.Fatal(assignErr)
		}
		if _, completeErr := store.CompleteJob(job.ID, "node-b", assigned.Attempt, json.RawMessage(`{}`), nil, Usage{ResourceScope: "job", PeakVRAMBytes: 4 << 30}, ""); completeErr != nil {
			t.Fatal(completeErr)
		}
	}
	if got, want := store.EstimateVRAM(Requirements{Task: "generation", Model: "model-b"}), uint64((4<<30)+(4<<30)/10); got != want {
		t.Fatalf("job-attributed VRAM estimate = %d, want %d", got, want)
	}
}

func TestNodesWithSameDisplayNameRemainDistinct(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"node_alpha123", "node_beta456"} {
		if err := store.UpsertNode(Node{ID: id, Name: "Shared PC"}); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := store.ListNodes()
	if err != nil || len(nodes) != 2 || nodes[0].ID == nodes[1].ID {
		t.Fatalf("same-name nodes collided: %#v, %v", nodes, err)
	}
}

func TestStorePersistsQueueAndOneTimePairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	low, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"low"}`), Priority: -10})
	if err != nil {
		t.Fatal(err)
	}
	high, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"high"}`), Priority: 50})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.QueuedJobs(10)
	if err != nil || len(queued) != 2 || queued[0].ID != high.ID {
		t.Fatalf("priority queue failed: %#v, %v", queued, err)
	}
	if _, err := store.AssignJob(high.ID, "node-one"); err != nil {
		t.Fatal(err)
	}
	queued, _ = store.QueuedJobs(10)
	if len(queued) != 1 || queued[0].ID != low.ID {
		t.Fatal("assigned job remained in queue")
	}
	requeued, err := store.RequeueNode("node-one", "disconnect")
	if err != nil || len(requeued) != 1 || requeued[0].Status != JobFailed {
		t.Fatal("ambiguous disconnected job did not fail closed")
	}
	queued, _ = store.QueuedJobs(10)
	if len(queued) != 1 || queued[0].ID != low.ID {
		t.Fatal("disconnected in-flight job was silently requeued")
	}
	_, publicKey, _ := NewIdentity()
	pair, err := store.CreatePairing(PairRequest{NodeName: "workstation", PublicKey: publicKey}, "https://relay.test/#pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecidePairing(pair.UserCode, true); err != nil {
		t.Fatal(err)
	}
	state, _, token, err := store.PollPairing(pair.DeviceCode)
	if err != nil || state != "approved" || token == "" {
		t.Fatalf("pairing failed: %s %v", state, err)
	}
	issuedToken := token
	state, _, token, err = store.PollPairing(pair.DeviceCode)
	if err != nil || state != "authorization_pending" || token != "" {
		t.Fatal("node token must only be returned once")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.GetJob(low.ID)
	if err != nil || job.ID != low.ID {
		t.Fatal("job did not survive restart")
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(issuedToken)) {
		t.Fatal("database exposed a raw token")
	}
}

func TestStoreRecoversStaleJobsWithEncryptionBoundary(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	normal, _ := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), MaxAttempts: 2})
	normal, _ = store.AssignJob(normal.ID, "node-a")
	normal.AssignedAt = time.Now().Add(-time.Hour)
	_ = store.SaveJob(normal)
	sealed, _ := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Sealed: &SealedEnvelope{Algorithm: sealedAlgorithm}, MaxAttempts: 1})
	sealed.AssignedNode, sealed.CreatedAt = "node-b", time.Now().Add(-time.Hour)
	_ = store.SaveJob(sealed)
	recovered, err := store.RecoverStaleJobs(time.Now(), time.Minute, time.Minute)
	if err != nil || len(recovered) != 2 {
		t.Fatalf("stale recovery failed: %v %#v", err, recovered)
	}
	normal, _ = store.GetJob(normal.ID)
	sealed, _ = store.GetJob(sealed.ID)
	if normal.Status != JobFailed || sealed.Status != JobFailed {
		t.Fatalf("unexpected recovery states: %s %s", normal.Status, sealed.Status)
	}
}

func TestStoreTracksProgressOnlyForAssignedPlaintextJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "browser"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(job.ID, "node-a", job.Attempt); err != nil {
		t.Fatal(err)
	}
	progress := JobProgress{Sequence: 1, Text: "partial", Phase: "generating", Busy: true}
	if _, err := store.UpdateJobProgress(job.ID, "node-a", job.Attempt, progress); err != nil {
		t.Fatal(err)
	}
	job, err = store.GetJob(job.ID)
	if err != nil || job.Progress == nil || job.Progress.Text != "partial" || job.Progress.Sequence != 1 {
		t.Fatalf("progress was not stored: %#v %v", job.Progress, err)
	}
	if _, err := store.UpdateJobProgress(job.ID, "different-node", job.Attempt, JobProgress{Sequence: 2, Text: "wrong"}); err == nil {
		t.Fatal("a different node must not update job progress")
	}
	job, err = store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if job.Progress == nil || job.Progress.Sequence != 2 || job.Progress.Phase != "final" || job.Progress.Busy {
		t.Fatalf("successful completion did not finalize progress: %#v", job.Progress)
	}
}

func TestStoreKeepsSessionAffinityPrivateToProducer(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation", SessionID: "chat-42"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(job.ID, "browser-node"); err != nil {
		t.Fatal(err)
	}
	if node, ok := store.RecentSessionNode("producer-a", "chat-42"); !ok || node != "browser-node" {
		t.Fatalf("session node was not found: %q %v", node, ok)
	}
	if _, ok := store.RecentSessionNode("producer-b", "chat-42"); ok {
		t.Fatal("session affinity leaked across producer identities")
	}
}

func TestCompleteJobIsBoundToAssignedWorkerAndNeverRetriesAnError(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "assigned-node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteJob(job.ID, "foreign-node", job.Attempt, json.RawMessage(`{"forged":true}`), nil, Usage{}, ""); err == nil {
		t.Fatal("foreign worker completed another worker's job")
	}
	job, _ = store.GetJob(job.ID)
	if job.Status != JobAssigned {
		t.Fatalf("foreign completion mutated job state: %s", job.Status)
	}
	job, err = store.CompleteJob(job.ID, "assigned-node", job.Attempt, nil, nil, Usage{}, "browser_timeout")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobFailed || !strings.Contains(job.Error, "browser_timeout") {
		t.Fatalf("ambiguous worker error was retried or hidden: %#v", job)
	}
	queued, _ := store.QueuedJobs(10)
	if len(queued) != 0 {
		t.Fatal("failed worker completion was silently requeued")
	}
}

func TestCompleteJobRejectsStaleAttemptFromSameWorker(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	first.Status = JobQueued
	if err := store.SaveJob(first); err != nil {
		t.Fatal(err)
	}
	current, err := store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if current.Attempt != first.Attempt+1 {
		t.Fatalf("assignment attempt did not advance: first=%d current=%d", first.Attempt, current.Attempt)
	}
	if _, err := store.CompleteJob(job.ID, "node-a", first.Attempt, json.RawMessage(`{"stale":true}`), nil, Usage{}, ""); err == nil {
		t.Fatal("stale result from the same worker completed the current assignment")
	}
	latest, err := store.GetJob(job.ID)
	if err != nil || latest.Status != JobAssigned || latest.Attempt != current.Attempt || len(latest.Result) != 0 {
		t.Fatalf("stale completion mutated current assignment: %#v, %v", latest, err)
	}
	completed, err := store.CompleteJob(job.ID, "node-a", current.Attempt, json.RawMessage(`{"current":true}`), nil, Usage{}, "")
	if err != nil || completed.Status != JobCompleted {
		t.Fatalf("current assignment could not complete: %#v, %v", completed, err)
	}
}

func TestCompleteJobEnforcesResultEnvelopeParity(t *testing.T) {
	validSealedResult := &SealedEnvelope{
		Algorithm:  sealedAlgorithm,
		Nonce:      encode(make([]byte, 12)),
		Ciphertext: encode(make([]byte, 16)),
	}
	tests := []struct {
		name       string
		sealedJob  bool
		result     json.RawMessage
		sealed     *SealedEnvelope
		wantStatus string
	}{
		{name: "plaintext valid", result: json.RawMessage(`{"ok":true}`), wantStatus: JobCompleted},
		{name: "plaintext malformed", result: json.RawMessage(`{"ok":`), wantStatus: JobFailed},
		{name: "plaintext returned sealed", sealed: validSealedResult, wantStatus: JobFailed},
		{name: "encrypted valid", sealedJob: true, sealed: validSealedResult, wantStatus: JobCompleted},
		{name: "encrypted returned plaintext", sealedJob: true, result: json.RawMessage(`{"leak":true}`), wantStatus: JobFailed},
		{name: "encrypted missing result", sealedJob: true, wantStatus: JobFailed},
		{name: "encrypted malformed nonce", sealedJob: true, sealed: &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: encode(make([]byte, 11)), Ciphertext: encode(make([]byte, 16))}, wantStatus: JobFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			request := SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)}
			if test.sealedJob {
				request.Payload = nil
				request.Sealed = &SealedEnvelope{Algorithm: sealedAlgorithm}
			}
			job, err := store.CreateJob(request)
			if err != nil {
				t.Fatal(err)
			}
			job, err = store.AssignJob(job.ID, "node-a")
			if err != nil {
				t.Fatal(err)
			}
			job, err = store.CompleteJob(job.ID, "node-a", job.Attempt, test.result, test.sealed, Usage{}, "")
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != test.wantStatus {
				t.Fatalf("completion status = %s, want %s (%s)", job.Status, test.wantStatus, job.Error)
			}
			if test.wantStatus == JobFailed && (len(job.Result) != 0 || job.SealedResult != nil || !strings.HasPrefix(job.Error, "worker result rejected:")) {
				t.Fatalf("rejected result was retained or error hidden: %#v", job)
			}
		})
	}
}

func TestWorkerResultSizeBoundary(t *testing.T) {
	objectOfSize := func(size int64) json.RawMessage {
		return json.RawMessage(`{"x":"` + strings.Repeat("a", int(size)-8) + `"}`)
	}
	if err := validateWorkerResult(Job{}, objectOfSize(MaximumJobResultBytes), nil, ""); err != nil {
		t.Fatalf("exact plaintext result limit was rejected: %v", err)
	}
	if err := validateWorkerResult(Job{}, objectOfSize(MaximumJobResultBytes+1), nil, ""); err == nil {
		t.Fatal("plaintext result one byte beyond the limit was accepted")
	}
	sealedJob := Job{SealedPayload: &SealedEnvelope{Algorithm: sealedAlgorithm}}
	exact := &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: encode(make([]byte, 12)), Ciphertext: encode(make([]byte, int(MaximumJobResultBytes+16)))}
	if err := validateWorkerResult(sealedJob, nil, exact, ""); err != nil {
		t.Fatalf("exact encrypted result limit was rejected: %v", err)
	}
	over := &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: exact.Nonce, Ciphertext: encode(make([]byte, int(MaximumJobResultBytes+17)))}
	if err := validateWorkerResult(sealedJob, nil, over, ""); err == nil {
		t.Fatal("encrypted result one byte beyond the limit was accepted")
	}
}
