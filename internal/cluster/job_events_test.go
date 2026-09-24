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

	bolt "go.etcd.io/bbolt"
)

func TestAuthoritativeJobEventsFollowDurableLifecycle(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"hello"}`)})
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
	if _, err := store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"output":{"mode":"text","text":"ok"}}`), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListJobEvents(job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(page.Events))
	for index, event := range page.Events {
		types = append(types, event.Type)
		if event.Schema != JobEventSchemaV1 || event.JobID != job.ID || event.Sequence != uint64(index+1) || event.Source != "relay" || event.Authority != "authoritative" {
			t.Fatalf("invalid authoritative event %d: %#v", index, event)
		}
	}
	want := []string{"job.accepted", "job.queued", "worker.assigned", "execution.started", "job.completed"}
	if !reflect.DeepEqual(types, want) || page.Next != 5 || page.Newest != 5 || page.Gap {
		t.Fatalf("event lifecycle = %v page=%#v, want %v", types, page, want)
	}
	replayed, err := store.ListJobEvents(job.ID, 3, 1)
	if err != nil || len(replayed.Events) != 1 || replayed.Events[0].Sequence != 4 || replayed.Next != 4 {
		t.Fatalf("cursor replay = %#v err=%v", replayed, err)
	}
}

func TestAuthoritativeEventFailureRollsBackJobTransition(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.saveJobEventTestHook = func(event JobEvent) error {
		if event.Type == "job.accepted" {
			return errors.New("injected event failure")
		}
		return nil
	}
	job, err := store.CreateJob(SubmitRequest{ID: "atomic-create", OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatalf("event failure committed job: %#v", job)
	}
	if _, err := store.GetJob("atomic-create"); err == nil {
		t.Fatal("job survived failed authoritative event write")
	}
	page, err := store.ListJobEvents("atomic-create", 0, 100)
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("failed transaction leaked events: %#v err=%v", page, err)
	}

	store.saveJobEventTestHook = nil
	job, err = store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
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
	store.saveJobEventTestHook = func(event JobEvent) error {
		if event.Type == "job.completed" {
			return errors.New("injected terminal event failure")
		}
		return nil
	}
	if _, err := store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, ""); err == nil {
		t.Fatal("terminal event failure committed completion")
	}
	stored, err := store.GetJob(job.ID)
	if err != nil || stored.Status != JobRunning {
		t.Fatalf("failed terminal event changed authoritative job: %#v err=%v", stored, err)
	}
}

func TestAdvisoryProgressEventsAreBoundedContentFreeAndNonAuthoritative(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
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
	for sequence := uint64(1); sequence <= maximumAdvisoryProgressEventsPerJob+8; sequence++ {
		job, err = store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{
			Sequence: sequence, Phase: "generation", Percent: int(sequence % 101), Busy: true,
			Text: "secret generated content", Detail: "secret provider detail",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListJobEvents(job.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	progressCount := 0
	for _, event := range page.Events {
		if event.Type != "execution.progress" {
			continue
		}
		progressCount++
		if event.Authority != "advisory" || event.Source != "worker" || event.Progress == nil || event.Progress.ReportedSequence == 0 {
			t.Fatalf("invalid progress event: %#v", event)
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) == "" || containsAny(string(raw), "secret generated content", "secret provider detail") {
			t.Fatalf("progress event leaked semantic content: %s", raw)
		}
	}
	if progressCount != maximumAdvisoryProgressEventsPerJob {
		t.Fatalf("progress events = %d, want bounded %d", progressCount, maximumAdvisoryProgressEventsPerJob)
	}
	if page.Events[0].Type != "job.accepted" || page.Events[len(page.Events)-1].Type != "job.completed" {
		t.Fatalf("progress displaced lifecycle evidence: first=%s last=%s", page.Events[0].Type, page.Events[len(page.Events)-1].Type)
	}
}

func TestAdvisoryProgressEventFailureRollsBackProgress(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
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
	store.saveJobEventTestHook = func(event JobEvent) error {
		if event.Type == "execution.progress" {
			return errors.New("injected advisory event failure")
		}
		return nil
	}
	if _, err := store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Percent: 25}); err == nil {
		t.Fatal("advisory event failure committed progress")
	}
	stored, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Progress != nil {
		t.Fatalf("failed advisory event left progress behind: %#v", stored.Progress)
	}
}

func containsAny(value string, values ...string) bool {
	for _, candidate := range values {
		if candidate != "" && strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func TestJobEventRetentionReportsReplayGap(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job := Job{ID: "bounded-events", Status: JobRunning, UpdatedAt: time.Now().UTC()}
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		for index := 0; index < maximumRetainedJobEvents+4; index++ {
			job.UpdatedAt = job.UpdatedAt.Add(time.Millisecond)
			if err := appendAuthoritativeJobEventTx(tx, store, job, "job.queued"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListJobEvents(job.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != maximumRetainedJobEvents || page.OldestRetained != 5 || page.Newest != maximumRetainedJobEvents+4 || !page.Gap {
		t.Fatalf("bounded replay page = count %d oldest %d newest %d gap %v", len(page.Events), page.OldestRetained, page.Newest, page.Gap)
	}
	continued, err := store.ListJobEvents(job.ID, 4, 500)
	if err != nil || continued.Gap || len(continued.Events) != maximumRetainedJobEvents || continued.Events[0].Sequence != 5 {
		t.Fatalf("retained-cursor replay = %#v err=%v", continued, err)
	}
}

func TestJobEventEndpointScopesProducerOwnership(t *testing.T) {
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
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	if status, _ := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+job.ID+"/events?after=0&limit=1", producerB, nil); status != http.StatusForbidden {
		t.Fatalf("another producer read job events with status %d", status)
	}
	status, raw := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+job.ID+"/events?after=0&limit=1", producerA, nil)
	if status != http.StatusOK {
		t.Fatalf("owner event read returned %d: %s", status, raw)
	}
	var page JobEventPage
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Events) != 1 || page.Events[0].Type != "job.accepted" || page.Next != 1 {
		t.Fatalf("owner event page = %#v err=%v", page, err)
	}
	if status, _ := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+job.ID+"/events?after=-1", producerA, nil); status != http.StatusBadRequest {
		t.Fatalf("invalid cursor returned %d", status)
	}
}
