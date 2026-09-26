package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	executionEventStreamSchema       = "contextbridge.event-stream-control.v1"
	executionEventStreamMode         = "authoritative-events-v1"
	executionEventStreamPageLimit    = 100
	executionEventStreamPollInterval = 250 * time.Millisecond
	executionEventStreamHeartbeat    = 10 * time.Second
	executionEventStreamLifetime     = 30 * time.Second
	executionEventStreamWriteTimeout = 10 * time.Second
)

type executionEventStreamControl struct {
	Schema         string `json:"schema"`
	Type           string `json:"type"`
	After          uint64 `json:"after"`
	OldestRetained uint64 `json:"oldest_retained,omitempty"`
	Newest         uint64 `json:"newest,omitempty"`
}

func (r *Relay) handleJobEventStream(w http.ResponseWriter, req *http.Request) {
	job, err := r.store.GetJob(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if !canReadJob(req.Context(), job) {
		writeError(w, http.StatusForbidden, errors.New("job belongs to another producer"))
		return
	}
	r.streamExecutionEvents(w, req, job.ID, r.store.ListJobEvents, terminalJobStatus(job.Status))
}

func (r *Relay) handlePipelineRunEventStream(w http.ResponseWriter, req *http.Request) {
	run, err := r.store.GetPipelineRun(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("pipeline run not found"))
		return
	}
	if record, ok := tokenRecord(req.Context()); ok && record.Role == "producer" && run.OwnerSubject != record.Subject {
		writeError(w, http.StatusForbidden, errors.New("pipeline run belongs to another producer"))
		return
	}
	r.streamExecutionEvents(w, req, run.ID, r.store.ListPipelineEvents, terminalPipelineStatus(run.Status))
}

type executionEventPageReader func(string, uint64, int) (JobEventPage, error)

func (r *Relay) streamExecutionEvents(w http.ResponseWriter, req *http.Request, executionID string, read executionEventPageReader, initiallyTerminal bool) {
	after, err := executionEventStreamAfter(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	release, ok := r.acquireExecutionEventStream(req)
	if !ok {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, errors.New("execution event stream capacity reached; reconnect later"))
		return
	}
	defer release()
	page, err := read(executionID, after, executionEventStreamPageLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is unavailable for this HTTP connection"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-ContextBridge-Event-Stream", executionEventStreamMode)
	if page.Gap {
		w.Header().Set("X-ContextBridge-Event-Gap", "true")
	}
	w.WriteHeader(http.StatusOK)
	if !writeExecutionEventStreamChunk(w, flusher, []byte("retry: 1000\n\n")) {
		return
	}

	cursor := after
	gapSent := false
	deadline := time.NewTimer(executionEventStreamLifetime)
	defer deadline.Stop()
	poll := time.NewTicker(executionEventStreamPollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(executionEventStreamHeartbeat)
	defer heartbeat.Stop()

	for {
		if page.Gap && !gapSent {
			control := executionEventStreamControl{
				Schema: executionEventStreamSchema, Type: "retention_gap", After: cursor,
				OldestRetained: page.OldestRetained, Newest: page.Newest,
			}
			if !writeExecutionEventSSE(w, flusher, "contextbridge.gap", 0, control) {
				return
			}
			gapSent = true
		}
		terminalSeen := false
		for _, event := range page.Events {
			if !writeExecutionEventSSE(w, flusher, event.Type, event.Sequence, event) {
				return
			}
			cursor = event.Sequence
			if terminalExecutionEvent(event.Type) {
				terminalSeen = true
			}
		}
		if terminalSeen || (initiallyTerminal && cursor >= page.Newest) {
			return
		}
		if cursor < page.Newest {
			page, err = read(executionID, cursor, executionEventStreamPageLimit)
			if err != nil {
				return
			}
			continue
		}

		select {
		case <-req.Context().Done():
			return
		case <-deadline.C:
			_ = writeExecutionEventStreamChunk(w, flusher, []byte(": reconnect\n\n"))
			return
		case <-heartbeat.C:
			if !writeExecutionEventStreamChunk(w, flusher, []byte(": keepalive\n\n")) {
				return
			}
		case <-poll.C:
			page, err = read(executionID, cursor, executionEventStreamPageLimit)
			if err != nil {
				return
			}
		}
	}
}

func (r *Relay) acquireExecutionEventStream(req *http.Request) (func(), bool) {
	select {
	case r.eventStreamSlots <- struct{}{}:
	default:
		return nil, false
	}
	record, _ := tokenRecord(req.Context())
	principal := record.Role + "\x00" + record.Subject
	r.eventStreamMu.Lock()
	if r.eventStreams[principal] >= maximumEventStreamsPerSubject {
		r.eventStreamMu.Unlock()
		<-r.eventStreamSlots
		return nil, false
	}
	r.eventStreams[principal]++
	r.eventStreamMu.Unlock()
	return func() {
		r.eventStreamMu.Lock()
		if r.eventStreams[principal] <= 1 {
			delete(r.eventStreams, principal)
		} else {
			r.eventStreams[principal]--
		}
		r.eventStreamMu.Unlock()
		<-r.eventStreamSlots
	}, true
}

func executionEventStreamAfter(req *http.Request) (uint64, error) {
	raw := strings.TrimSpace(req.URL.Query().Get("after"))
	if raw == "" {
		raw = strings.TrimSpace(req.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return 0, nil
	}
	if len(raw) > 20 {
		return 0, errors.New("after or Last-Event-ID must be an unsigned event sequence")
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("after or Last-Event-ID must be an unsigned event sequence")
	}
	return value, nil
}

func terminalExecutionEvent(eventType string) bool {
	switch eventType {
	case "job.completed", "job.failed", "job.cancelled", "job.ambiguous", "pipeline.completed", "pipeline.failed", "pipeline.cancelled":
		return true
	default:
		return false
	}
}

func writeExecutionEventSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, sequence uint64, value interface{}) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var frame strings.Builder
	if sequence > 0 {
		fmt.Fprintf(&frame, "id: %d\n", sequence)
	}
	fmt.Fprintf(&frame, "event: %s\ndata: %s\n\n", eventType, raw)
	return writeExecutionEventStreamChunk(w, flusher, []byte(frame.String()))
}

func writeExecutionEventStreamChunk(w http.ResponseWriter, flusher http.Flusher, chunk []byte) bool {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(executionEventStreamWriteTimeout))
	_, err := w.Write(chunk)
	if err == nil {
		flusher.Flush()
	}
	_ = controller.SetWriteDeadline(time.Time{})
	return err == nil
}
