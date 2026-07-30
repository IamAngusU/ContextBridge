package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (r *Relay) handlePipelines(w http.ResponseWriter, _ *http.Request) {
	type summary struct {
		Name          string `json:"name"`
		Steps         int    `json:"steps"`
		MaxIterations int    `json:"max_iterations"`
	}
	result := make([]summary, 0, len(r.cfg.Pipelines))
	for name, pipeline := range r.cfg.Pipelines {
		result = append(result, summary{Name: name, Steps: len(pipeline.Steps), MaxIterations: pipeline.MaxIterations})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	writeJSON(w, http.StatusOK, result)
}

func (r *Relay) handlePipelineRun(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	pipeline, ok := r.cfg.Pipelines[name]
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("pipeline not found"))
		return
	}
	record, _ := tokenRecord(req.Context())
	for index := range pipeline.Steps {
		if err := scopeRequirements(&pipeline.Steps[index].Requirements, record); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		if err := r.validateRequirements(pipeline.Steps[index].Requirements); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	var input json.RawMessage
	if err := decodeJSON(req.Body, &input, r.cfg.MaxJobBytes); err != nil || !json.Valid(input) {
		writeError(w, http.StatusBadRequest, errors.New("pipeline input must be valid JSON"))
		return
	}
	run := PipelineRun{ID: randomID("run"), Pipeline: name, Status: "running", Input: input, CreatedAt: time.Now().UTC()}
	run.OwnerSubject = record.Subject
	if err := r.store.SavePipelineRun(run); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	go r.executePipeline(run, pipeline)
	writeJSON(w, http.StatusAccepted, run)
}

func (r *Relay) handlePipelineRunStatus(w http.ResponseWriter, req *http.Request) {
	run, err := r.store.GetPipelineRun(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("pipeline run not found"))
		return
	}
	if record, ok := tokenRecord(req.Context()); ok && record.Role == "producer" && run.OwnerSubject != record.Subject {
		writeError(w, http.StatusForbidden, errors.New("pipeline run belongs to another producer"))
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (r *Relay) executePipeline(run PipelineRun, pipeline Pipeline) {
	runtimeLimit := r.cfg.MaxPipelineRuntime
	if pipeline.MaxRuntimeSeconds > 0 {
		runtimeLimit = time.Duration(pipeline.MaxRuntimeSeconds) * time.Second
	}
	if runtimeLimit <= 0 {
		runtimeLimit = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeLimit)
	defer cancel()
	values := map[string]json.RawMessage{"input": run.Input, "previous": run.Input}
	globalIterations := pipeline.MaxIterations
	if globalIterations <= 0 {
		globalIterations = 3
	}
	if globalIterations > 20 {
		globalIterations = 20
	}
	for _, step := range pipeline.Steps {
		iterations := step.MaxIterations
		if iterations <= 0 {
			iterations = 1
		}
		if iterations > globalIterations {
			iterations = globalIterations
		}
		for iteration := 0; iteration < iterations; iteration++ {
			payload, err := renderPipelineInput(step.Input, values)
			if err != nil {
				r.failPipeline(&run, err)
				return
			}
			requirements := step.Requirements
			job, err := r.store.CreateJob(SubmitRequest{OwnerSubject: run.OwnerSubject, Source: "pipeline:" + run.Pipeline, Requirements: requirements, Payload: payload, MaxAttempts: step.Retries + 1})
			if err != nil {
				r.failPipeline(&run, err)
				return
			}
			job.Pipeline, job.Step, job.ParentID = run.Pipeline, step.Name, run.ID
			_ = r.store.SaveJob(job)
			r.signalDispatch()
			job, err = r.waitJob(ctx, job.ID, step.TimeoutSeconds)
			run.Steps = append(run.Steps, job)
			if err != nil {
				r.failPipeline(&run, fmt.Errorf("step %s: %w", step.Name, err))
				return
			}
			if job.SealedResult != nil {
				r.failPipeline(&run, fmt.Errorf("step %s returned an encrypted result that the relay cannot chain", step.Name))
				return
			}
			values["previous"] = job.Result
			values["steps."+step.Name+".output"] = job.Result
			run.Output = job.Result
			mergeUsage(&run.Usage, job.Usage)
			_ = r.store.SavePipelineRun(run)
			if step.ContinuePath == "" || !jsonPathEquals(job.Result, step.ContinuePath, step.ContinueEquals) {
				break
			}
			if iteration+1 >= iterations {
				r.failPipeline(&run, fmt.Errorf("step %s reached its iteration limit", step.Name))
				return
			}
		}
	}
	run.Status = "completed"
	run.FinishedAt = time.Now().UTC()
	_ = r.store.SavePipelineRun(run)
	_ = r.store.AddEvent(Event{Kind: "pipeline.completed", Message: "Pipeline " + run.Pipeline + " completed", JobID: run.ID})
}

func (r *Relay) waitJob(ctx context.Context, id string, timeoutSeconds int) (Job, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stepCtx.Done():
			return Job{}, stepCtx.Err()
		case <-ticker.C:
		}
		job, err := r.store.GetJob(id)
		if err != nil {
			return Job{}, err
		}
		switch job.Status {
		case JobCompleted:
			return job, nil
		case JobFailed, JobCancelled:
			return job, errors.New(job.Error)
		}
	}
}

func (r *Relay) failPipeline(run *PipelineRun, err error) {
	run.Status = "failed"
	run.Error = cleanLabel(err.Error(), 500)
	run.FinishedAt = time.Now().UTC()
	_ = r.store.SavePipelineRun(*run)
	_ = r.store.AddEvent(Event{Kind: "pipeline.failed", Message: run.Error, JobID: run.ID})
}

func renderPipelineInput(template string, values map[string]json.RawMessage) (json.RawMessage, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		template = "${previous}"
	}
	for key, value := range values {
		template = strings.ReplaceAll(template, "${"+key+"}", string(value))
	}
	if strings.Contains(template, "${") {
		return nil, errors.New("pipeline input contains an unresolved placeholder")
	}
	if !json.Valid([]byte(template)) {
		return nil, errors.New("pipeline input template did not produce valid JSON")
	}
	return json.RawMessage(template), nil
}

func jsonPathEquals(raw json.RawMessage, path, wanted string) bool {
	var current interface{}
	if json.Unmarshal(raw, &current) != nil {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "$."), ".") {
		object, ok := current.(map[string]interface{})
		if !ok {
			return false
		}
		current, ok = object[part]
		if !ok {
			return false
		}
	}
	return fmt.Sprint(current) == wanted
}

func mergeUsage(target *Usage, value Usage) {
	target.InputTokens += value.InputTokens
	target.OutputTokens += value.OutputTokens
	target.TotalTokens += value.TotalTokens
	target.ComputeMS += value.ComputeMS
	target.QueueMS += value.QueueMS
	target.EstimatedCostUSD += value.EstimatedCostUSD
	target.EquivalentCostUSD += value.EquivalentCostUSD
	target.SavedCostUSD += value.SavedCostUSD
	if value.PeakVRAMBytes > target.PeakVRAMBytes {
		target.PeakVRAMBytes = value.PeakVRAMBytes
	}
}
