package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAtomicQueueAdmissionNeverExceedsCapacity(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const capacity = 7
	const contenders = 48
	start := make(chan struct{})
	var admitted atomic.Int32
	var full atomic.Int32
	var unexpected atomic.Value
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, createErr := store.CreateJobAdmitted(SubmitRequest{Payload: json.RawMessage(`{"prompt":"bounded"}`)}, capacity)
			switch {
			case createErr == nil:
				admitted.Add(1)
			case errors.Is(createErr, ErrQueueFull):
				full.Add(1)
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
	if got := admitted.Load(); got != capacity {
		t.Fatalf("admitted %d jobs, want exactly %d", got, capacity)
	}
	if got := full.Load(); got != contenders-capacity {
		t.Fatalf("reported %d full admissions, want %d", got, contenders-capacity)
	}
	if got, err := store.CountJobs(JobQueued); err != nil || got != capacity {
		t.Fatalf("queue contains %d jobs, want %d: %v", got, capacity, err)
	}
}

func TestReservationAdmissionGCAndOwnerBinding(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	newAssignment := func(id, job string, expires time.Time) Assignment {
		return Assignment{ID: id, JobID: job, NodeID: "node-a", ExpiresAt: expires, Requirements: Requirements{Task: "generation"}}
	}
	now := time.Now().UTC()
	if err := store.CreateReservationAdmitted(newAssignment("assignment-a1", "job-a1", now.Add(time.Minute)), "secret-a1", "owner-a", 3, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateReservationAdmitted(newAssignment("assignment-a2", "job-a2", now.Add(time.Minute)), "secret-a2", "owner-a", 3, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateReservationAdmitted(newAssignment("assignment-a3", "job-a3", now.Add(time.Minute)), "secret-a3", "owner-a", 3, 2); !errors.Is(err, ErrOwnerReservationCapacity) {
		t.Fatalf("per-owner reservation bound returned %v", err)
	}
	if err := store.CreateReservationAdmitted(newAssignment("assignment-b1", "job-b1", now.Add(time.Minute)), "secret-b1", "owner-b", 3, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateReservationAdmitted(newAssignment("assignment-b2", "job-b2", now.Add(time.Minute)), "secret-b2", "owner-b", 3, 2); !errors.Is(err, ErrReservationCapacity) {
		t.Fatalf("global reservation bound returned %v", err)
	}

	sealed := &SealedEnvelope{Algorithm: sealedAlgorithm}
	if _, err := store.ConsumeReservationAdmitted("assignment-a1", "secret-a1", sealed, "test", "", "owner-b", 0, 1, 10); !errors.Is(err, ErrReservationOwnerMismatch) {
		t.Fatalf("foreign producer consumed reservation: %v", err)
	}
	job, err := store.ConsumeReservationAdmitted("assignment-a1", "secret-a1", sealed, "test", "", "owner-a", 0, 1, 10)
	if err != nil || job.OwnerSubject != "owner-a" || job.Status != JobQueued {
		t.Fatalf("rightful producer could not consume reservation: %#v, %v", job, err)
	}

	shortExpiry := time.Now().UTC().Add(15 * time.Millisecond)
	if err := store.CreateReservationAdmitted(newAssignment("assignment-expiring", "job-expiring", shortExpiry), "secret-expiring", "owner-c", 10, 10); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	removed, err := store.GarbageCollectReservations(time.Now().UTC())
	if err != nil || removed != 1 {
		t.Fatalf("expired reservation GC removed %d, want 1: %v", removed, err)
	}
	if _, err := store.ConsumeReservationAdmitted("assignment-expiring", "secret-expiring", sealed, "test", "", "owner-c", 0, 1, 10); err == nil {
		t.Fatal("garbage-collected reservation remained consumable")
	}
}

func TestAtomicPerOwnerReservationAdmission(t *testing.T) {
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
			assignment := Assignment{
				ID:        fmt.Sprintf("assignment-owner-%d", index),
				JobID:     fmt.Sprintf("job-owner-%d", index),
				ExpiresAt: time.Now().UTC().Add(time.Minute),
			}
			createErr := store.CreateReservationAdmitted(assignment, fmt.Sprintf("secret-%d", index), "one-owner", 100, ownerCapacity)
			switch {
			case createErr == nil:
				admitted.Add(1)
			case errors.Is(createErr, ErrOwnerReservationCapacity):
				limited.Add(1)
			default:
				unexpected.Store(createErr)
			}
		}()
	}
	close(start)
	wait.Wait()
	if value := unexpected.Load(); value != nil {
		t.Fatalf("unexpected reservation admission error: %v", value)
	}
	if got := admitted.Load(); got != ownerCapacity {
		t.Fatalf("admitted %d owner reservations, want %d", got, ownerCapacity)
	}
	if got := limited.Load(); got != contenders-ownerCapacity {
		t.Fatalf("limited %d owner reservations, want %d", got, contenders-ownerCapacity)
	}
}

func TestFullQueueDoesNotConsumeReservation(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	queued, err := store.CreateJobAdmitted(SubmitRequest{Payload: json.RawMessage(`{}`)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	assignment := Assignment{ID: "assignment-held", JobID: "job-held", NodeID: "node-a", ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if err := store.CreateReservationAdmitted(assignment, "held-secret", "owner-a", 10, 10); err != nil {
		t.Fatal(err)
	}
	sealed := &SealedEnvelope{Algorithm: sealedAlgorithm}
	if _, err := store.ConsumeReservationAdmitted(assignment.ID, "held-secret", sealed, "", "", "owner-a", 0, 1, 1); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue returned %v", err)
	}
	if _, err := store.CancelJob(queued.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeReservationAdmitted(assignment.ID, "held-secret", sealed, "", "", "owner-a", 0, 1, 1); err != nil {
		t.Fatalf("queue-full attempt consumed the reservation: %v", err)
	}
}

func TestRenderedPipelinePayloadHonorsExactByteLimit(t *testing.T) {
	value := json.RawMessage(`{"message":"ä"}`)
	values := map[string]json.RawMessage{"input": value}
	exact := int64(len(value))
	rendered, err := renderPipelineInputBounded("${input}", values, exact)
	if err != nil || string(rendered) != string(value) {
		t.Fatalf("exact-size rendered payload rejected: %q, %v", rendered, err)
	}
	if _, err := renderPipelineInputBounded("${input}", values, exact-1); err == nil {
		t.Fatal("one-byte oversized rendered payload was accepted")
	} else {
		var limitErr *payloadLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("oversized render returned the wrong error: %T %v", err, err)
		}
	}
}

func TestPipelineTimeoutCancelsJobAndRejectsLateResult(t *testing.T) {
	for _, initial := range []string{JobQueued, JobRunning} {
		t.Run(initial, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			job, err := store.CreateJob(SubmitRequest{Payload: json.RawMessage(`{}`), MaxAttempts: 3})
			if err != nil {
				t.Fatal(err)
			}
			if initial == JobRunning {
				job, err = store.AssignJob(job.ID, "node-a")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.MarkRunning(job.ID, "node-a", job.Attempt); err != nil {
					t.Fatal(err)
				}
			}
			relay := &Relay{store: store}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			cancelled, err := relay.waitJob(ctx, job.ID, 30)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("wait returned %v, want deadline exceeded", err)
			}
			if cancelled.ID != job.ID || cancelled.Status != JobCancelled {
				t.Fatalf("underlying job was not cancelled: %#v", cancelled)
			}
			if initial == JobRunning {
				if _, err := store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"late":true}`), nil, Usage{}, ""); err == nil {
					t.Fatal("late worker result was accepted after pipeline timeout")
				}
			}
			latest, err := store.GetJob(job.ID)
			if err != nil || latest.Status != JobCancelled {
				t.Fatalf("cancelled job changed state: %#v, %v", latest, err)
			}
			queued, err := store.QueuedJobs(10)
			if err != nil || len(queued) != 0 {
				t.Fatalf("timed-out job was silently retried: %#v, %v", queued, err)
			}
		})
	}
}
