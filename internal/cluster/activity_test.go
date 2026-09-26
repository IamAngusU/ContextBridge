package cluster

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectPipelineActivityExpandsActiveAndExceptionsOnly(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	run := PipelineRun{ID: "run-a", Pipeline: "release", Status: "running", CreatedAt: base}
	run.Steps = []Job{
		{ID: "done", Step: "fetch", Status: JobCompleted, CreatedAt: base, StartedAt: base, FinishedAt: base.Add(time.Second)},
		{ID: "live", Step: "render", Status: JobRunning, CreatedAt: base, StartedAt: base.Add(2 * time.Second)},
		{ID: "bad", Step: "publish", Status: JobFailed, FailureCode: FailureExecutionStateAmbiguous, CreatedAt: base, FinishedAt: base.Add(3 * time.Second)},
	}
	projection := ProjectPipelineActivity(run, true)
	if projection.Schema != ActivityProjectionV1 || projection.Summary.Completed != 1 || projection.Summary.Active != 1 || projection.Summary.Ambiguous != 1 || len(projection.Items) != 2 {
		t.Fatalf("projection = %#v", projection)
	}
	if projection.Items[0].State != "ambiguous" || projection.Items[1].State != JobRunning {
		t.Fatalf("exception/active ordering = %#v", projection.Items)
	}
	if projection.Items[0].ID != "publish" || projection.Items[0].JobID != "bad" {
		t.Fatalf("stable step relationship lost: %#v", projection.Items[0])
	}
}

func TestPipelineActivityEndpointRefreshesChildStateAndScopesOwnership(t *testing.T) {
	const admin = "activity_admin_012345678901234567890123456789"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	owner, _, err := relay.store.CreateToken("producer", "activity-owner", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := relay.store.CreateToken("producer", "activity-other", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{ID: "run-live-activity", Pipeline: "release", OwnerSubject: "activity-owner", Status: "running", CreatedAt: time.Now().UTC()}
	if err := relay.store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	job, err := relay.store.CreateJob(SubmitRequest{
		OwnerSubject: "activity-owner", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"must-not-leak"}`),
		Pipeline: run.Pipeline, Step: "render", ParentID: run.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := relay.store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.MarkRunning(job.ID, "node-a", assigned.Attempt); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	endpoint := server.URL + "/v1/cluster/pipeline-runs/" + run.ID + "/activity"
	if status, _ := relayHTTPTest(t, http.MethodGet, endpoint, other, nil); status != http.StatusForbidden {
		t.Fatalf("foreign producer read activity with status %d", status)
	}
	status, raw := relayHTTPTest(t, http.MethodGet, endpoint, owner, nil)
	if status != http.StatusOK || strings.Contains(string(raw), "must-not-leak") {
		t.Fatalf("activity status=%d body=%s", status, raw)
	}
	var projection ActivityProjection
	if err := json.Unmarshal(raw, &projection); err != nil || len(projection.Items) != 1 || projection.Items[0].State != JobRunning || !projection.HistoryComplete {
		t.Fatalf("live activity projection=%#v err=%v", projection, err)
	}
	foreign, err := relay.store.CreateJob(SubmitRequest{
		OwnerSubject: "activity-owner", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"unrelated"}`),
		Pipeline: run.Pipeline, Step: "foreign", ParentID: "different-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	run.Steps = []Job{foreign}
	if err := relay.store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	refreshed, complete, err := relay.pipelineRunWithCurrentSteps(run.ID)
	if err != nil || complete || len(refreshed.Steps) != 1 || refreshed.Steps[0].ID != job.ID {
		t.Fatalf("explicit parent boundary was not enforced: run=%#v complete=%v err=%v", refreshed, complete, err)
	}
}

func TestProjectPipelineActivityBoundsDetailsAndPreservesExactCounts(t *testing.T) {
	run := PipelineRun{ID: "run-b", Pipeline: "fanout", Status: "running", CreatedAt: time.Now().UTC()}
	for index := 0; index < MaximumActivityDetailRows+4; index++ {
		run.Steps = append(run.Steps, Job{ID: fmt.Sprintf("job-%02d", index), Step: fmt.Sprintf("step-%02d", index), Status: JobRunning, CreatedAt: run.CreatedAt.Add(time.Duration(index))})
	}
	for index := 0; index < 7; index++ {
		run.Steps = append(run.Steps, Job{ID: fmt.Sprintf("done-%02d", index), Step: "done", Status: JobCompleted})
	}
	projection := ProjectPipelineActivity(run, false)
	if len(projection.Items) != MaximumActivityDetailRows || projection.DetailOverflow != 4 || projection.Summary.Active != 12 || projection.Summary.Completed != 7 || projection.HistoryComplete {
		t.Fatalf("bounded projection = %#v", projection)
	}
}

func TestProjectPipelineActivityNeverGroupsUnrelatedJobs(t *testing.T) {
	run := PipelineRun{ID: "run-c", Pipeline: "safe", Status: "running"}
	projection := ProjectPipelineActivity(run, true)
	if len(projection.Items) != 0 || projection.Summary != (ActivitySummary{}) {
		t.Fatalf("projection invented children without an explicit relationship: %#v", projection)
	}
}
