package cluster

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	if err != nil || len(requeued) != 1 || requeued[0].Status != JobQueued {
		t.Fatal("ordinary job was not requeued")
	}
	queued, _ = store.QueuedJobs(10)
	if len(queued) != 2 || queued[0].ID != high.ID {
		t.Fatal("requeued job lost its priority")
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
	if normal.Status != JobQueued || sealed.Status != JobFailed {
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
	if _, err := store.MarkRunning(job.ID, "node-a"); err != nil {
		t.Fatal(err)
	}
	progress := JobProgress{Sequence: 1, Text: "partial", Phase: "generating", Busy: true}
	if _, err := store.UpdateJobProgress(job.ID, "node-a", progress); err != nil {
		t.Fatal(err)
	}
	job, err = store.GetJob(job.ID)
	if err != nil || job.Progress == nil || job.Progress.Text != "partial" || job.Progress.Sequence != 1 {
		t.Fatalf("progress was not stored: %#v %v", job.Progress, err)
	}
	if _, err := store.UpdateJobProgress(job.ID, "different-node", JobProgress{Sequence: 2, Text: "wrong"}); err == nil {
		t.Fatal("a different node must not update job progress")
	}
	job, err = store.CompleteJob(job.ID, json.RawMessage(`{"ok":true}`), nil, Usage{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if job.Progress == nil || job.Progress.Sequence != 2 || job.Progress.Phase != "final" || job.Progress.Busy {
		t.Fatalf("successful completion did not finalize progress: %#v", job.Progress)
	}
}
