package cluster

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestRelayAuthorityPersistsClusterAndAdvancesEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	firstStore, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstStore.AcquireRelayAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Valid() || first.Epoch != 1 {
		t.Fatalf("first authority = %#v", first)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	second, err := secondStore.AcquireRelayAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if second.ClusterID != first.ClusterID || second.Epoch != first.Epoch+1 {
		t.Fatalf("authority did not advance monotonically: first=%#v second=%#v", first, second)
	}
}

func TestRelayAuthorityRejectsCorruptOrExhaustedEpoch(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "corrupt", raw: []byte{1}},
		{name: "exhausted", raw: func() []byte {
			raw := make([]byte, 8)
			binary.BigEndian.PutUint64(raw, math.MaxUint64)
			return raw
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.db.Update(func(tx *bolt.Tx) error {
				meta := tx.Bucket(bucketStoreMeta)
				if err := meta.Put(keyClusterID, []byte("cluster_test")); err != nil {
					return err
				}
				return meta.Put(keyRelayEpoch, test.raw)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AcquireRelayAuthority(); err == nil {
				t.Fatal("invalid durable epoch was accepted")
			}
		})
	}
}

func TestFencedAssignmentRejectsStaleWorkerTransitions(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authority, err := store.AcquireRelayAuthority()
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Task: "generation"}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer", Requirements: requirements, Payload: json.RawMessage(`{"prompt":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	node := Node{ID: "node-a", Name: "Node A", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 1}}
	decision := ExplainRouting([]Node{node}, requirements, 0)
	assigned, err := store.AssignJobFencedWithDecision(job.ID, node.ID, decision, authority)
	if err != nil {
		t.Fatal(err)
	}
	if assigned.AssignmentFence == nil || assigned.AssignmentFence.ClusterID != authority.ClusterID || assigned.AssignmentFence.RelayEpoch != authority.Epoch || assigned.AssignmentFence.Generation != uint64(assigned.Attempt) {
		t.Fatalf("invalid durable assignment fence: %#v", assigned.AssignmentFence)
	}
	stale := *assigned.AssignmentFence
	stale.RelayEpoch++
	if _, err := store.MarkRunningFenced(job.ID, node.ID, assigned.Attempt, &stale); !errors.Is(err, ErrAssignmentFenceMismatch) {
		t.Fatalf("stale start returned %v", err)
	}
	if _, err := store.MarkRunningFenced(job.ID, node.ID, assigned.Attempt, assigned.AssignmentFence); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJobWithFailureFenced(job.ID, node.ID, assigned.Attempt, &stale, json.RawMessage(`{"text":"wrong"}`), nil, Usage{}, "", ""); !errors.Is(err, ErrAssignmentFenceMismatch) {
		t.Fatalf("stale result returned %v", err)
	}
	current, err := store.GetJob(job.ID)
	if err != nil || current.Status != JobRunning {
		t.Fatalf("stale result mutated job: %#v, %v", current, err)
	}
	completed, err := store.CompleteJobWithFailureFenced(job.ID, node.ID, assigned.Attempt, assigned.AssignmentFence, json.RawMessage(`{"text":"ok"}`), nil, Usage{}, "", "")
	if err != nil || completed.Status != JobCompleted {
		t.Fatalf("matching result was rejected: %#v, %v", completed, err)
	}
}

func TestAssignmentGenerationOverflowFailsWithoutMutation(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authority, err := store.AcquireRelayAuthority()
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Task: "generation"}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer", Requirements: requirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job.Attempt = math.MaxInt
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	node := Node{ID: "node-a", Name: "Node A", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 1}}
	decision := ExplainRouting([]Node{node}, requirements, 0)
	if _, err := store.AssignJobFencedWithDecision(job.ID, node.ID, decision, authority); err == nil {
		t.Fatal("overflowing assignment generation was accepted")
	}
	stored, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobQueued || stored.Attempt != math.MaxInt || stored.AssignmentFence != nil {
		t.Fatalf("failed overflowing assignment mutated the job: %#v", stored)
	}
}

func TestWorkerPersistsAndRejectsOlderRelayAuthority(t *testing.T) {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	identity := WorkerIdentity{
		NodeID: "node-a", NodeToken: "cb_node_0123456789012345678901234567890123456789",
		PrivateKey: privateKey, PublicKey: publicKey, RelayURL: "http://127.0.0.1:32150",
	}
	if err := saveIdentity(identityPath, identity); err != nil {
		t.Fatal(err)
	}
	worker, err := LoadWorker(WorkerConfig{RelayURL: identity.RelayURL, IdentityFile: identityPath})
	if err != nil {
		t.Fatal(err)
	}
	authority := RelayAuthority{ClusterID: "cluster_test", Epoch: 7}
	if err := worker.acceptRelayAuthority(authority); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadWorker(WorkerConfig{RelayURL: identity.RelayURL, IdentityFile: identityPath})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.identity.ClusterID != authority.ClusterID || reloaded.identity.HighestRelayEpoch != authority.Epoch {
		t.Fatalf("authority fence was not durable: %#v", reloaded.identity)
	}
	if err := reloaded.acceptRelayAuthority(RelayAuthority{ClusterID: authority.ClusterID, Epoch: authority.Epoch - 1}); err == nil {
		t.Fatal("older relay epoch was accepted")
	}
	if err := reloaded.acceptRelayAuthority(RelayAuthority{ClusterID: "cluster_foreign", Epoch: authority.Epoch + 1}); err == nil {
		t.Fatal("foreign cluster identity was accepted")
	}
	job := Job{ID: "job-a", Attempt: 2, AssignmentFence: &AssignmentFence{ClusterID: authority.ClusterID, RelayEpoch: authority.Epoch, Generation: 2}}
	if err := reloaded.validateJobFence(job); err != nil {
		t.Fatal(err)
	}
	job.AssignmentFence.Generation = 1
	if err := reloaded.validateJobFence(job); err == nil {
		t.Fatal("wrong assignment generation was accepted")
	}
}

func TestWorkerReservationDoesNotReleaseForAnotherFence(t *testing.T) {
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve("job-a") {
		t.Fatal("reserve failed")
	}
	fence := &AssignmentFence{ClusterID: "cluster_test", RelayEpoch: 2, Generation: 1}
	if !worker.beginDispatch("job-a", 1, fence) {
		t.Fatal("dispatch failed")
	}
	stale := *fence
	stale.RelayEpoch--
	if worker.matchesDispatch("job-a", 1, &stale) {
		t.Fatal("stale fence matched a live worker slot")
	}
	if !worker.matchesDispatch("job-a", 1, fence) {
		t.Fatal("matching fence did not match live worker slot")
	}
}
