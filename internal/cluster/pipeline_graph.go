package cluster

import (
	"errors"
	"fmt"
	"strings"
)

const (
	PipelineModeLinear = "linear"
	PipelineModeDAG    = "dag"

	MaximumPipelineParallelism     = 16
	MaximumPipelineDependencyFan   = 32
	MaximumPipelineDependencyEdges = 1024
)

// PipelineGraphPlan is a deterministic, validation-only view of a configured
// pipeline. DAG execution is deliberately not enabled by this contract slice.
type PipelineGraphPlan struct {
	Mode        string
	MaxParallel int
	EdgeCount   int
	Order       []string
	Layers      [][]string
}

func EffectivePipelineMode(pipeline Pipeline) string {
	mode := strings.ToLower(strings.TrimSpace(pipeline.Mode))
	if mode == "" {
		return PipelineModeLinear
	}
	return mode
}

// PlanPipelineGraph validates the complete operator-owned graph before any
// child job exists. Ties follow declaration order so plans and diagnostics are
// reproducible across processes and platforms.
func PlanPipelineGraph(pipeline Pipeline) (PipelineGraphPlan, error) {
	mode := EffectivePipelineMode(pipeline)
	if mode != PipelineModeLinear && mode != PipelineModeDAG {
		return PipelineGraphPlan{}, fmt.Errorf("pipeline mode must be %q or %q", PipelineModeLinear, PipelineModeDAG)
	}
	if len(pipeline.Steps) == 0 {
		return PipelineGraphPlan{}, errors.New("pipeline requires at least one step")
	}
	if mode == PipelineModeLinear {
		if pipeline.MaxParallel != 0 {
			return PipelineGraphPlan{}, errors.New("linear pipeline must not set max_parallel")
		}
		plan := PipelineGraphPlan{Mode: mode, MaxParallel: 1, Order: make([]string, 0, len(pipeline.Steps)), Layers: make([][]string, 0, len(pipeline.Steps))}
		for _, step := range pipeline.Steps {
			if len(step.DependsOn) != 0 {
				return PipelineGraphPlan{}, fmt.Errorf("linear pipeline step %s must not set depends_on", step.Name)
			}
			plan.Order = append(plan.Order, step.Name)
			plan.Layers = append(plan.Layers, []string{step.Name})
		}
		return plan, nil
	}

	if pipeline.MaxParallel < 1 || pipeline.MaxParallel > MaximumPipelineParallelism {
		return PipelineGraphPlan{}, fmt.Errorf("DAG max_parallel must be between 1 and %d", MaximumPipelineParallelism)
	}
	if pipeline.MaxIterations != 0 {
		return PipelineGraphPlan{}, errors.New("DAG pipeline must not set max_iterations")
	}

	indexByName := make(map[string]int, len(pipeline.Steps))
	for index, step := range pipeline.Steps {
		name := strings.TrimSpace(step.Name)
		if name == "" {
			return PipelineGraphPlan{}, errors.New("DAG pipeline contains an empty step name")
		}
		if _, exists := indexByName[name]; exists {
			return PipelineGraphPlan{}, fmt.Errorf("DAG pipeline repeats step name %s", name)
		}
		indexByName[name] = index
		if step.MaxIterations != 0 || strings.TrimSpace(step.ContinuePath) != "" || strings.TrimSpace(step.ContinueEquals) != "" {
			return PipelineGraphPlan{}, fmt.Errorf("DAG step %s must not use iteration or continue fields", name)
		}
	}

	indegree := make([]int, len(pipeline.Steps))
	children := make([][]int, len(pipeline.Steps))
	edges := 0
	for index, step := range pipeline.Steps {
		if len(step.DependsOn) > MaximumPipelineDependencyFan {
			return PipelineGraphPlan{}, fmt.Errorf("DAG step %s has more than %d dependencies", step.Name, MaximumPipelineDependencyFan)
		}
		seenDependencies := make(map[string]struct{}, len(step.DependsOn))
		for _, rawDependency := range step.DependsOn {
			dependency := strings.TrimSpace(rawDependency)
			dependencyIndex, exists := indexByName[dependency]
			if !exists {
				return PipelineGraphPlan{}, fmt.Errorf("DAG step %s references unknown dependency %s", step.Name, dependency)
			}
			if dependencyIndex == index {
				return PipelineGraphPlan{}, fmt.Errorf("DAG step %s depends on itself", step.Name)
			}
			if _, duplicate := seenDependencies[dependency]; duplicate {
				return PipelineGraphPlan{}, fmt.Errorf("DAG step %s repeats dependency %s", step.Name, dependency)
			}
			seenDependencies[dependency] = struct{}{}
			indegree[index]++
			children[dependencyIndex] = append(children[dependencyIndex], index)
			edges++
			if edges > MaximumPipelineDependencyEdges {
				return PipelineGraphPlan{}, fmt.Errorf("DAG has more than %d dependency edges", MaximumPipelineDependencyEdges)
			}
			if len(children[dependencyIndex]) > MaximumPipelineDependencyFan {
				return PipelineGraphPlan{}, fmt.Errorf("DAG step %s has more than %d dependants", dependency, MaximumPipelineDependencyFan)
			}
		}
		if err := validateDAGTemplate(step, seenDependencies); err != nil {
			return PipelineGraphPlan{}, err
		}
	}

	remaining := append([]int(nil), indegree...)
	order := make([]string, 0, len(pipeline.Steps))
	layers := make([][]string, 0, len(pipeline.Steps))
	processed := make([]bool, len(pipeline.Steps))
	for len(order) < len(pipeline.Steps) {
		ready := make([]int, 0, len(pipeline.Steps)-len(order))
		for index := range pipeline.Steps {
			if !processed[index] && remaining[index] == 0 {
				ready = append(ready, index)
			}
		}
		if len(ready) == 0 {
			return PipelineGraphPlan{}, errors.New("DAG contains a dependency cycle")
		}
		layer := make([]string, 0, len(ready))
		for _, index := range ready {
			processed[index] = true
			name := pipeline.Steps[index].Name
			order = append(order, name)
			layer = append(layer, name)
		}
		layers = append(layers, layer)
		for _, index := range ready {
			for _, child := range children[index] {
				remaining[child]--
			}
		}
	}

	return PipelineGraphPlan{Mode: mode, MaxParallel: pipeline.MaxParallel, EdgeCount: edges, Order: order, Layers: layers}, nil
}

func validateDAGTemplate(step PipelineStep, dependencies map[string]struct{}) error {
	template := strings.TrimSpace(step.Input)
	if template == "" {
		return fmt.Errorf("DAG step %s must set input explicitly; implicit previous is unavailable", step.Name)
	}
	for len(template) > 0 {
		start := strings.Index(template, "${")
		if start < 0 {
			return nil
		}
		template = template[start+2:]
		end := strings.IndexByte(template, '}')
		if end < 0 {
			return fmt.Errorf("DAG step %s input contains an unresolved placeholder", step.Name)
		}
		key := template[:end]
		template = template[end+1:]
		if key == "input" {
			continue
		}
		if key == "previous" {
			return fmt.Errorf("DAG step %s input must not reference previous", step.Name)
		}
		const prefix = "steps."
		const suffix = ".output"
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
			return fmt.Errorf("DAG step %s input contains unsupported placeholder %s", step.Name, key)
		}
		dependency := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
		if _, allowed := dependencies[dependency]; !allowed {
			return fmt.Errorf("DAG step %s input references %s without a direct dependency", step.Name, dependency)
		}
	}
	return nil
}
