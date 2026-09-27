package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProducerSubmissionUsesRelayAssignedJobIDs(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin,
		AllowedTasks: []string{"generation"}, MaxQueuedJobs: 10,
	}, nil)
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
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	explicit := SubmitRequest{ID: "predictable-order-42", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"bounded"}`)}
	raw, err := json.Marshal(explicit)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/v1/cluster/jobs", "/v1/cluster/contracts/validate"} {
		status, body := relayHTTPTest(t, http.MethodPost, server.URL+endpoint, producerA, raw)
		var problem contractErrorResponse
		if err := json.Unmarshal(body, &problem); err != nil {
			t.Fatalf("decode %s response: %v: %s", endpoint, err, body)
		}
		if status != http.StatusUnprocessableEntity || problem.Code != AdmissionCodeJobIDRelayAssigned {
			t.Fatalf("%s accepted producer ID: status=%d problem=%#v", endpoint, status, problem)
		}
	}
	if _, err := relay.store.GetJob(explicit.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected producer ID became durable: %v", err)
	}

	withoutID := SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"bounded"}`)}
	raw, err = json.Marshal(withoutID)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make([]Job, 0, 2)
	for _, token := range []string{producerA, producerB} {
		status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", token, raw)
		var job Job
		if err := json.Unmarshal(body, &job); err != nil {
			t.Fatalf("decode accepted job: %v: %s", err, body)
		}
		if status != http.StatusAccepted || !strings.HasPrefix(job.ID, "job_") {
			t.Fatalf("relay-assigned admission = status %d job %#v", status, job)
		}
		accepted = append(accepted, job)
	}
	if accepted[0].ID == accepted[1].ID {
		t.Fatalf("two producers received the same relay ID: %s", accepted[0].ID)
	}

	adminInput := SubmitRequest{ID: "operator-maintenance-job", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)}
	raw, err = json.Marshal(adminInput)
	if err != nil {
		t.Fatal(err)
	}
	status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", admin, raw)
	if status != http.StatusAccepted || !bytes.Contains(body, []byte(`"id":"operator-maintenance-job"`)) {
		t.Fatalf("trusted admin explicit ID = status %d body %s", status, body)
	}
}

func TestReservedSubmissionBindsEchoedJobID(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := PolicyDecision{
		Schema: PolicyDecisionV1, Outcome: "allow", ReasonCodes: []string{PolicyCodeAllowed}, RuleID: "default",
		PolicyFingerprint: "sha256:" + strings.Repeat("1", 64), EgressClass: "local", ProviderClassification: "local",
		CostEnforcement: "not_requested", EvaluatedAt: time.Now().UTC(),
	}
	assignment := Assignment{
		ID: "assignment-bound-id", JobID: "job-bound-id", NodeID: "node-a", OwnerSubject: "producer-a", Attempt: 1,
		ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: Requirements{Task: "generation", Provider: "ollama"}, PolicyDecision: policy,
	}
	if err := store.CreateReservationAdmitted(assignment, "secret", "producer-a", 10, 10); err != nil {
		t.Fatal(err)
	}
	sealed := &SealedEnvelope{Algorithm: sealedAlgorithm, Ciphertext: "opaque"}
	if _, err := store.ConsumeReservationAdmittedGovernedWithPolicy(assignment.ID, "secret", "attacker-selected", sealed, "test", "", "producer-a", 0, 1, 10, ProducerLimits{}, policy); !errors.Is(err, ErrReservationContextMismatch) {
		t.Fatalf("mismatched reserved job ID was accepted: %v", err)
	}
	job, err := store.ConsumeReservationAdmittedGovernedWithPolicy(assignment.ID, "secret", assignment.JobID, sealed, "test", "", "producer-a", 0, 1, 10, ProducerLimits{}, policy)
	if err != nil || job.ID != assignment.JobID {
		t.Fatalf("relay-issued reserved job ID was rejected: %#v %v", job, err)
	}
}

func TestProducerPointLookupsHideForeignResourceExistence(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	owner, _, err := relay.store.CreateToken("producer", "producer-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := relay.store.CreateToken("producer", "producer-b", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	observer, _, err := relay.store.CreateToken("observer", "observer-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	job, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{ID: "run-private", Pipeline: "demo", OwnerSubject: "producer-a", Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
	if err := relay.store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	jobCases := []struct {
		method string
		suffix string
	}{
		{http.MethodGet, ""},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/events/stream"},
		{http.MethodGet, "/route"},
		{http.MethodGet, "/estimate"},
		{http.MethodDelete, ""},
	}
	for _, tc := range jobCases {
		foreignStatus, foreignBody := relayHTTPTest(t, tc.method, server.URL+"/v1/cluster/jobs/"+job.ID+tc.suffix, foreign, nil)
		missingStatus, missingBody := relayHTTPTest(t, tc.method, server.URL+"/v1/cluster/jobs/missing-job"+tc.suffix, foreign, nil)
		if foreignStatus != http.StatusNotFound || missingStatus != http.StatusNotFound || !bytes.Equal(foreignBody, missingBody) {
			t.Fatalf("%s jobs/{id}%s oracle remains: foreign=(%d %s) missing=(%d %s)", tc.method, tc.suffix, foreignStatus, foreignBody, missingStatus, missingBody)
		}
	}
	if saved, err := relay.store.GetJob(job.ID); err != nil || saved.Status != JobQueued {
		t.Fatalf("foreign cancellation changed job: %#v %v", saved, err)
	}

	for _, suffix := range []string{"", "/activity", "/events", "/events/stream"} {
		foreignStatus, foreignBody := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/pipeline-runs/"+run.ID+suffix, foreign, nil)
		missingStatus, missingBody := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/pipeline-runs/missing-run"+suffix, foreign, nil)
		if foreignStatus != http.StatusNotFound || missingStatus != http.StatusNotFound || !bytes.Equal(foreignBody, missingBody) {
			t.Fatalf("pipeline-runs/{id}%s oracle remains: foreign=(%d %s) missing=(%d %s)", suffix, foreignStatus, foreignBody, missingStatus, missingBody)
		}
	}

	for _, token := range []string{owner, observer, admin} {
		status, _ := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+job.ID, token, nil)
		if status != http.StatusOK {
			t.Fatalf("authorized job read with token %q returned %d", token, status)
		}
		status, _ = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/pipeline-runs/"+run.ID, token, nil)
		if status != http.StatusOK {
			t.Fatalf("authorized pipeline read with token %q returned %d", token, status)
		}
	}
}
