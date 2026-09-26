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
	record := TokenRecord{Role: "producer", ProducerLimits: ProducerLimits{Providers: []string{"ollama"}, Egress: "local_only", RequireE2EE: true}}
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
	if _, _, err := store.CreateTokenWithLimits("observer", "observer", nil, time.Hour, ProducerLimits{RequireE2EE: true}); err == nil {
		t.Fatal("non-producer token accepted an E2EE producer requirement")
	}
	token, saved, err := store.CreateTokenWithLimits("producer", "website", nil, time.Hour, record.ProducerLimits)
	if err != nil || token == "" || saved.ProducerLimits.Egress != "local_only" || len(saved.ProducerLimits.Providers) != 1 || !saved.ProducerLimits.RequireE2EE {
		t.Fatalf("producer limits were not persisted: %#v %v", saved, err)
	}
	if _, err := store.CreateJobAdmittedGoverned(governedTestRequest(t, "website", "cleartext"), 10, record.ProducerLimits); !errors.Is(err, ErrE2EERequired) {
		t.Fatalf("durable admission accepted cleartext under an E2EE-only credential: %v", err)
	}
	sealed := governedTestRequest(t, "website", "sealed")
	sealed.Payload = nil
	sealed.Sealed = &SealedEnvelope{Algorithm: sealedAlgorithm, Ciphertext: "opaque"}
	if _, err := store.CreateJobAdmittedGoverned(sealed, 10, record.ProducerLimits); err != nil {
		t.Fatalf("durable admission rejected a sealed payload: %v", err)
	}
}

func TestProducerTenantScopeBindsAndRejectsExactly(t *testing.T) {
	record := TokenRecord{Role: "producer", ProducerLimits: ProducerLimits{AllowedTenants: []string{"tenant-a"}}}
	tenant := ""
	if err := scopeTenantID(&tenant, record); err != nil || tenant != "tenant-a" {
		t.Fatalf("single tenant scope was not applied: tenant=%q err=%v", tenant, err)
	}
	tenant = "tenant-b"
	if err := scopeTenantID(&tenant, record); !errors.Is(err, ErrTenantScopeForbidden) {
		t.Fatalf("different tenant was not rejected: %v", err)
	}
	tenant = "TENANT-A"
	if err := scopeTenantID(&tenant, record); !errors.Is(err, ErrTenantScopeForbidden) {
		t.Fatalf("case-changing a policy selector escaped its exact scope: %v", err)
	}
	tenant = ""
	record.ProducerLimits.AllowedTenants = []string{"tenant-a", "tenant-b"}
	if err := scopeTenantID(&tenant, record); err == nil {
		t.Fatal("multi-tenant credential did not require an explicit tenant_id")
	}
	tenant = "caller-label"
	if err := scopeTenantID(&tenant, TokenRecord{Role: "producer"}); err != nil || tenant != "caller-label" {
		t.Fatalf("unscoped credential changed backwards-compatible tenant behavior: tenant=%q err=%v", tenant, err)
	}
}

func TestProducerTenantScopeIsDurableAndEnforcedByStore(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	limits := ProducerLimits{AllowedTenants: []string{"tenant-a"}}
	token, saved, err := store.CreateTokenWithLimits("producer", "tenant-app", nil, time.Hour, limits)
	if err != nil || token == "" || len(saved.ProducerLimits.AllowedTenants) != 1 || saved.ProducerLimits.AllowedTenants[0] != "tenant-a" {
		t.Fatalf("tenant scope was not persisted: %#v %v", saved, err)
	}
	if _, _, err := store.CreateTokenWithLimits("observer", "observer", nil, time.Hour, limits); err == nil {
		t.Fatal("non-producer token accepted a tenant scope")
	}
	if _, _, err := store.CreateTokenWithLimits("producer", "ambiguous", nil, time.Hour, ProducerLimits{AllowedTenants: []string{"Tenant-A", "tenant-a"}}); err == nil {
		t.Fatal("case-insensitively ambiguous tenant scope was accepted")
	}
	denied := governedTestRequest(t, "tenant-app", "denied")
	denied.TenantID = "tenant-b"
	if _, err := store.CreateJobAdmittedGoverned(denied, 10, limits); !errors.Is(err, ErrTenantScopeForbidden) {
		t.Fatalf("store admitted a job outside its durable tenant scope: %v", err)
	}
	allowed := governedTestRequest(t, "tenant-app", "allowed")
	allowed.TenantID = "tenant-a"
	if _, err := store.CreateJobAdmittedGoverned(allowed, 10, limits); err != nil {
		t.Fatalf("store rejected the exact allowed tenant: %v", err)
	}

	policy, err := EvaluateExecutionPolicy(ExecutionPolicyConfig{}, "tenant-a", Requirements{Task: "generation"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	assignment := Assignment{
		ID: "tenant-scope-assignment", JobID: "tenant-scope-job", NodeID: "node-a", PublicKey: "key",
		OwnerSubject: "tenant-app", TenantID: "tenant-a", ExpiresAt: time.Now().UTC().Add(time.Minute),
		Requirements: Requirements{Task: "generation"}, PolicyDecision: policy,
	}
	if err := store.CreateReservationAdmitted(assignment, "tenant-scope-secret", "tenant-app", 10, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeReservationAdmittedGovernedWithPolicy(assignment.ID, "tenant-scope-secret", &SealedEnvelope{Algorithm: sealedAlgorithm, Ciphertext: "opaque"}, "test", "tenant-b", "tenant-app", 0, 1, 10, limits, policy); !errors.Is(err, ErrTenantScopeForbidden) {
		t.Fatalf("reserved admission escaped its durable tenant scope: %v", err)
	}
}

func TestProducerTenantScopePrecedesPolicySelectionAcrossRelaySurfaces(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}, MaxJobBytes: 4096,
		ExecutionPolicy: ExecutionPolicyConfig{
			Enabled: true, TenantMode: "listed_only", RequireTenant: true, LocalProviders: []string{"ollama"},
			Tenants: map[string]ExecutionPolicyRule{"tenant-a": {}, "tenant-b": {}},
		},
		Pipelines: map[string]Pipeline{"other-tenant": {
			TenantID: "tenant-b", Steps: []PipelineStep{{Name: "one", Requirements: Requirements{Task: "generation", Provider: "ollama"}, Input: `{"prompt":"hello"}`}},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	token, _, err := relay.store.CreateTokenWithLimits("producer", "tenant-app", nil, time.Hour, ProducerLimits{AllowedTenants: []string{"tenant-a"}})
	if err != nil {
		t.Fatal(err)
	}

	jobBody := []byte(`{"tenant_id":"tenant-b","requirements":{"task":"generation","provider":"ollama"},"payload":{"prompt":"bounded"}}`)
	assignmentBody := []byte(`{"tenant_id":"tenant-b","requirements":{"task":"generation","provider":"ollama"}}`)
	for _, test := range []struct {
		name string
		path string
		body []byte
	}{
		{"submit", "/v1/cluster/jobs", jobBody},
		{"validate", "/v1/cluster/contracts/validate", jobBody},
		{"route explain", "/v1/cluster/routes/explain", assignmentBody},
		{"assignment", "/v1/cluster/assign", assignmentBody},
		{"pipeline", "/v1/cluster/pipelines/other-tenant/run", []byte(`{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, response := relayHTTPTest(t, http.MethodPost, server.URL+test.path, token, test.body)
			var problem contractErrorResponse
			if err := json.Unmarshal(response, &problem); err != nil {
				t.Fatal(err)
			}
			if status != http.StatusForbidden || problem.Code != AdmissionCodeTenantScopeForbidden {
				t.Fatalf("tenant scope result = %d %#v", status, problem)
			}
		})
	}

	status, response := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", token, []byte(`{"requirements":{"task":"generation","provider":"ollama"},"payload":{"prompt":"default tenant"}}`))
	if status != http.StatusAccepted {
		t.Fatalf("single-tenant default admission = %d: %s", status, response)
	}
	var accepted Job
	if err := json.Unmarshal(response, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TenantID != "tenant-a" {
		t.Fatalf("admitted job tenant = %q, want tenant-a", accepted.TenantID)
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
	body := []byte(`{"role":"producer","subject":"bounded-api","producer_limits":{"max_queued_jobs":2,"max_jobs_per_hour":10,"providers":["ollama"],"allowed_tenants":["tenant-a"],"egress":"local_only","require_e2ee":true}}`)
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
	if created.Record.ProducerLimits.MaxQueuedJobs != 2 || created.Record.ProducerLimits.MaxJobsPerHour != 10 || created.Record.ProducerLimits.Egress != "local_only" || len(created.Record.ProducerLimits.AllowedTenants) != 1 || created.Record.ProducerLimits.AllowedTenants[0] != "tenant-a" || !created.Record.ProducerLimits.RequireE2EE {
		t.Fatalf("token API lost governance: %#v", created.Record)
	}
	body = []byte(`{"role":"observer","subject":"observer","producer_limits":{"max_jobs_per_hour":1}}`)
	status, _ = relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/tokens", admin, body)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("observer producer limits were not rejected: HTTP %d", status)
	}
}

func TestE2EEOnlyProducerCannotStartCleartextPipeline(t *testing.T) {
	const admin = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, AllowedTasks: []string{"generation"}, MaxJobBytes: 4096,
		Pipelines: map[string]Pipeline{"linear": {Steps: []PipelineStep{{Name: "one", Requirements: Requirements{Task: "generation", Provider: "ollama"}, Input: `{"prompt":"hello"}`}}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	token, _, err := relay.store.CreateTokenWithLimits("producer", "sealed-app", nil, time.Hour, ProducerLimits{RequireE2EE: true})
	if err != nil {
		t.Fatal(err)
	}
	status, response := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/pipelines/linear/run", token, []byte(`{}`))
	var problem contractErrorResponse
	if err := json.Unmarshal(response, &problem); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusForbidden || problem.Code != AdmissionCodeE2EERequired {
		t.Fatalf("cleartext pipeline with E2EE-only credential = %d %#v", status, problem)
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
