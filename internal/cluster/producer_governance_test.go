package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func governedTestRequest(t *testing.T, owner, marker string) SubmitRequest {
	t.Helper()
	requirements := Requirements{Task: "generation", Provider: "ollama", Egress: "local_only"}
	decision, err := EvaluateExecutionPolicy(ExecutionPolicyConfig{}, "", requirements, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return SubmitRequest{OwnerSubject: owner, Requirements: requirements, PolicyDecision: decision, Payload: json.RawMessage(fmt.Sprintf(`{"prompt":%q}`, marker))}
}

func TestProducerHourlyAdmissionLimitIsDurableAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	limits := ProducerLimits{MaxQueuedJobs: 10, MaxJobsPerHour: 1}
	request := governedTestRequest(t, "producer-a", "first")
	digest := sha256.Sum256(request.Payload)
	hash := fmt.Sprintf("%x", digest[:])
	first, replayed, err := store.CreateJobAdmittedIdempotentGoverned(request, 20, limits, "one", hash)
	if err != nil || replayed {
		t.Fatalf("first governed admission: replayed=%v err=%v", replayed, err)
	}
	retry, replayed, err := store.CreateJobAdmittedIdempotentGoverned(request, 20, limits, "one", hash)
	if err != nil || !replayed || retry.ID != first.ID {
		t.Fatalf("idempotent replay consumed quota: replayed=%v first=%q retry=%q err=%v", replayed, first.ID, retry.ID, err)
	}
	if _, err := store.CreateJobAdmittedGoverned(governedTestRequest(t, "producer-a", "second"), 20, limits); !errors.Is(err, ErrOwnerRateCapacity) {
		t.Fatalf("second admission did not hit hourly limit: %v", err)
	}
	if _, err := store.CreateJobAdmittedGoverned(governedTestRequest(t, "producer-b", "independent"), 20, limits); err != nil {
		t.Fatalf("another producer did not receive an independent quota: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateJobAdmittedGoverned(governedTestRequest(t, "producer-a", "after-restart"), 20, limits); !errors.Is(err, ErrOwnerRateCapacity) {
		t.Fatalf("restart lost durable hourly limit: %v", err)
	}
}

func TestRejectedGlobalQueueAdmissionDoesNotConsumeHourlyQuota(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	filler, err := store.CreateJobAdmitted(governedTestRequest(t, "filler", "queue-full"), 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	limits := ProducerLimits{MaxQueuedJobs: 10, MaxJobsPerHour: 1}
	if _, err := store.CreateJobAdmittedGoverned(governedTestRequest(t, "bounded", "rejected"), 1, limits); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue was not rejected before quota accounting: %v", err)
	}
	if _, err := store.CancelJob(filler.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJobAdmittedGoverned(governedTestRequest(t, "bounded", "accepted"), 1, limits); err != nil {
		t.Fatalf("rejected admission consumed the hourly quota: %v", err)
	}
}

func TestProducerTokenScopesProviderEgressAndQueue(t *testing.T) {
	record := TokenRecord{Role: "producer", ProducerLimits: ProducerLimits{Providers: []string{"ollama"}, Egress: "local_only"}}
	requirements := Requirements{Task: "generation"}
	if err := scopeRequirements(&requirements, record); err != nil {
		t.Fatal(err)
	}
	if requirements.Provider != "ollama" || requirements.Egress != "local_only" {
		t.Fatalf("single-provider/local scope was not applied: %#v", requirements)
	}
	denied := Requirements{Task: "generation", Provider: "adapter", Egress: "remote_allowed"}
	if err := scopeRequirements(&denied, record); err == nil {
		t.Fatal("provider/egress scope was widened by the request")
	}

	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.CreateTokenWithLimits("observer", "observer", nil, time.Hour, ProducerLimits{MaxJobsPerHour: 1}); err == nil {
		t.Fatal("non-producer token accepted producer limits")
	}
	token, saved, err := store.CreateTokenWithLimits("producer", "website", nil, time.Hour, record.ProducerLimits)
	if err != nil || token == "" || saved.ProducerLimits.Egress != "local_only" || len(saved.ProducerLimits.Providers) != 1 {
		t.Fatalf("producer limits were not persisted: %#v %v", saved, err)
	}
}

func TestProducerHourlyLimitReturnsStableHTTP429(t *testing.T) {
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345", AllowedTasks: []string{"generation"}, MaxJobBytes: 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	token, _, err := relay.store.CreateTokenWithLimits("producer", "limited", nil, time.Hour, ProducerLimits{MaxJobsPerHour: 1, Providers: []string{"ollama"}, Egress: "local_only"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"requirements":{"task":"generation"},"payload":{"prompt":"bounded"}}`)
	status, response := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", token, body)
	if status != http.StatusAccepted {
		t.Fatalf("first admission = %d: %s", status, response)
	}
	status, response = relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", token, body)
	var problem contractErrorResponse
	if err := json.Unmarshal(response, &problem); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusTooManyRequests || problem.Code != AdmissionCodeCapacityOwnerRate {
		t.Fatalf("second admission = %d %#v", status, problem)
	}
}

func TestTokenAPIValidatesAndPersistsProducerLimits(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	body := []byte(`{"role":"producer","subject":"bounded-api","producer_limits":{"max_queued_jobs":2,"max_jobs_per_hour":10,"providers":["ollama"],"egress":"local_only"}}`)
	status, response := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/tokens", admin, body)
	if status != http.StatusCreated {
		t.Fatalf("token API = %d: %s", status, response)
	}
	var created struct {
		Record TokenRecord `json:"record"`
	}
	if err := json.Unmarshal(response, &created); err != nil {
		t.Fatal(err)
	}
	if created.Record.ProducerLimits.MaxQueuedJobs != 2 || created.Record.ProducerLimits.MaxJobsPerHour != 10 || created.Record.ProducerLimits.Egress != "local_only" {
		t.Fatalf("token API lost governance: %#v", created.Record)
	}
	body = []byte(`{"role":"observer","subject":"observer","producer_limits":{"max_jobs_per_hour":1}}`)
	status, _ = relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/tokens", admin, body)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("observer producer limits were not rejected: HTTP %d", status)
	}
}

func TestPipelineKeepsIssuingProducerLimitsAfterRequestReturns(t *testing.T) {
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345", AllowedTasks: []string{"generation"}, MaxJobBytes: 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	limits := ProducerLimits{MaxQueuedJobs: 10, MaxJobsPerHour: 1}
	if _, err := relay.store.CreateJobAdmittedGoverned(governedTestRequest(t, "pipeline-owner", "preexisting"), 20, limits); err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{ID: "governed-pipeline", Pipeline: "test", OwnerSubject: "pipeline-owner", ProducerLimits: limits, Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
	if err := relay.store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	relay.executePipeline(context.Background(), run, Pipeline{Steps: []PipelineStep{{Name: "step", Requirements: Requirements{Task: "generation", Provider: "ollama", Egress: "local_only"}, Input: `{"prompt":"pipeline"}`}}})
	saved, err := relay.store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "failed" || saved.Error != ErrOwnerRateCapacity.Error() {
		t.Fatalf("pipeline escaped producer quota: %#v", saved)
	}
}
