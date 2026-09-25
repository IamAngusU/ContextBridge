package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
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
	// A map value copy still aliases the Steps backing array. Scope a deep copy
	// so one producer request cannot mutate shared relay configuration (or race
	// with another producer using a different group scope).
	pipeline = clonePipeline(pipeline)
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
		if _, err := EvaluateExecutionPolicy(r.cfg.ExecutionPolicy, pipeline.TenantID, pipeline.Steps[index].Requirements, time.Now().UTC()); err != nil {
			var violation *PolicyViolation
			if errors.As(err, &violation) {
				writeErrorCode(w, http.StatusForbidden, violation.Code(), err)
			} else {
				writeError(w, http.StatusInternalServerError, err)
			}
			return
		}
	}
	var input json.RawMessage
	if err := decodeJSON(req.Body, &input, r.cfg.MaxJobBytes); err != nil || !json.Valid(input) {
		writeError(w, http.StatusBadRequest, errors.New("pipeline input must be valid JSON"))
		return
	}
	run := PipelineRun{ID: randomID("run"), Pipeline: name, TenantID: pipeline.TenantID, ProducerLimits: r.producerLimits(record), Status: "running", Input: input, CreatedAt: time.Now().UTC()}
	run.OwnerSubject = record.Subject
	if !r.beginAdmission() {
		writeError(w, http.StatusServiceUnavailable, errors.New("relay is stopping"))
		return
	}
	err := r.store.CreatePipelineRunAdmitted(run, maxActivePipelineRuns, maxActivePipelineRunsPerOwner)
	r.endAdmission()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrOwnerPipelineCapacity) {
			status = http.StatusTooManyRequests
		} else if errors.Is(err, ErrPipelineCapacity) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err)
		return
	}
	r.pipelineWG.Add(1)
	go func() {
		defer r.pipelineWG.Done()
		r.executePipeline(r.pipelineContext(), run, pipeline)
	}()
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

func (r *Relay) handlePipelineRunEvents(w http.ResponseWriter, req *http.Request) {
	run, err := r.store.GetPipelineRun(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("pipeline run not found"))
		return
	}
	if record, ok := tokenRecord(req.Context()); ok && record.Role == "producer" && run.OwnerSubject != record.Subject {
		writeError(w, http.StatusForbidden, errors.New("pipeline run belongs to another producer"))
		return
	}
	after := uint64(0)
	if raw := strings.TrimSpace(req.URL.Query().Get("after")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after must be an unsigned event sequence"))
			return
		}
		after = value
	}
	page, err := r.store.ListPipelineEvents(run.ID, after, queryLimit(req, 100, 500))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}

func (r *Relay) executePipeline(parent context.Context, run PipelineRun, pipeline Pipeline) {
	defer r.recoverPipelinePanic(&run)
	runtimeLimit := r.cfg.MaxPipelineRuntime
	if pipeline.MaxRuntimeSeconds > 0 {
		runtimeLimit = time.Duration(pipeline.MaxRuntimeSeconds) * time.Second
	}
	if runtimeLimit <= 0 {
		runtimeLimit = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, runtimeLimit)
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
			payload, err := renderPipelineInputBounded(step.Input, values, r.cfg.MaxJobBytes)
			if err != nil {
				r.failPipeline(&run, err)
				return
			}
			requirements := step.Requirements
			policyDecision, err := EvaluateExecutionPolicy(r.cfg.ExecutionPolicy, run.TenantID, requirements, time.Now().UTC())
			if err != nil {
				r.failPipeline(&run, fmt.Errorf("step %s policy: %w", step.Name, err))
				return
			}
			if !r.beginAdmission() {
				r.failPipeline(&run, errors.New("relay is stopping"))
				return
			}
			job, err := r.store.CreateJobAdmittedGoverned(SubmitRequest{
				OwnerSubject:   run.OwnerSubject,
				TenantID:       run.TenantID,
				Source:         "pipeline:" + run.Pipeline,
				Requirements:   requirements,
				PolicyDecision: policyDecision,
				Payload:        payload,
				MaxAttempts:    step.Retries + 1,
				Pipeline:       run.Pipeline,
				Step:           step.Name,
				ParentID:       run.ID,
			}, r.cfg.MaxQueuedJobs, run.ProducerLimits)
			r.endAdmission()
			if err != nil {
				r.failPipeline(&run, err)
				return
			}
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
			if err := r.store.SavePipelineRun(run); err != nil {
				r.failPipeline(&run, fmt.Errorf("save checkpoint after step %s: %w", step.Name, err))
				return
			}
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
	if err := r.store.SavePipelineRun(run); err != nil {
		r.failPipeline(&run, fmt.Errorf("save completed pipeline: %w", err))
		return
	}
	if err := r.store.AddEvent(Event{Kind: "pipeline.completed", Message: "Pipeline " + run.Pipeline + " completed", JobID: run.ID}); err != nil {
		r.logger.Printf("pipeline %s completed but its event could not be saved: %v", run.ID, err)
	}
}

func (r *Relay) recoverPipelinePanic(run *PipelineRun) {
	if recover() == nil {
		return
	}
	// Never include the recovered value: it may contain provider-controlled or
	// tenant-sensitive content. The local stack identifies the code path while
	// the durable record exposes only the bounded state transition.
	r.logger.Printf("pipeline %s recovered an internal panic:\n%s", run.ID, debug.Stack())
	r.failPipeline(run, errors.New("pipeline panicked; execution state is ambiguous; explicit resubmission required"))
}

func clonePipeline(pipeline Pipeline) Pipeline {
	copyPipeline := pipeline
	copyPipeline.Steps = make([]PipelineStep, len(pipeline.Steps))
	copy(copyPipeline.Steps, pipeline.Steps)
	for index := range copyPipeline.Steps {
		copyPipeline.Steps[index].Requirements = cloneRequirements(copyPipeline.Steps[index].Requirements)
	}
	return copyPipeline
}

func cloneRequirements(requirements Requirements) Requirements {
	copyRequirements := requirements
	copyRequirements.RequiredTags = append([]string(nil), requirements.RequiredTags...)
	copyRequirements.PreferredNodes = append([]string(nil), requirements.PreferredNodes...)
	return copyRequirements
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
		if err := stepCtx.Err(); err != nil {
			return r.cancelTimedOutPipelineJob(id, err)
		}
		job, err := r.store.GetJob(id)
		if err != nil {
			return Job{}, err
		}
		switch job.Status {
		case JobCompleted:
			if err := stepCtx.Err(); err != nil {
				return job, err
			}
			if deadline, ok := stepCtx.Deadline(); ok && job.FinishedAt.After(deadline) {
				return job, context.DeadlineExceeded
			}
			return job, nil
		case JobFailed, JobCancelled:
			if job.Error == "" {
				return job, fmt.Errorf("job ended with status %s", job.Status)
			}
			return job, errors.New(job.Error)
		}
		select {
		case <-stepCtx.Done():
			return r.cancelTimedOutPipelineJob(id, stepCtx.Err())
		case <-ticker.C:
		}
	}
}

func (r *Relay) cancelTimedOutPipelineJob(id string, timeoutErr error) (Job, error) {
	job, err := r.store.GetJob(id)
	if err != nil {
		return Job{}, fmt.Errorf("%w; unable to read timed-out job: %v", timeoutErr, err)
	}
	if job.Status == JobCompleted || job.Status == JobFailed || job.Status == JobCancelled {
		return job, timeoutErr
	}
	cancelled, err := r.store.CancelJob(id)
	if err == nil {
		// A pipeline deadline makes the durable job terminal, but the worker may
		// still be executing a side-effecting adapter/model request. Keep that
		// slot reserved until its matching result or disconnect while removing it
		// from future stale-record scans, exactly like the public cancel endpoint.
		reservationState := r.markWorkerReservationTerminalState(cancelled.AssignedNode, cancelled.ID)
		if reservationState == workerReservationDispatched {
			r.cancelWorkerExecution(cancelled)
		} else {
			failureCode := ""
			if reservationState == workerReservationMissing && job.Status != JobQueued {
				failureCode = FailureExecutionStateAmbiguous
			}
			_, _ = r.store.ResolveRoutingRecoveryProbe(cancelled.AssignedNode, cancelled.ID, failureCode)
			_, _ = r.store.ReleaseAdapterSessionJobLock(cancelled.ID)
		}
		_ = r.store.AddEvent(Event{Kind: "job.cancelled", Message: "Pipeline step timed out", JobID: id})
		return cancelled, timeoutErr
	}
	// A worker result may have won the transaction race with cancellation. The
	// pipeline deadline still wins for orchestration, so report the real final
	// record while retaining the timeout as the pipeline error.
	if latest, readErr := r.store.GetJob(id); readErr == nil {
		return latest, timeoutErr
	}
	return job, fmt.Errorf("%w; unable to cancel timed-out job: %v", timeoutErr, err)
}

func (r *Relay) failPipeline(run *PipelineRun, err error) {
	run.Status = "failed"
	run.Error = cleanLabel(err.Error(), 500)
	run.FinishedAt = time.Now().UTC()
	if saveErr := r.store.SavePipelineRun(*run); saveErr != nil {
		r.logger.Printf("pipeline %s failed and its terminal checkpoint could not be saved: %v (pipeline error: %v)", run.ID, saveErr, err)
		return
	}
	if eventErr := r.store.AddEvent(Event{Kind: "pipeline.failed", Message: run.Error, JobID: run.ID}); eventErr != nil {
		r.logger.Printf("pipeline %s failed but its event could not be saved: %v", run.ID, eventErr)
	}
}

func renderPipelineInput(template string, values map[string]json.RawMessage) (json.RawMessage, error) {
	return renderPipelineInputBounded(template, values, 0)
}

func renderPipelineInputBounded(template string, values map[string]json.RawMessage, maxBytes int64) (json.RawMessage, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		template = "${previous}"
	}
	var rendered strings.Builder
	for len(template) > 0 {
		start := strings.Index(template, "${")
		if start < 0 {
			if err := appendPipelineFragment(&rendered, template, maxBytes); err != nil {
				return nil, err
			}
			break
		}
		if err := appendPipelineFragment(&rendered, template[:start], maxBytes); err != nil {
			return nil, err
		}
		template = template[start+2:]
		end := strings.IndexByte(template, '}')
		if end < 0 {
			return nil, errors.New("pipeline input contains an unresolved placeholder")
		}
		key := template[:end]
		value, ok := values[key]
		if !ok {
			return nil, errors.New("pipeline input contains an unresolved placeholder")
		}
		if err := appendPipelineFragment(&rendered, string(value), maxBytes); err != nil {
			return nil, err
		}
		template = template[end+1:]
	}
	result := rendered.String()
	if !json.Valid([]byte(result)) {
		return nil, errors.New("pipeline input template did not produce valid JSON")
	}
	return json.RawMessage(result), nil
}

func appendPipelineFragment(builder *strings.Builder, fragment string, maxBytes int64) error {
	if maxBytes > 0 && int64(len(fragment)) > maxBytes-int64(builder.Len()) {
		return &payloadLimitError{message: fmt.Sprintf("pipeline rendered payload exceeds %d bytes", maxBytes)}
	}
	_, _ = builder.WriteString(fragment)
	return nil
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
	target.InputTokens = saturatingUint64Add(target.InputTokens, value.InputTokens)
	target.OutputTokens = saturatingUint64Add(target.OutputTokens, value.OutputTokens)
	target.TotalTokens = saturatingUint64Add(target.TotalTokens, value.TotalTokens)
	target.ComputeMS = saturatingUint64Add(target.ComputeMS, value.ComputeMS)
	target.QueueMS = saturatingUint64Add(target.QueueMS, value.QueueMS)
	target.ReservedCostUSD = saturatingCostAdd(target.ReservedCostUSD, value.ReservedCostUSD)
	target.EstimatedCostUSD = saturatingCostAdd(target.EstimatedCostUSD, value.EstimatedCostUSD)
	target.EquivalentCostUSD = saturatingCostAdd(target.EquivalentCostUSD, value.EquivalentCostUSD)
	target.SavedCostUSD = saturatingCostAdd(target.SavedCostUSD, value.SavedCostUSD)
	target.CostKnownJobs = saturatingUint64Add(target.CostKnownJobs, value.CostKnownJobs)
	target.CostUnknownJobs = saturatingUint64Add(target.CostUnknownJobs, value.CostUnknownJobs)
	target.CostStatus = aggregateCostStatus(target.CostKnownJobs, target.CostUnknownJobs, target.CostStatus, value.CostStatus)
	if target.CostSource == "" {
		target.CostSource = value.CostSource
	} else if value.CostSource != "" && target.CostSource != value.CostSource {
		target.CostSource = "multiple"
	}
	if value.ResourceScope == "job" {
		target.ResourceScope = "job"
		if value.PeakVRAMBytes > target.PeakVRAMBytes {
			target.PeakVRAMBytes = value.PeakVRAMBytes
		}
		if value.PeakRAMBytes > target.PeakRAMBytes {
			target.PeakRAMBytes = value.PeakRAMBytes
		}
		if value.PeakGPUUtilization > target.PeakGPUUtilization {
			target.PeakGPUUtilization = value.PeakGPUUtilization
		}
	}
}

func aggregateCostStatus(known, unknown uint64, current, next string) string {
	if known > 0 && unknown > 0 {
		return CostPartial
	}
	if unknown > 0 {
		return CostUnknown
	}
	if current == "" {
		return next
	}
	if next == "" || current == next {
		return current
	}
	return CostPartial
}
