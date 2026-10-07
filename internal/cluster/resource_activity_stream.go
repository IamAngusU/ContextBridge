package cluster

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const resourceActivityStreamMode = "activity-snapshots-v1"

// This opt-in semantic stream is deliberately separate from the content-minimized
// lifecycle log. Each event replaces the previous snapshot; there is no replay.
func (r *Relay) handleJobActivityStream(w http.ResponseWriter, req *http.Request) {
	if _, err := r.visibleJob(req.Context(), req.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if req.URL.Query().Has("after") || req.Header.Get("Last-Event-ID") != "" {
		writeError(w, http.StatusBadRequest, errors.New("activity streams send current snapshots, not event replay; omit after and Last-Event-ID"))
		return
	}
	release, ok := r.acquireExecutionEventStream(req)
	if !ok {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, errors.New("execution event stream capacity reached; reconnect later"))
		return
	}
	defer release()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is unavailable for this HTTP connection"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-ContextBridge-Event-Stream", resourceActivityStreamMode)
	token := strings.TrimSpace(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
	deadline := time.NewTimer(executionEventStreamLifetime)
	defer deadline.Stop()
	poll := time.NewTicker(executionEventStreamPollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(executionEventStreamHeartbeat)
	defer heartbeat.Stop()
	var previous [sha256.Size]byte
	for {
		// Reauthenticate and recheck current tenant/owner visibility before every
		// snapshot, rather than retaining the connection's initial token scope.
		record, valid := r.store.Authenticate(token)
		if !valid || (record.Role != "admin" && record.Role != "observer" && record.Role != "producer") {
			return
		}
		job, err := r.visibleJob(withTokenRecord(req.Context(), record), req.PathValue("id"))
		if err != nil {
			return
		}
		view := ProjectJobResourceActivity(job)
		raw, err := json.Marshal(view)
		if err != nil {
			return
		}
		digest := sha256.Sum256(raw)
		if digest != previous {
			if !writeExecutionEventSSE(w, flusher, "activity.snapshot", 0, view) {
				return
			}
			previous = digest
		}
		if terminalJobStatus(job.Status) || view.EvidenceStatus == "not_requested" || view.EvidenceStatus == "encrypted" {
			return
		}
		select {
		case <-req.Context().Done():
			return
		case <-deadline.C:
			return
		case <-heartbeat.C:
			if !writeExecutionEventStreamChunk(w, flusher, []byte(": keepalive\n\n")) {
				return
			}
		case <-poll.C:
		}
	}
}

func activityStreamOpenAPI(operation map[string]interface{}) map[string]interface{} {
	operation["responses"] = map[string]interface{}{
		"200": map[string]interface{}{
			"description": "Authenticated current activity.snapshot events (JobResourceActivity), no replay or SSE IDs. Reconnect for latest snapshot. Stream expires after 30 seconds; permission is rechecked during streaming.",
			"content":     map[string]interface{}{"text/event-stream": map[string]interface{}{"schema": map[string]string{"type": "string"}}},
		},
		"400": map[string]string{"description": "Replay cursor is not supported"},
		"401": map[string]string{"description": "Authentication required"},
		"404": map[string]string{"description": "Job not found or not visible"},
		"503": map[string]string{"description": "Shared execution stream capacity exhausted"},
	}
	return operation
}
