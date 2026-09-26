package cluster

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDAGChildAdmissionCommitsJobCheckpointAndEventsAtomically(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	run := saveDAGAdmissionRun(t, store, 2, "root", "other")
	request := dagChildRequest(run, "root", "job-dag-root")

	updated, job, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", request, 10, ProducerLimits{MaxQueuedJobs: 5, MaxJobsPerHour: 5})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobQueued || updated.NodeStates[0].State != PipelineNodeQueued || updated.NodeStates[0].JobID != job.ID || len(updated.Steps) != 1 || updated.Steps[0].ID != job.ID {
		t.Fatalf("job and graph checkpoint diverged: %#v %#v", job, updated)
	}
	storedRun, err := store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedJob, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.NodeStates[0].JobID != storedJob.ID || storedJob.ParentID != storedRun.ID || storedJob.Step != "root" {
		t.Fatalf("durable job and graph checkpoint diverged: %#v %#v", storedJob, storedRun)
	}
	events, err := store.ListPipelineEvents(run.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 2 || events.Events[0].Type != "pipeline.started" || events.Events[1].Type != "pipeline.step.queued" || events.Events[1].JobID != job.ID {
		t.Fatalf("unexpected atomic DAG events: %#v", events.Events)
	}
	if _, _, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", request, 10, ProducerLimits{}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("duplicate step admission error = %v", err)
	}
}

func TestDAGChildAdmissionRollsBackEveryOwningRecord(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	run := saveDAGAdmissionRun(t, store, 1, "root")
	request := dagChildRequest(run, "root", "job-rollback")
	store.saveJobEventTestHook = func(event JobEvent) error {
		if event.Type == "pipeline.step.queued" {
			return errors.New("injected pipeline event failure")
		}
		return nil
	}
	if _, _, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", request, 10, ProducerLimits{MaxJobsPerHour: 1}); err == nil {
		t.Fatal("injected event failure did not abort DAG admission")
	}
	store.saveJobEventTestHook = nil
	if _, err := store.GetJob(request.ID); err == nil {
		t.Fatal("child job survived rolled-back DAG admission")
	}
	stored, err := store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NodeStates[0].State != PipelineNodeReady || stored.NodeStates[0].JobID != "" || len(stored.Steps) != 0 {
		t.Fatalf("graph checkpoint survived rolled-back DAG admission: %#v", stored)
	}
	// The producer rate-window write shared the same failed Bolt transaction.
	// A second attempt under a limit of one must therefore still be allowed.
	if _, _, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", request, 10, ProducerLimits{MaxJobsPerHour: 1}); err != nil {
		t.Fatalf("rolled-back rate capacity was still consumed: %v", err)
	}
}

func TestDAGChildAdmissionEnforcesParallelAndRunContext(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	run := saveDAGAdmissionRun(t, store, 1, "first", "second")
	if _, _, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "first", dagChildRequest(run, "first", "job-first"), 10, ProducerLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "second", dagChildRequest(run, "second", "job-second"), 10, ProducerLimits{}); err == nil || !strings.Contains(err.Error(), "max_parallel") {
		t.Fatalf("parallel overflow error = %v", err)
	}
	wrongOwner := dagChildRequest(run, "second", "job-wrong-owner")
	wrongOwner.OwnerSubject = "another-owner"
	if _, _, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "second", wrongOwner, 10, ProducerLimits{}); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("run context mismatch error = %v", err)
	}
}

func TestDAGChildAdmissionPreservesMaximumSafePipelineAndStepNames(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	createdAt := time.Now().UTC()
	step := "s" + strings.Repeat("x", 127)
	pipelineName := "p" + strings.Repeat("y", 127)
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 1, Steps: []PipelineStep{{Name: step, Input: `${input}`}}}
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{ID: "run-max-names", Pipeline: pipelineName, OwnerSubject: "producer-a", Status: "running", Graph: &binding, NodeStates: nodes, CreatedAt: createdAt}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	_, job, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, step, dagChildRequest(run, step, "job-max-names"), 10, ProducerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Pipeline != pipelineName || job.Step != step {
		t.Fatalf("maximum safe names were truncated: pipeline=%d step=%d", len(job.Pipeline), len(job.Step))
	}
}

func openDAGAdmissionStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func saveDAGAdmissionRun(t *testing.T, store *Store, maxParallel int, stepNames ...string) PipelineRun {
	t.Helper()
	createdAt := time.Now().UTC()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: maxParallel}
	for _, name := range stepNames {
		pipeline.Steps = append(pipeline.Steps, PipelineStep{Name: name, Input: `${input}`})
	}
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{
		ID: "run-dag-admission", Pipeline: "dag-admission", OwnerSubject: "producer-a", TenantID: "tenant-a",
		Status: "running", Graph: &binding, NodeStates: nodes, CreatedAt: createdAt,
	}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	return run
}

func dagChildRequest(run PipelineRun, step, id string) SubmitRequest {
	return SubmitRequest{
		ID: id, OwnerSubject: run.OwnerSubject, TenantID: run.TenantID, Source: "pipeline:" + run.Pipeline,
		Payload: []byte(`{"prompt":"test"}`), MaxAttempts: 1, Pipeline: run.Pipeline, Step: step, ParentID: run.ID,
		Requirements: Requirements{Task: "generation", Provider: "ollama"},
	}
}
