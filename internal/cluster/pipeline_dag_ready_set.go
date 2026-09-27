package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// dagReadySet returns at most the currently available graph slots in the
// immutable topological order bound to the run. It is deliberately a bounded
// snapshot: callers must still use the atomic store admission primitive for
// every returned step because another executor may win the same ready node.
func dagReadySet(run PipelineRun) ([]string, error) {
	if run.Status != "running" || run.Graph == nil {
		return nil, errors.New("DAG run is not active")
	}
	if err := validatePipelineRunGraphState(run); err != nil {
		return nil, err
	}
	active := 0
	stateByStep := make(map[string]string, len(run.NodeStates))
	for _, node := range run.NodeStates {
		stateByStep[node.Step] = node.State
		if node.State == PipelineNodeQueued || node.State == PipelineNodeRunning {
			active++
		}
	}
	available := run.Graph.MaxParallel - active
	if available <= 0 {
		return []string{}, nil
	}
	ready := make([]string, 0, available)
	for _, step := range run.Graph.Order {
		if stateByStep[step] != PipelineNodeReady {
			continue
		}
		ready = append(ready, step)
		if len(ready) == available {
			break
		}
	}
	return ready, nil
}

type preparedDAGChild struct {
	step     string
	payload  json.RawMessage
	require  Requirements
	decision PolicyDecision
	retries  int
}

// admitDAGReadySet is the bounded ready-set admission core for the future DAG
// executor. It creates no goroutine or poller and does not enable the public
// DAG run endpoint. Each selected child still crosses execution policy,
// producer governance, queue limits and the store's atomic ready->queued
// transition. If a later admission fails, the returned run/jobs describe any
// earlier durable admissions; they are never rolled back or silently hidden.
func (r *Relay) admitDAGReadySet(runID string, pipeline Pipeline) (PipelineRun, []Job, error) {
	run, err := r.store.GetPipelineRun(runID)
	if err != nil {
		return PipelineRun{}, nil, err
	}
	if EffectivePipelineMode(pipeline) != PipelineModeDAG {
		return run, nil, errors.New("ready-set admission requires a DAG pipeline")
	}
	pipeline = clonePipeline(pipeline)
	expectedBinding, _, err := NewDAGRunCheckpoint(pipeline, run.CreatedAt)
	if err != nil {
		return run, nil, err
	}
	if run.Graph == nil || expectedBinding.ConfigSHA256 != run.Graph.ConfigSHA256 || expectedBinding.GraphSHA256 != run.Graph.GraphSHA256 {
		return run, nil, errors.New("DAG pipeline configuration does not match the run binding")
	}
	ready, err := dagReadySet(run)
	if err != nil || len(ready) == 0 {
		return run, []Job{}, err
	}

	stepByName := make(map[string]PipelineStep, len(pipeline.Steps))
	for _, step := range pipeline.Steps {
		stepByName[step.Name] = step
	}
	prepared := make([]preparedDAGChild, 0, len(ready))
	for _, stepName := range ready {
		step, exists := stepByName[stepName]
		if !exists {
			return run, nil, fmt.Errorf("DAG run references unavailable step %s", stepName)
		}
		payload, err := r.renderDAGStepInput(run, step)
		if err != nil {
			return run, nil, fmt.Errorf("step %s input: %w", stepName, err)
		}
		if err := r.validateRequirements(step.Requirements); err != nil {
			return run, nil, fmt.Errorf("step %s requirements: %w", stepName, err)
		}
		decision, err := EvaluateExecutionPolicy(r.cfg.ExecutionPolicy, run.TenantID, step.Requirements, time.Now().UTC())
		if err != nil {
			return run, nil, fmt.Errorf("step %s policy: %w", stepName, err)
		}
		prepared = append(prepared, preparedDAGChild{
			step: stepName, payload: payload, require: step.Requirements,
			decision: decision, retries: step.Retries + 1,
		})
	}

	jobs := make([]Job, 0, len(prepared))
	for _, child := range prepared {
		if !r.beginAdmission() {
			if len(jobs) > 0 {
				r.signalDispatch()
			}
			return run, jobs, errors.New("relay is stopping")
		}
		updated, job, admitErr := r.store.CreateDAGChildJobAdmittedGoverned(run.ID, child.step, SubmitRequest{
			OwnerSubject:   run.OwnerSubject,
			TenantID:       run.TenantID,
			Source:         "pipeline:" + run.Pipeline,
			Requirements:   child.require,
			PolicyDecision: child.decision,
			Payload:        child.payload,
			MaxAttempts:    child.retries,
			Pipeline:       run.Pipeline,
			Step:           child.step,
			ParentID:       run.ID,
		}, r.cfg.MaxQueuedJobs, run.ProducerLimits)
		r.endAdmission()
		if admitErr != nil {
			if len(jobs) > 0 {
				r.signalDispatch()
			}
			return run, jobs, admitErr
		}
		run = updated
		jobs = append(jobs, job)
	}
	if len(jobs) > 0 {
		r.signalDispatch()
	}
	return run, jobs, nil
}

func (r *Relay) renderDAGStepInput(run PipelineRun, step PipelineStep) (json.RawMessage, error) {
	values := map[string]json.RawMessage{"input": run.Input}
	nodes := make(map[string]PipelineNodeCheckpoint, len(run.NodeStates))
	for _, node := range run.NodeStates {
		nodes[node.Step] = node
	}
	for _, dependency := range step.DependsOn {
		node, exists := nodes[dependency]
		if !exists || node.State != PipelineNodeCompleted || node.JobID == "" {
			return nil, fmt.Errorf("dependency %s is not durably completed", dependency)
		}
		job, err := r.store.GetJob(node.JobID)
		if err != nil {
			return nil, fmt.Errorf("read dependency %s: %w", dependency, err)
		}
		if !pipelineChildBelongsToRun(job, run) || job.Step != dependency || job.Status != JobCompleted {
			return nil, fmt.Errorf("dependency %s child does not match its checkpoint", dependency)
		}
		if job.SealedResult != nil {
			return nil, fmt.Errorf("dependency %s returned an encrypted result that the relay cannot chain", dependency)
		}
		values["steps."+dependency+".output"] = job.Result
	}
	return renderPipelineInputBounded(step.Input, values, r.cfg.MaxJobBytes)
}
