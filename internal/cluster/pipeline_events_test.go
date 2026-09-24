package cluster

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPipelineEventsAreAtomicOrderedAndStepScoped(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run := PipelineRun{ID: "run-events", Pipeline: "demo", OwnerSubject: "producer-a", Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(SubmitRequest{
		OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`),
		Pipeline: "demo", Step: "render", ParentID: run.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.MarkRunning(job.ID, "node-a", job.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "")
	if err != nil {
		t.Fatal(err)
	}
	run.Steps = append(run.Steps, job)
	run.Status = "completed"
	run.FinishedAt = time.Now().UTC()
	if err := store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListPipelineEvents(run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(page.Events))
	for index, event := range page.Events {
		types = append(types, event.Type)
		if event.Sequence != uint64(index+1) || event.RunID != run.ID || event.Authority != "authoritative" || event.Source != "relay" {
			t.Fatalf("invalid pipeline event %d: %#v", index, event)
		}
		if strings.HasPrefix(event.Type, "pipeline.step.") && (event.StepID != "render" || event.JobID != job.ID) {
			t.Fatalf("step event lost stable identity: %#v", event)
		}
	}
	want := []string{"pipeline.started", "pipeline.step.queued", "pipeline.step.started", "pipeline.step.completed", "pipeline.completed"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("pipeline event lifecycle = %v, want %v", types, want)
	}
}

func TestPipelineEventFailureRollsBackOwningState(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.saveJobEventTestHook = func(event JobEvent) error {
		if event.Type == "pipeline.started" {
			return errors.New("injected pipeline event failure")
		}
		return nil
	}
	run := PipelineRun{ID: "run-atomic-events", Pipeline: "demo", OwnerSubject: "producer-a", Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err == nil {
		t.Fatal("pipeline admission survived failed authoritative event")
	}
	if _, err := store.GetPipelineRun(run.ID); err == nil {
		t.Fatal("pipeline run was committed without pipeline.started")
	}
	store.saveJobEventTestHook = nil
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	store.saveJobEventTestHook = func(event JobEvent) error {
		if event.Type == "pipeline.step.queued" {
			return errors.New("injected step event failure")
		}
		return nil
	}
	if _, err := store.CreateJob(SubmitRequest{ID: "atomic-step-job", OwnerSubject: "producer-a", Payload: json.RawMessage(`{}`), Pipeline: "demo", Step: "step-a", ParentID: run.ID}); err == nil {
		t.Fatal("job admission survived failed pipeline step event")
	}
	if _, err := store.GetJob("atomic-step-job"); err == nil {
		t.Fatal("pipeline child job committed without step event")
	}
}

func TestPipelineEventEndpointScopesProducerOwnership(t *testing.T) {
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
	run := PipelineRun{ID: "run-api-events", Pipeline: "demo", OwnerSubject: "producer-a", Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
	if err := relay.store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	endpoint := server.URL + "/v1/cluster/pipeline-runs/" + run.ID + "/events"
	if status, _ := relayHTTPTest(t, http.MethodGet, endpoint, producerB, nil); status != http.StatusForbidden {
		t.Fatalf("another producer read pipeline events with status %d", status)
	}
	status, raw := relayHTTPTest(t, http.MethodGet, endpoint, producerA, nil)
	if status != http.StatusOK {
		t.Fatalf("owner pipeline event read returned %d: %s", status, raw)
	}
	var page JobEventPage
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Events) != 1 || page.Events[0].Type != "pipeline.started" {
		t.Fatalf("pipeline event page = %#v err=%v", page, err)
	}
}
