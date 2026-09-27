package cluster

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDAGReadySetAdmissionIsBoundedStableAndDependencyAware(t *testing.T) {
	relay := newPipelineCheckpointRelay(t)
	defer relay.Close()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{
		{Name: "extract_b", Input: `${input}`, Requirements: Requirements{Task: "generation", Provider: "ollama"}},
		{Name: "extract_a", Input: `${input}`, Requirements: Requirements{Task: "generation", Provider: "ollama"}},
		{Name: "spare", Input: `${input}`, Requirements: Requirements{Task: "generation", Provider: "ollama"}},
		{Name: "synthesize", DependsOn: []string{"extract_b", "extract_a"}, Input: `{"b":${steps.extract_b.output},"a":${steps.extract_a.output}}`, Requirements: Requirements{Task: "generation", Provider: "ollama"}},
	}}
	run := saveDAGExecutorRun(t, relay, pipeline)

	updated, jobs, err := relay.admitDAGReadySet(run.ID, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if got := dagJobSteps(jobs); !reflect.DeepEqual(got, []string{"extract_b", "extract_a"}) {
		t.Fatalf("first stable ready set = %v", got)
	}
	if ready, err := dagReadySet(updated); err != nil || len(ready) != 0 {
		t.Fatalf("ready set with full capacity = %v, err=%v", ready, err)
	}

	completeDAGExecutorChild(t, relay, jobs[0], json.RawMessage(`{"text":"B"}`))
	updated, err = relay.store.ReconcileDAGChildTerminal(run.ID, jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if nodeState(updated, "synthesize") != PipelineNodeNotReady {
		t.Fatalf("join became ready before every dependency: %#v", updated.NodeStates)
	}
	updated, next, err := relay.admitDAGReadySet(run.ID, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if got := dagJobSteps(next); !reflect.DeepEqual(got, []string{"spare"}) {
		t.Fatalf("next bounded ready set = %v", got)
	}

	completeDAGExecutorChild(t, relay, jobs[1], json.RawMessage(`{"text":"A"}`))
	if _, err := relay.store.ReconcileDAGChildTerminal(run.ID, jobs[1].ID); err != nil {
		t.Fatal(err)
	}
	completeDAGExecutorChild(t, relay, next[0], json.RawMessage(`{"text":"unused"}`))
	if _, err := relay.store.ReconcileDAGChildTerminal(run.ID, next[0].ID); err != nil {
		t.Fatal(err)
	}
	_, joined, err := relay.admitDAGReadySet(run.ID, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if got := dagJobSteps(joined); !reflect.DeepEqual(got, []string{"synthesize"}) {
		t.Fatalf("join ready set = %v", got)
	}
	if string(joined[0].Payload) != `{"b":{"text":"B"},"a":{"text":"A"}}` {
		t.Fatalf("join payload = %s", joined[0].Payload)
	}
}

func TestDAGReadySetRejectsConfigurationDriftBeforeAdmission(t *testing.T) {
	relay := newPipelineCheckpointRelay(t)
	defer relay.Close()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 1, Steps: []PipelineStep{{
		Name: "root", Input: `${input}`, Requirements: Requirements{Task: "generation", Provider: "ollama"},
	}}}
	run := saveDAGExecutorRun(t, relay, pipeline)
	changed := clonePipeline(pipeline)
	changed.Steps[0].Input = `{"changed":true}`

	if _, jobs, err := relay.admitDAGReadySet(run.ID, changed); err == nil || !strings.Contains(err.Error(), "does not match") || len(jobs) != 0 {
		t.Fatalf("configuration drift result: jobs=%v err=%v", jobs, err)
	}
	stored, err := relay.store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NodeStates[0].State != PipelineNodeReady || len(stored.Steps) != 0 {
		t.Fatalf("configuration drift admitted work: %#v", stored)
	}
}

func TestDAGReadySetPreflightsEverySelectedPolicyBeforeAdmission(t *testing.T) {
	relay := newPipelineCheckpointRelay(t)
	defer relay.Close()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{
		{Name: "allowed", Input: `${input}`, Requirements: Requirements{Task: "generation", Provider: "ollama"}},
		{Name: "denied", Input: `${input}`, Requirements: Requirements{Task: "forbidden", Provider: "ollama"}},
	}}
	run := saveDAGExecutorRun(t, relay, pipeline)

	if _, jobs, err := relay.admitDAGReadySet(run.ID, pipeline); err == nil || !strings.Contains(err.Error(), "not allowed") || len(jobs) != 0 {
		t.Fatalf("policy preflight result: jobs=%v err=%v", jobs, err)
	}
	stored, err := relay.store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Steps) != 0 || nodeState(stored, "allowed") != PipelineNodeReady {
		t.Fatalf("preflight failure partially admitted a ready set: %#v", stored)
	}
}

func saveDAGExecutorRun(t *testing.T, relay *Relay, pipeline Pipeline) PipelineRun {
	t.Helper()
	createdAt := time.Now().UTC()
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{
		ID: "run-dag-executor", Pipeline: "dag-executor", OwnerSubject: "owner-a", TenantID: "tenant-a",
		Status: "running", Input: json.RawMessage(`{"source":"demo"}`), Graph: &binding, NodeStates: nodes, CreatedAt: createdAt,
	}
	if err := relay.store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	return run
}

func completeDAGExecutorChild(t *testing.T, relay *Relay, job Job, result json.RawMessage) {
	t.Helper()
	assigned, err := relay.store.AssignJob(job.ID, "dag-executor-node")
	if err != nil {
		t.Fatal(err)
	}
	running, err := relay.store.MarkRunning(assigned.ID, assigned.AssignedNode, assigned.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.CompleteJob(running.ID, running.AssignedNode, running.Attempt, result, nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
}

func dagJobSteps(jobs []Job) []string {
	steps := make([]string, 0, len(jobs))
	for _, job := range jobs {
		steps = append(steps, job.Step)
	}
	return steps
}

func nodeState(run PipelineRun, step string) string {
	for _, node := range run.NodeStates {
		if node.Step == step {
			return node.State
		}
	}
	return ""
}
