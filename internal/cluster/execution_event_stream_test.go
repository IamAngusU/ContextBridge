package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestJobEventStreamReplaysResumesAndPreservesOwnership(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	producerA, _, err := relay.store.CreateToken("producer", "producer-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	producerB, _, err := relay.store.CreateToken("producer", "producer-b", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"not-an-event"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	endpoint := server.URL + "/v1/cluster/jobs/" + job.ID + "/events/stream"

	status, body, headers := readEventStream(t, endpoint, producerA, "")
	if status != http.StatusOK || headers.Get("Content-Type") != "text/event-stream" || headers.Get("X-ContextBridge-Event-Stream") != executionEventStreamMode {
		t.Fatalf("stream response = %d %#v: %s", status, headers, body)
	}
	events := decodeSSEExecutionEvents(t, body)
	if len(events) < 2 || events[0].Sequence != 1 || events[len(events)-1].Type != "job.cancelled" {
		t.Fatalf("stream events = %#v", events)
	}
	if strings.Contains(body, "not-an-event") {
		t.Fatalf("job payload leaked into event stream: %s", body)
	}
	if len(relay.eventStreamSlots) != 0 {
		t.Fatalf("terminal stream did not release its slot: %d", len(relay.eventStreamSlots))
	}
	if len(relay.eventStreams) != 0 {
		t.Fatalf("terminal stream did not release its principal count: %#v", relay.eventStreams)
	}

	request, err := http.NewRequest(http.MethodGet, endpoint+"?after=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+producerA)
	request.Header.Set("Last-Event-ID", "invalid-but-overridden")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	resumedBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	resumed := decodeSSEExecutionEvents(t, string(resumedBody))
	if response.StatusCode != http.StatusOK || len(resumed) == 0 || resumed[0].Sequence <= 1 {
		t.Fatalf("resumed stream = status %d events %#v body %s", response.StatusCode, resumed, resumedBody)
	}

	if status, _, _ := readEventStream(t, endpoint, producerB, ""); status != http.StatusForbidden {
		t.Fatalf("another producer read stream with status %d", status)
	}
	if status, _, _ := readEventStream(t, endpoint, producerA, "not-a-sequence"); status != http.StatusBadRequest {
		t.Fatalf("invalid Last-Event-ID returned status %d", status)
	}
}

func TestJobEventStreamSignalsRetentionGap(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	job, err := relay.store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maximumRetainedJobEvents+4; index++ {
		job.UpdatedAt = time.Now().UTC()
		if err := relay.store.db.Update(func(tx *bolt.Tx) error {
			return appendAuthoritativeJobEventTx(tx, relay.store, job, "job.queued")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := relay.store.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	status, body, headers := readEventStream(t, server.URL+"/v1/cluster/jobs/"+job.ID+"/events/stream?after=0", admin, "")
	if status != http.StatusOK || headers.Get("X-ContextBridge-Event-Gap") != "true" {
		t.Fatalf("gap stream = %d %#v: %s", status, headers, body)
	}
	if !strings.Contains(body, "event: contextbridge.gap") || !strings.Contains(body, `"type":"retention_gap"`) {
		t.Fatalf("gap control record missing: %s", body)
	}
	events := decodeSSEExecutionEvents(t, body)
	if len(events) != maximumRetainedJobEvents || events[0].Sequence <= 1 || events[len(events)-1].Type != "job.cancelled" {
		t.Fatalf("retained stream events = count %d first %#v last %#v", len(events), events[0], events[len(events)-1])
	}
}

func TestPipelineEventStreamAndCapacityBound(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	run := PipelineRun{ID: "run-stream", Pipeline: "demo", Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
	if err := relay.store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	run.Status = "completed"
	run.FinishedAt = time.Now().UTC()
	if err := relay.store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	endpoint := server.URL + "/v1/cluster/pipeline-runs/" + run.ID + "/events/stream"
	status, body, _ := readEventStream(t, endpoint, admin, "")
	events := decodeSSEExecutionEvents(t, body)
	if status != http.StatusOK || len(events) != 2 || events[0].Type != "pipeline.started" || events[1].Type != "pipeline.completed" {
		t.Fatalf("pipeline stream = %d %#v body %s", status, events, body)
	}

	for index := 0; index < maximumExecutionEventStreams; index++ {
		relay.eventStreamSlots <- struct{}{}
	}
	status, body, headers := readEventStream(t, endpoint, admin, "")
	for index := 0; index < maximumExecutionEventStreams; index++ {
		<-relay.eventStreamSlots
	}
	if status != http.StatusServiceUnavailable || headers.Get("Retry-After") != "1" || !strings.Contains(body, "capacity reached") {
		t.Fatalf("capacity response = %d %#v: %s", status, headers, body)
	}
}

func TestEventStreamDisconnectReleasesSlot(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	job, err := relay.store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/v1/cluster/jobs/"+job.ID+"/events/stream", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+admin)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		relay.Handler().ServeHTTP(response, request)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for len(relay.eventStreamSlots) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(relay.eventStreamSlots) != 1 {
		cancel()
		<-done
		t.Fatal("event stream did not acquire a bounded slot")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event stream did not stop after client disconnect")
	}
	if len(relay.eventStreamSlots) != 0 {
		t.Fatalf("disconnected stream leaked a slot: %d", len(relay.eventStreamSlots))
	}
	if len(relay.eventStreams) != 0 {
		t.Fatalf("disconnected stream leaked a principal count: %#v", relay.eventStreams)
	}
}

func TestEventStreamCapacityIsAlsoBoundedPerSubject(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(context.WithValue(request.Context(), tokenContextKey{}, TokenRecord{Role: "producer", Subject: "producer-a"}))
	releases := make([]func(), 0, maximumEventStreamsPerSubject)
	for index := 0; index < maximumEventStreamsPerSubject; index++ {
		release, ok := relay.acquireExecutionEventStream(request)
		if !ok {
			t.Fatalf("subject stream %d was rejected before the bound", index+1)
		}
		releases = append(releases, release)
	}
	if release, ok := relay.acquireExecutionEventStream(request); ok {
		release()
		t.Fatal("subject exceeded its stream bound")
	}
	for _, release := range releases {
		release()
	}
	if len(relay.eventStreamSlots) != 0 || len(relay.eventStreams) != 0 {
		t.Fatalf("capacity bookkeeping leaked: total=%d subjects=%#v", len(relay.eventStreamSlots), relay.eventStreams)
	}
}

func readEventStream(t *testing.T, target, token, lastEventID string) (int, string, http.Header) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body), response.Header.Clone()
}

func decodeSSEExecutionEvents(t *testing.T, body string) []JobEvent {
	t.Helper()
	result := []JobEvent{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if bytes.Contains([]byte(raw), []byte(`"schema":"`+executionEventStreamSchema+`"`)) {
			continue
		}
		var event JobEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatalf("decode SSE event %q: %v", raw, err)
		}
		result = append(result, event)
	}
	return result
}
