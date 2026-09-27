package cluster

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDAGTerminalCheckpointPromotesReadySetAndIsIdempotent(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	run := saveDAGCheckpointRun(t, store, 2,
		PipelineStep{Name: "root", Input: `${input}`},
		PipelineStep{Name: "left", DependsOn: []string{"root"}, Input: `${input}`},
		PipelineStep{Name: "right", DependsOn: []string{"root"}, Input: `${input}`},
		PipelineStep{Name: "join", DependsOn: []string{"left", "right"}, Input: `${input}`},
	)
	_, job, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", dagChildRequest(run, "root", "job-root"), 10, ProducerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	job = completeDAGChild(t, store, job, "")

	updated, err := store.ReconcileDAGChildTerminal(run.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"root": PipelineNodeCompleted, "left": PipelineNodeReady,
		"right": PipelineNodeReady, "join": PipelineNodeNotReady,
	}
	assertDAGNodeStates(t, updated, want)
	if len(updated.Steps) != 1 || updated.Steps[0].Status != JobCompleted {
		t.Fatalf("terminal child snapshot was not checkpointed: %#v", updated.Steps)
	}
	eventsBefore, err := store.ListPipelineEvents(run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := pipelineEventsOfType(eventsBefore.Events, "pipeline.step.ready"); !reflect.DeepEqual(got, []string{"left", "right"}) {
		t.Fatalf("ready event order = %v", got)
	}
	statesBefore := clonePipelineNodeCheckpoints(updated.NodeStates)

	retried, err := store.ReconcileDAGChildTerminal(run.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventsAfter, err := store.ListPipelineEvents(run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retried.NodeStates, statesBefore) {
		t.Fatalf("idempotent retry changed checkpoints: before=%#v after=%#v", statesBefore, retried.NodeStates)
	}
	if !reflect.DeepEqual(eventsAfter.Events, eventsBefore.Events) {
		t.Fatalf("idempotent retry emitted duplicate events: before=%#v after=%#v", eventsBefore.Events, eventsAfter.Events)
	}
}

func TestDAGTerminalCheckpointBlocksDescendantsAndPreservesActiveSibling(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	run := saveDAGCheckpointRun(t, store, 2,
		PipelineStep{Name: "root", Input: `${input}`},
		PipelineStep{Name: "independent", Input: `${input}`},
		PipelineStep{Name: "child", DependsOn: []string{"root"}, Input: `${input}`},
		PipelineStep{Name: "grandchild", DependsOn: []string{"child"}, Input: `${input}`},
	)
	_, failed, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", dagChildRequest(run, "root", "job-root-failed"), 10, ProducerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, sibling, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "independent", dagChildRequest(run, "independent", "job-independent"), 10, ProducerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	failed = completeDAGChild(t, store, failed, FailureExecutionStateAmbiguous)

	updated, err := store.ReconcileDAGChildTerminal(run.ID, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertDAGNodeStates(t, updated, map[string]string{
		"root": PipelineNodeAmbiguous, "independent": PipelineNodeQueued,
		"child": PipelineNodeBlockedByDependency, "grandchild": PipelineNodeBlockedByDependency,
	})
	storedSibling, err := store.GetJob(sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedSibling.Status != JobQueued {
		t.Fatalf("independent sibling changed to %s", storedSibling.Status)
	}
	events, err := store.ListPipelineEvents(run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := pipelineEventsOfType(events.Events, "pipeline.step.blocked"); !reflect.DeepEqual(got, []string{"child", "grandchild"}) {
		t.Fatalf("blocked event order = %v", got)
	}
}

func TestDAGTerminalCheckpointEventFailureRollsBackAndRetrySucceeds(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	run := saveDAGCheckpointRun(t, store, 1,
		PipelineStep{Name: "root", Input: `${input}`},
		PipelineStep{Name: "child", DependsOn: []string{"root"}, Input: `${input}`},
	)
	_, job, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", dagChildRequest(run, "root", "job-event-rollback"), 10, ProducerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	job = completeDAGChild(t, store, job, "")
	store.saveJobEventTestHook = func(event JobEvent) error {
		if event.Type == "pipeline.step.ready" {
			return errors.New("injected ready event failure")
		}
		return nil
	}
	if _, err := store.ReconcileDAGChildTerminal(run.ID, job.ID); err == nil || !strings.Contains(err.Error(), "injected ready event failure") {
		t.Fatalf("reconciliation error = %v", err)
	}
	stored, err := store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertDAGNodeStates(t, stored, map[string]string{"root": PipelineNodeQueued, "child": PipelineNodeNotReady})
	if stored.Steps[0].Status != JobQueued {
		t.Fatalf("terminal snapshot survived rolled-back transaction: %s", stored.Steps[0].Status)
	}

	store.saveJobEventTestHook = nil
	retried, err := store.ReconcileDAGChildTerminal(run.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertDAGNodeStates(t, retried, map[string]string{"root": PipelineNodeCompleted, "child": PipelineNodeReady})
}

func TestDAGTerminalCheckpointRejectsNonTerminalOrMissingRunSnapshot(t *testing.T) {
	store := openDAGAdmissionStore(t)
	defer store.Close()
	run := saveDAGCheckpointRun(t, store, 1, PipelineStep{Name: "root", Input: `${input}`})
	_, job, err := store.CreateDAGChildJobAdmittedGoverned(run.ID, "root", dagChildRequest(run, "root", "job-invalid-terminal"), 10, ProducerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileDAGChildTerminal(run.ID, job.ID); err == nil || !strings.Contains(err.Error(), "not terminal") {
		t.Fatalf("non-terminal reconciliation error = %v", err)
	}
	job = completeDAGChild(t, store, job, "")
	corrupt, err := store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	corrupt.Steps = nil
	if err := store.SavePipelineRun(corrupt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileDAGChildTerminal(run.ID, job.ID); err == nil || !strings.Contains(err.Error(), "missing from its run checkpoint") {
		t.Fatalf("missing snapshot reconciliation error = %v", err)
	}
}

func TestDAGTerminalCheckpointMapsEveryTerminalOutcome(t *testing.T) {
	tests := []struct {
		name string
		job  Job
		want string
	}{
		{name: "completed", job: Job{Status: JobCompleted}, want: PipelineNodeCompleted},
		{name: "cancelled", job: Job{Status: JobCancelled}, want: PipelineNodeCancelled},
		{name: "failed", job: Job{Status: JobFailed, FailureCode: FailureWorkerExecution}, want: PipelineNodeFailed},
		{name: "ambiguous", job: Job{Status: JobFailed, FailureCode: FailureExecutionStateAmbiguous}, want: PipelineNodeAmbiguous},
		{name: "timeout ambiguous", job: Job{Status: JobFailed, FailureCode: FailureExecutionTimeoutAmbiguous}, want: PipelineNodeAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dagNodeStateForTerminalJob(test.job)
			if err != nil || got != test.want {
				t.Fatalf("state = %q, want %q, err=%v", got, test.want, err)
			}
		})
	}
}

func saveDAGCheckpointRun(t *testing.T, store *Store, maxParallel int, steps ...PipelineStep) PipelineRun {
	t.Helper()
	createdAt := time.Now().UTC()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: maxParallel, Steps: steps}
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{
		ID: "run-dag-terminal", Pipeline: "dag-terminal", OwnerSubject: "producer-a", TenantID: "tenant-a",
		Status: "running", Graph: &binding, NodeStates: nodes, CreatedAt: createdAt,
	}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	return run
}

func completeDAGChild(t *testing.T, store *Store, job Job, failureCode string) Job {
	t.Helper()
	assigned, err := store.AssignJob(job.ID, "node-dag-test")
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.MarkRunning(assigned.ID, assigned.AssignedNode, assigned.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if failureCode == "" {
		job, err = store.CompleteJob(running.ID, running.AssignedNode, running.Attempt, []byte(`{"ok":true}`), nil, Usage{}, "")
	} else {
		job, err = store.CompleteJobWithFailure(running.ID, running.AssignedNode, running.Attempt, nil, nil, Usage{}, "execution outcome is ambiguous", failureCode)
	}
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func assertDAGNodeStates(t *testing.T, run PipelineRun, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(run.NodeStates))
	for _, node := range run.NodeStates {
		got[node.Step] = node.State
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DAG states = %v, want %v", got, want)
	}
}

func pipelineEventsOfType(events []JobEvent, eventType string) []string {
	steps := []string{}
	for _, event := range events {
		if event.Type == eventType {
			steps = append(steps, event.StepID)
		}
	}
	return steps
}
