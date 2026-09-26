package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPipelineCheckpointFailureStopsBeforeExternalFollowup(t *testing.T) {
	relay := newPipelineCheckpointRelay(t)
	defer relay.Close()
	run := PipelineRun{ID: "checkpoint-before-followup", Pipeline: "checkpoint-test", OwnerSubject: "owner-a", Status: "running", Input: json.RawMessage(`{"input":true}`), CreatedAt: time.Now().UTC()}
	if err := relay.store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	checkpointErr := errors.New("injected running checkpoint failure")
	var failed atomic.Bool
	relay.store.savePipelineRunTestHook = func(saved PipelineRun) error {
		if saved.Status == "running" && len(saved.Steps) == 1 && saved.Steps[0].Status == JobCompleted && failed.CompareAndSwap(false, true) {
			return checkpointErr
		}
		return nil
	}
	pipeline := Pipeline{Steps: []PipelineStep{
		{Name: "first", Requirements: Requirements{Task: "generation"}, Input: `${previous}`},
		{Name: "external-followup", Requirements: Requirements{Task: "generation"}, Input: `${previous}`},
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.executePipeline(context.Background(), run, pipeline)
	}()
	completeNextPipelineJob(t, relay, "first")
	waitPipelineDone(t, done)
	jobs, err := relay.store.ListJobsForOwner(10, "", run.OwnerSubject)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Step != "first" {
		t.Fatalf("checkpoint failure admitted an external follow-up: %#v", jobs)
	}
	stored, err := relay.store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || !strings.Contains(stored.Error, "save checkpoint after step first") || !strings.Contains(stored.Error, checkpointErr.Error()) {
		t.Fatalf("checkpoint failure was not persisted deterministically: %#v", stored)
	}
}

func TestPipelineCompletedCheckpointFailurePersistsFailedTerminalState(t *testing.T) {
	relay := newPipelineCheckpointRelay(t)
	defer relay.Close()
	run := PipelineRun{ID: "completed-checkpoint-failure", Pipeline: "checkpoint-test", OwnerSubject: "owner-a", Status: "running", Input: json.RawMessage(`{"input":true}`), CreatedAt: time.Now().UTC()}
	if err := relay.store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	checkpointErr := errors.New("injected completed checkpoint failure")
	var completedAttempts atomic.Int32
	relay.store.savePipelineRunTestHook = func(saved PipelineRun) error {
		if saved.Status == "completed" {
			completedAttempts.Add(1)
			return checkpointErr
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.executePipeline(context.Background(), run, Pipeline{Steps: []PipelineStep{{Name: "only", Requirements: Requirements{Task: "generation"}, Input: `${previous}`}}})
	}()
	completeNextPipelineJob(t, relay, "only")
	waitPipelineDone(t, done)
	if completedAttempts.Load() != 1 {
		t.Fatalf("completed checkpoint attempts = %d, want 1", completedAttempts.Load())
	}
	stored, err := relay.store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || !strings.Contains(stored.Error, "save completed pipeline") || !strings.Contains(stored.Error, checkpointErr.Error()) || stored.FinishedAt.IsZero() {
		t.Fatalf("completed checkpoint failure was not converted to a deterministic terminal failure: %#v", stored)
	}
}

func newPipelineCheckpointRelay(t *testing.T) *Relay {
	t.Helper()
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "checkpoint_admin_012345678901234567890123456789",
		AllowedTasks: []string{"generation"}, MaxQueuedJobs: 20, MaxPipelineRuntime: 10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

func completeNextPipelineJob(t *testing.T, relay *Relay, step string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := relay.store.ListJobsForOwner(20, JobQueued, "owner-a")
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if job.Step != step {
				continue
			}
			assigned, err := relay.store.AssignJob(job.ID, "checkpoint-node")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := relay.store.CompleteJob(job.ID, "checkpoint-node", assigned.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, ""); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pipeline step %q was not admitted", step)
}

func waitPipelineDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not finish")
	}
}
