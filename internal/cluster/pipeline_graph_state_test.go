package cluster

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewDAGRunCheckpointIsDeterministicAndBound(t *testing.T) {
	createdAt := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{
		{Name: "root", Input: `${input}`, Requirements: Requirements{Task: "generation"}},
		{Name: "left", DependsOn: []string{"root"}, Input: `${steps.root.output}`},
		{Name: "right", DependsOn: []string{"root"}, Input: `${steps.root.output}`},
	}}
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ContractVersion != PipelineGraphContractV1 || binding.StepCount != 3 || binding.EdgeCount != 2 || !validSHA256(binding.ConfigSHA256) || !validSHA256(binding.GraphSHA256) {
		t.Fatalf("unexpected graph binding: %#v", binding)
	}
	if nodes[0].State != PipelineNodeReady || nodes[1].State != PipelineNodeNotReady || nodes[2].State != PipelineNodeNotReady {
		t.Fatalf("unexpected initial node states: %#v", nodes)
	}
	secondBinding, secondNodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil || !reflect.DeepEqual(binding, secondBinding) || !reflect.DeepEqual(nodes, secondNodes) {
		t.Fatalf("checkpoint creation was not deterministic: %#v %#v %v", binding, secondBinding, err)
	}

	changed := clonePipeline(pipeline)
	changed.Steps[0].Input = `{"changed":${input}}`
	changedBinding, _, err := NewDAGRunCheckpoint(changed, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if changedBinding.ConfigSHA256 == binding.ConfigSHA256 || changedBinding.GraphSHA256 == binding.GraphSHA256 {
		t.Fatal("configuration edit did not invalidate the run binding")
	}
}

func TestDAGCheckpointStoreRejectsGraphMutationAndTerminalRewind(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	createdAt := time.Now().UTC()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{
		{Name: "root", Input: `${input}`},
		{Name: "child", DependsOn: []string{"root"}, Input: `${steps.root.output}`},
	}}
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{ID: "run-dag-checkpoint", Pipeline: "dag", Status: "running", Graph: &binding, NodeStates: nodes, CreatedAt: createdAt}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}

	queued := run
	queued.NodeStates = clonePipelineNodeStates(run.NodeStates)
	queued.NodeStates[0].State = PipelineNodeQueued
	queued.NodeStates[0].JobID = "job-root"
	queued.NodeStates[0].UpdatedAt = createdAt.Add(time.Second)
	if err := store.SavePipelineRun(queued); err != nil {
		t.Fatalf("valid ready -> queued transition failed: %v", err)
	}
	completed := queued
	completed.NodeStates = clonePipelineNodeStates(queued.NodeStates)
	completed.NodeStates[0].State = PipelineNodeCompleted
	completed.NodeStates[0].UpdatedAt = createdAt.Add(2 * time.Second)
	if err := store.SavePipelineRun(completed); err != nil {
		t.Fatalf("valid queued -> completed transition failed: %v", err)
	}

	rewind := completed
	rewind.NodeStates = clonePipelineNodeStates(completed.NodeStates)
	rewind.NodeStates[0].State = PipelineNodeRunning
	rewind.NodeStates[0].UpdatedAt = createdAt.Add(3 * time.Second)
	if err := store.SavePipelineRun(rewind); err == nil || !strings.Contains(err.Error(), "cannot move") {
		t.Fatalf("terminal rewind error = %v", err)
	}

	tampered := completed
	tampered.Graph = &PipelineGraphBinding{}
	*tampered.Graph = *completed.Graph
	tampered.Graph.Order = append([]string(nil), completed.Graph.Order...)
	tampered.Graph.Order[0], tampered.Graph.Order[1] = tampered.Graph.Order[1], tampered.Graph.Order[0]
	if err := store.SavePipelineRun(tampered); err == nil {
		t.Fatal("mutated graph binding was persisted")
	}

	stored, err := store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NodeStates[0].State != PipelineNodeCompleted || stored.NodeStates[0].JobID != "job-root" || stored.Graph.GraphSHA256 != binding.GraphSHA256 {
		t.Fatalf("rejected mutation changed durable state: %#v", stored)
	}
}

func TestDAGCheckpointRejectsMalformedInitialState(t *testing.T) {
	createdAt := time.Now().UTC()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 1, Steps: []PipelineStep{{Name: "root", Input: `${input}`}}}
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{Graph: &binding, NodeStates: clonePipelineNodeStates(nodes)}
	run.NodeStates[0].DependsOn = []string{"missing"}
	if err := validatePipelineRunGraphState(run); err == nil {
		t.Fatal("malformed dependency graph was accepted")
	}
	run.NodeStates = clonePipelineNodeStates(nodes)
	run.NodeStates[0].State = PipelineNodeRunning
	if err := validatePipelineRunGraphState(run); err == nil || !strings.Contains(err.Error(), "child job") {
		t.Fatalf("running node without child job error = %v", err)
	}
}

func TestDAGCheckpointRequiresCompletedDependenciesAndUniqueJobs(t *testing.T) {
	createdAt := time.Now().UTC()
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{
		{Name: "root", Input: `${input}`},
		{Name: "child", DependsOn: []string{"root"}, Input: `${steps.root.output}`},
	}}
	binding, nodes, err := NewDAGRunCheckpoint(pipeline, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{Graph: &binding, NodeStates: clonePipelineNodeStates(nodes)}
	run.NodeStates[1].State = PipelineNodeQueued
	run.NodeStates[1].JobID = "job-child"
	if err := validatePipelineRunGraphState(run); err == nil || !strings.Contains(err.Error(), "before all dependencies completed") {
		t.Fatalf("early child admission error = %v", err)
	}

	run.NodeStates = clonePipelineNodeStates(nodes)
	run.NodeStates[0].State = PipelineNodeCompleted
	run.NodeStates[0].JobID = "job-shared"
	run.NodeStates[1].State = PipelineNodeQueued
	run.NodeStates[1].JobID = "job-shared"
	if err := validatePipelineRunGraphState(run); err == nil || !strings.Contains(err.Error(), "share child job") {
		t.Fatalf("duplicate child job error = %v", err)
	}

	run.NodeStates = clonePipelineNodeStates(nodes)
	run.NodeStates[1].State = PipelineNodeBlockedByDependency
	if err := validatePipelineRunGraphState(run); err == nil || !strings.Contains(err.Error(), "without a terminal dependency") {
		t.Fatalf("false blocked state error = %v", err)
	}
}

func clonePipelineNodeStates(nodes []PipelineNodeCheckpoint) []PipelineNodeCheckpoint {
	cloned := append([]PipelineNodeCheckpoint(nil), nodes...)
	for index := range cloned {
		cloned[index].DependsOn = append([]string(nil), nodes[index].DependsOn...)
	}
	return cloned
}
