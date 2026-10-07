package cluster

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func activityResult() json.RawMessage {
	return json.RawMessage(`{"output":{"mode":"text","provider":"adapter","text":"private answer","activity":{"schema":"contextbridge.resource-activity.v1","truncated":true,"items":[{"id":"a1","kind":"file","action":"read","label":"README.md","ref":"res_one"},{"id":"a2","kind":"web","action":"cited","label":"Docs","url":"https://example.test/docs"},{"id":"a3","kind":"image","action":"inspected","label":"sample.png"}]}}}`)
}

func TestJobActivityEvidenceIsBoundedSeparateFromLifecycle(t *testing.T) {
	job := Job{ID: "job-a", Status: JobCompleted, Requirements: Requirements{Provider: "adapter"},
		Payload: json.RawMessage(`{"prompt":"private prompt","output":{"activity":true}}`), Result: activityResult()}
	view := ProjectJobResourceActivity(job)
	if view.EvidenceSource != "adapter_reported" || view.Counts["file.read"] != 1 || view.Counts["web.cited"] != 1 ||
		view.Counts["image.inspected"] != 1 || len(view.Resources) != 3 || !view.Truncated {
		t.Fatalf("view = %+v", view)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "subagent") {
		t.Fatalf("content/group leakage: %s", raw)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*Job)
	}{
		{"pending", "pending", func(j *Job) { j.Status = JobRunning }},
		{"no opt in", "not_requested", func(j *Job) { j.Payload = json.RawMessage(`{}`) }},
		{"sealed input", "encrypted", func(j *Job) { j.SealedPayload = &SealedEnvelope{} }},
		{"sealed result", "encrypted", func(j *Job) { j.SealedResult = &SealedEnvelope{} }},
		{"model result", "not_reported", func(j *Job) { j.Requirements.Provider = "ollama" }},
		{"unreported", "not_reported", func(j *Job) { j.Result = json.RawMessage(`{"output":{"provider":"adapter","text":"I opened a.png"}}`) }},
		{"bad manifest", "invalid", func(j *Job) {
			j.Result = json.RawMessage(strings.ReplaceAll(string(j.Result), "README.md", "../secret"))
		}},
		{"duplicate manifest", "invalid", func(j *Job) {
			j.Result = json.RawMessage(strings.ReplaceAll(string(j.Result), `"truncated":true`, `"truncated":true,"truncated":false`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := job
			tc.mutate(&candidate)
			got := ProjectJobResourceActivity(candidate)
			if got.EvidenceStatus != tc.want || len(got.Resources) != 0 || len(got.Counts) != 0 {
				t.Fatalf("projection = %+v", got)
			}
		})
	}
}

func TestJobActivityEndpointOwnershipPersistenceAndNoHistoryLeak(t *testing.T) {
	const admin = "resource_activity_admin_012345678901234567890"
	database := filepath.Join(t.TempDir(), "relay.db")
	relay, err := NewRelay(RelayConfig{Database: database, AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { relay.Close() }()
	owner, _, err := relay.store.CreateToken("producer", "owner", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := relay.store.CreateToken("producer", "other", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "owner", Requirements: Requirements{Provider: "adapter"},
		Payload: json.RawMessage(`{"prompt":"private prompt","output":{"activity":true}}`)})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := relay.store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.CompleteJob(job.ID, "node-a", assigned.Attempt, activityResult(), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	// A restart must not lose evidence; it is backed by the same persisted result.
	relay.Close()
	relay, err = NewRelay(RelayConfig{Database: database, AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	endpoint := server.URL + "/v1/cluster/jobs/" + job.ID + "/activity"
	for _, token := range []string{owner, admin} {
		status, body := relayHTTPTest(t, http.MethodGet, endpoint, token, nil)
		if status != 200 || !strings.Contains(string(body), `"file.read":1`) || strings.Contains(string(body), "private") {
			t.Fatalf("activity = %d: %s", status, body)
		}
	}
	for _, target := range []string{endpoint, server.URL + "/v1/cluster/jobs/missing/activity"} {
		if status, _ := relayHTTPTest(t, http.MethodGet, target, other, nil); status != 404 {
			t.Fatalf("other status %d", status)
		}
	}
	if status, _ := relayHTTPTest(t, http.MethodGet, endpoint, "", nil); status != 401 {
		t.Fatalf("anonymous status %d", status)
	}
	for _, suffix := range []string{"/v1/cluster/jobs", "/v1/cluster/jobs/" + job.ID + "/events"} {
		_, body := relayHTTPTest(t, http.MethodGet, server.URL+suffix, owner, nil)
		if strings.Contains(string(body), "README.md") || strings.Contains(string(body), "private answer") {
			t.Fatalf("history/events acquired semantic resource evidence: %s", body)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+owner)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("activity response cacheable")
	}
}
