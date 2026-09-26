package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const PipelineGraphContractV1 = "contextbridge.pipeline-graph.v1"

const (
	PipelineNodeNotReady            = "not_ready"
	PipelineNodeReady               = "ready"
	PipelineNodeQueued              = "queued"
	PipelineNodeRunning             = "running"
	PipelineNodeCompleted           = "completed"
	PipelineNodeFailed              = "failed"
	PipelineNodeCancelled           = "cancelled"
	PipelineNodeBlockedByDependency = "blocked_by_dependency"
	PipelineNodeAmbiguous           = "ambiguous"
)

// NewDAGRunCheckpoint creates the immutable graph binding and initial durable
// node states for a validated DAG. It does not admit or execute any work.
func NewDAGRunCheckpoint(pipeline Pipeline, createdAt time.Time) (PipelineGraphBinding, []PipelineNodeCheckpoint, error) {
	plan, err := PlanPipelineGraph(pipeline)
	if err != nil {
		return PipelineGraphBinding{}, nil, err
	}
	if plan.Mode != PipelineModeDAG {
		return PipelineGraphBinding{}, nil, errors.New("pipeline is not a DAG")
	}
	if createdAt.IsZero() {
		return PipelineGraphBinding{}, nil, errors.New("DAG checkpoint requires a creation time")
	}
	normalized := clonePipeline(pipeline)
	normalized.Mode = PipelineModeDAG
	for index := range normalized.Steps {
		for dependencyIndex := range normalized.Steps[index].DependsOn {
			normalized.Steps[index].DependsOn[dependencyIndex] = strings.TrimSpace(normalized.Steps[index].DependsOn[dependencyIndex])
		}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return PipelineGraphBinding{}, nil, fmt.Errorf("encode DAG configuration: %w", err)
	}
	configDigest := sha256.Sum256(encoded)
	nodes := make([]PipelineNodeCheckpoint, 0, len(normalized.Steps))
	for _, step := range normalized.Steps {
		state := PipelineNodeNotReady
		if len(step.DependsOn) == 0 {
			state = PipelineNodeReady
		}
		nodes = append(nodes, PipelineNodeCheckpoint{
			Step: step.Name, DependsOn: append([]string(nil), step.DependsOn...), State: state, UpdatedAt: createdAt.UTC(),
		})
	}
	binding := PipelineGraphBinding{
		ContractVersion: PipelineGraphContractV1,
		Mode:            PipelineModeDAG, ConfigSHA256: hex.EncodeToString(configDigest[:]),
		MaxParallel: normalized.MaxParallel, StepCount: len(nodes), EdgeCount: plan.EdgeCount,
		Order: append([]string(nil), plan.Order...),
	}
	binding.GraphSHA256, err = pipelineGraphBindingDigest(binding, nodes)
	if err != nil {
		return PipelineGraphBinding{}, nil, err
	}
	return binding, nodes, nil
}

func validatePipelineRunGraphState(run PipelineRun) error {
	if run.Graph == nil {
		if len(run.NodeStates) != 0 {
			return errors.New("pipeline node states require a graph binding")
		}
		return nil
	}
	binding := *run.Graph
	if binding.ContractVersion != PipelineGraphContractV1 || binding.Mode != PipelineModeDAG {
		return errors.New("pipeline graph uses an unsupported contract")
	}
	if !validSHA256(binding.ConfigSHA256) || !validSHA256(binding.GraphSHA256) {
		return errors.New("pipeline graph requires valid SHA-256 bindings")
	}
	if binding.MaxParallel < 1 || binding.MaxParallel > MaximumPipelineParallelism {
		return errors.New("pipeline graph max_parallel is outside the public limit")
	}
	if binding.StepCount < 1 || binding.StepCount != len(run.NodeStates) || len(binding.Order) != len(run.NodeStates) {
		return errors.New("pipeline graph step count does not match its checkpoint")
	}

	nodeByName := make(map[string]PipelineNodeCheckpoint, len(run.NodeStates))
	jobIDs := make(map[string]string, len(run.NodeStates))
	edges := 0
	outgoing := make(map[string]int, len(run.NodeStates))
	for _, node := range run.NodeStates {
		if strings.TrimSpace(node.Step) == "" {
			return errors.New("pipeline graph contains an empty step name")
		}
		if _, duplicate := nodeByName[node.Step]; duplicate {
			return fmt.Errorf("pipeline graph repeats step %s", node.Step)
		}
		if !validPipelineNodeState(node.State) || node.UpdatedAt.IsZero() {
			return fmt.Errorf("pipeline graph step %s has an invalid checkpoint", node.Step)
		}
		if len(node.DependsOn) > MaximumPipelineDependencyFan {
			return fmt.Errorf("pipeline graph step %s exceeds dependency fan-in", node.Step)
		}
		if node.JobID != "" && !validJobID(node.JobID) {
			return fmt.Errorf("pipeline graph step %s has an invalid child job", node.Step)
		}
		if previousStep, duplicate := jobIDs[node.JobID]; node.JobID != "" && duplicate {
			return fmt.Errorf("pipeline graph steps %s and %s share child job %s", previousStep, node.Step, node.JobID)
		} else if node.JobID != "" {
			jobIDs[node.JobID] = node.Step
		}
		if (node.State == PipelineNodeNotReady || node.State == PipelineNodeReady || node.State == PipelineNodeBlockedByDependency) && node.JobID != "" {
			return fmt.Errorf("pipeline graph step %s has a job before admission", node.Step)
		}
		if (node.State == PipelineNodeQueued || node.State == PipelineNodeRunning || node.State == PipelineNodeCompleted) && !validJobID(node.JobID) {
			return fmt.Errorf("pipeline graph step %s requires a valid child job", node.Step)
		}
		nodeByName[node.Step] = node
	}
	for _, node := range run.NodeStates {
		seen := make(map[string]struct{}, len(node.DependsOn))
		allDependenciesCompleted := true
		hasBlockingDependency := false
		for _, dependency := range node.DependsOn {
			if dependency == node.Step {
				return fmt.Errorf("pipeline graph step %s depends on itself", node.Step)
			}
			if _, exists := nodeByName[dependency]; !exists {
				return fmt.Errorf("pipeline graph step %s has unknown dependency %s", node.Step, dependency)
			}
			if _, duplicate := seen[dependency]; duplicate {
				return fmt.Errorf("pipeline graph step %s repeats dependency %s", node.Step, dependency)
			}
			seen[dependency] = struct{}{}
			dependencyState := nodeByName[dependency].State
			if dependencyState != PipelineNodeCompleted {
				allDependenciesCompleted = false
			}
			if dependencyState == PipelineNodeFailed || dependencyState == PipelineNodeCancelled || dependencyState == PipelineNodeAmbiguous || dependencyState == PipelineNodeBlockedByDependency {
				hasBlockingDependency = true
			}
			edges++
			outgoing[dependency]++
			if outgoing[dependency] > MaximumPipelineDependencyFan || edges > MaximumPipelineDependencyEdges {
				return errors.New("pipeline graph exceeds its dependency limits")
			}
		}
		if pipelineNodeRequiresCompletedDependencies(node.State) && !allDependenciesCompleted {
			return fmt.Errorf("pipeline graph step %s became %s before all dependencies completed", node.Step, node.State)
		}
		if node.State == PipelineNodeBlockedByDependency && !hasBlockingDependency {
			return fmt.Errorf("pipeline graph step %s is blocked without a terminal dependency", node.Step)
		}
	}
	if edges != binding.EdgeCount {
		return errors.New("pipeline graph edge count does not match its binding")
	}
	position := make(map[string]int, len(binding.Order))
	for index, step := range binding.Order {
		if _, exists := nodeByName[step]; !exists {
			return fmt.Errorf("pipeline graph order references unknown step %s", step)
		}
		if _, duplicate := position[step]; duplicate {
			return fmt.Errorf("pipeline graph order repeats step %s", step)
		}
		position[step] = index
	}
	for _, node := range run.NodeStates {
		for _, dependency := range node.DependsOn {
			if position[dependency] >= position[node.Step] {
				return errors.New("pipeline graph order is not topological")
			}
		}
	}
	wantDigest, err := pipelineGraphBindingDigest(binding, run.NodeStates)
	if err != nil {
		return err
	}
	if wantDigest != binding.GraphSHA256 {
		return errors.New("pipeline graph checkpoint does not match its immutable binding")
	}
	return nil
}

func validatePipelineRunGraphTransition(previous, next PipelineRun) error {
	if previous.Graph == nil || next.Graph == nil {
		if previous.Graph != nil || next.Graph != nil || len(previous.NodeStates) != len(next.NodeStates) {
			return errors.New("pipeline graph binding cannot be added or removed after admission")
		}
		return nil
	}
	if !reflect.DeepEqual(previous.Graph, next.Graph) || len(previous.NodeStates) != len(next.NodeStates) {
		return errors.New("pipeline graph binding is immutable")
	}
	for index := range previous.NodeStates {
		before := previous.NodeStates[index]
		after := next.NodeStates[index]
		if before.Step != after.Step || !reflect.DeepEqual(before.DependsOn, after.DependsOn) {
			return errors.New("pipeline graph node identity is immutable")
		}
		if before.JobID != "" && before.JobID != after.JobID {
			return fmt.Errorf("pipeline graph step %s changed its child job", before.Step)
		}
		if after.UpdatedAt.Before(before.UpdatedAt) {
			return fmt.Errorf("pipeline graph step %s moved its checkpoint time backwards", before.Step)
		}
		if !allowedPipelineNodeTransition(before.State, after.State) {
			return fmt.Errorf("pipeline graph step %s cannot move from %s to %s", before.Step, before.State, after.State)
		}
	}
	return nil
}

func pipelineGraphBindingDigest(binding PipelineGraphBinding, nodes []PipelineNodeCheckpoint) (string, error) {
	immutableNodes := make([]struct {
		Step      string   `json:"step"`
		DependsOn []string `json:"depends_on,omitempty"`
	}, 0, len(nodes))
	for _, node := range nodes {
		immutableNodes = append(immutableNodes, struct {
			Step      string   `json:"step"`
			DependsOn []string `json:"depends_on,omitempty"`
		}{Step: node.Step, DependsOn: append([]string(nil), node.DependsOn...)})
	}
	payload := struct {
		ContractVersion string      `json:"contract_version"`
		Mode            string      `json:"mode"`
		ConfigSHA256    string      `json:"config_sha256"`
		MaxParallel     int         `json:"max_parallel"`
		StepCount       int         `json:"step_count"`
		EdgeCount       int         `json:"edge_count"`
		Order           []string    `json:"topological_order"`
		Nodes           interface{} `json:"nodes"`
	}{
		ContractVersion: binding.ContractVersion,
		Mode:            binding.Mode,
		ConfigSHA256:    binding.ConfigSHA256,
		MaxParallel:     binding.MaxParallel,
		StepCount:       binding.StepCount,
		EdgeCount:       binding.EdgeCount,
		Order:           append([]string(nil), binding.Order...),
		Nodes:           immutableNodes,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode pipeline graph binding: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPipelineNodeState(state string) bool {
	switch state {
	case PipelineNodeNotReady, PipelineNodeReady, PipelineNodeQueued, PipelineNodeRunning,
		PipelineNodeCompleted, PipelineNodeFailed, PipelineNodeCancelled,
		PipelineNodeBlockedByDependency, PipelineNodeAmbiguous:
		return true
	default:
		return false
	}
}

func pipelineNodeRequiresCompletedDependencies(state string) bool {
	switch state {
	case PipelineNodeReady, PipelineNodeQueued, PipelineNodeRunning, PipelineNodeCompleted, PipelineNodeFailed, PipelineNodeAmbiguous:
		return true
	default:
		return false
	}
}

func allowedPipelineNodeTransition(before, after string) bool {
	if before == after {
		return true
	}
	switch before {
	case PipelineNodeNotReady:
		return after == PipelineNodeReady || after == PipelineNodeBlockedByDependency || after == PipelineNodeCancelled
	case PipelineNodeReady:
		return after == PipelineNodeQueued || after == PipelineNodeFailed || after == PipelineNodeCancelled || after == PipelineNodeBlockedByDependency
	case PipelineNodeQueued:
		return after == PipelineNodeRunning || after == PipelineNodeCompleted || after == PipelineNodeFailed || after == PipelineNodeCancelled || after == PipelineNodeAmbiguous
	case PipelineNodeRunning:
		return after == PipelineNodeCompleted || after == PipelineNodeFailed || after == PipelineNodeCancelled || after == PipelineNodeAmbiguous
	default:
		return false
	}
}
