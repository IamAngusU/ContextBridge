package cluster

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlanPipelineGraphDiamondIsDeterministic(t *testing.T) {
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{
		{Name: "root", Input: `${input}`},
		{Name: "left", DependsOn: []string{"root"}, Input: `${steps.root.output}`},
		{Name: "right", DependsOn: []string{"root"}, Input: `${steps.root.output}`},
		{Name: "join", DependsOn: []string{"left", "right"}, Input: `{"left":${steps.left.output},"right":${steps.right.output}}`},
	}}
	plan, err := PlanPipelineGraph(pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != PipelineModeDAG || plan.MaxParallel != 2 || plan.EdgeCount != 4 {
		t.Fatalf("unexpected plan metadata: %#v", plan)
	}
	if want := []string{"root", "left", "right", "join"}; !reflect.DeepEqual(plan.Order, want) {
		t.Fatalf("order = %#v, want %#v", plan.Order, want)
	}
	if want := [][]string{{"root"}, {"left", "right"}, {"join"}}; !reflect.DeepEqual(plan.Layers, want) {
		t.Fatalf("layers = %#v, want %#v", plan.Layers, want)
	}
	second, err := PlanPipelineGraph(pipeline)
	if err != nil || !reflect.DeepEqual(plan, second) {
		t.Fatalf("planning was not deterministic: %#v %#v %v", plan, second, err)
	}
}

func TestPlanPipelineGraphRejectsUnsafeGraphsAndTemplates(t *testing.T) {
	valid := func() Pipeline {
		return Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{
			{Name: "a", Input: `${input}`},
			{Name: "b", DependsOn: []string{"a"}, Input: `${steps.a.output}`},
		}}
	}
	tests := []struct {
		name   string
		mutate func(*Pipeline)
		want   string
	}{
		{"unknown mode", func(p *Pipeline) { p.Mode = "parallel-ish" }, "mode"},
		{"parallel zero", func(p *Pipeline) { p.MaxParallel = 0 }, "max_parallel"},
		{"parallel overflow", func(p *Pipeline) { p.MaxParallel = MaximumPipelineParallelism + 1 }, "max_parallel"},
		{"unknown dependency", func(p *Pipeline) { p.Steps[1].DependsOn = []string{"missing"} }, "unknown dependency"},
		{"self dependency", func(p *Pipeline) { p.Steps[1].DependsOn = []string{"b"} }, "itself"},
		{"duplicate dependency", func(p *Pipeline) { p.Steps[1].DependsOn = []string{"a", "a"} }, "repeats dependency"},
		{"cycle", func(p *Pipeline) { p.Steps[0].DependsOn = []string{"b"} }, "cycle"},
		{"previous", func(p *Pipeline) { p.Steps[1].Input = `${previous}` }, "previous"},
		{"implicit previous", func(p *Pipeline) { p.Steps[1].Input = "" }, "explicitly"},
		{"undeclared sibling", func(p *Pipeline) { p.Steps[1].Input = `${steps.b.output}` }, "without a direct dependency"},
		{"unknown placeholder", func(p *Pipeline) { p.Steps[1].Input = `${environment.secret}` }, "unsupported placeholder"},
		{"iteration", func(p *Pipeline) { p.Steps[1].MaxIterations = 1 }, "iteration"},
		{"continue", func(p *Pipeline) { p.Steps[1].ContinuePath = "done" }, "continue"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pipeline := valid()
			test.mutate(&pipeline)
			if _, err := PlanPipelineGraph(pipeline); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestPlanPipelineGraphBoundsFanAndEdges(t *testing.T) {
	fanIn := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2}
	dependencies := make([]string, 0, MaximumPipelineDependencyFan+1)
	for index := 0; index < MaximumPipelineDependencyFan+1; index++ {
		name := fmt.Sprintf("source-%02d", index)
		fanIn.Steps = append(fanIn.Steps, PipelineStep{Name: name, Input: `${input}`})
		dependencies = append(dependencies, name)
	}
	fanIn.Steps = append(fanIn.Steps, PipelineStep{Name: "join", DependsOn: dependencies, Input: `${input}`})
	if _, err := PlanPipelineGraph(fanIn); err == nil || !strings.Contains(err.Error(), "dependencies") {
		t.Fatalf("fan-in overflow error = %v", err)
	}

	fanOut := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{{Name: "root", Input: `${input}`}}}
	for index := 0; index < MaximumPipelineDependencyFan+1; index++ {
		fanOut.Steps = append(fanOut.Steps, PipelineStep{Name: fmt.Sprintf("child-%02d", index), DependsOn: []string{"root"}, Input: `${steps.root.output}`})
	}
	if _, err := PlanPipelineGraph(fanOut); err == nil || !strings.Contains(err.Error(), "dependants") {
		t.Fatalf("fan-out overflow error = %v", err)
	}

	edgeOverflow := Pipeline{Mode: PipelineModeDAG, MaxParallel: MaximumPipelineParallelism}
	for source := 0; source < 34; source++ {
		edgeOverflow.Steps = append(edgeOverflow.Steps, PipelineStep{Name: fmt.Sprintf("source-%02d", source), Input: `${input}`})
	}
	for target := 0; target < 33; target++ {
		dependsOn := make([]string, 0, MaximumPipelineDependencyFan)
		for offset := 0; offset < MaximumPipelineDependencyFan; offset++ {
			dependsOn = append(dependsOn, fmt.Sprintf("source-%02d", (target+offset)%34))
		}
		edgeOverflow.Steps = append(edgeOverflow.Steps, PipelineStep{Name: fmt.Sprintf("target-%02d", target), DependsOn: dependsOn, Input: `${input}`})
	}
	if _, err := PlanPipelineGraph(edgeOverflow); err == nil || !strings.Contains(err.Error(), "dependency edges") {
		t.Fatalf("edge overflow error = %v", err)
	}
}

func TestLinearPipelineContractRemainsSequential(t *testing.T) {
	pipeline := Pipeline{Steps: []PipelineStep{{Name: "first"}, {Name: "second"}}}
	plan, err := PlanPipelineGraph(pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != PipelineModeLinear || plan.MaxParallel != 1 || !reflect.DeepEqual(plan.Layers, [][]string{{"first"}, {"second"}}) {
		t.Fatalf("unexpected legacy linear plan: %#v", plan)
	}
	pipeline.Steps[1].DependsOn = []string{"first"}
	if _, err := PlanPipelineGraph(pipeline); err == nil {
		t.Fatal("linear pipeline silently accepted DAG fields")
	}
}

func TestDAGRunIsRejectedBeforeAdmission(t *testing.T) {
	const token = "admin_012345678901234567890123456789012345"
	pipeline := Pipeline{Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{{Name: "one", Input: `${input}`}}}
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: token, Pipelines: map[string]Pipeline{"future": pipeline}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/cluster/pipelines/future/run", strings.NewReader(`{"work":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
	runs, err := relay.store.ListPipelineRuns(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("DAG rejection admitted work: %#v", runs)
	}
}

func TestClonePipelineOwnsDependencySlices(t *testing.T) {
	original := Pipeline{Steps: []PipelineStep{{Name: "step", DependsOn: []string{"root"}}}}
	cloned := clonePipeline(original)
	cloned.Steps[0].DependsOn[0] = "changed"
	if original.Steps[0].DependsOn[0] != "root" {
		t.Fatalf("clone aliased dependency slice: %#v", original)
	}
}

func TestPipelineListDoesNotClaimDAGExecution(t *testing.T) {
	const token = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: token, Pipelines: map[string]Pipeline{
		"linear": {Steps: []PipelineStep{{Name: "one"}}},
		"future": {Mode: PipelineModeDAG, MaxParallel: 2, Steps: []PipelineStep{{Name: "one", Input: `${input}`}}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/cluster/pipelines", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var summaries []struct {
		Name               string `json:"name"`
		Mode               string `json:"mode"`
		ExecutionSupported bool   `json:"execution_supported"`
	}
	if err := json.NewDecoder(response.Body).Decode(&summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[0].Name != "future" || summaries[0].Mode != PipelineModeDAG || summaries[0].ExecutionSupported || summaries[1].Name != "linear" || !summaries[1].ExecutionSupported {
		t.Fatalf("unexpected summaries: %#v", summaries)
	}
}
