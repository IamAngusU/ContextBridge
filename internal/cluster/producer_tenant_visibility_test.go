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

func TestProducerTenantScopeConstrainsExistingExecutionSurfaces(t *testing.T) {
	const (
		admin   = "admin_tenant_visibility_012345678901234567890123"
		subject = "shared-producer"
	)
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	jobA, err := relay.store.CreateJob(SubmitRequest{
		ID: "tenant-a-job", OwnerSubject: subject, TenantID: "tenant-a",
		Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"tenant a"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	jobB, err := relay.store.CreateJob(SubmitRequest{
		ID: "tenant-b-job", OwnerSubject: subject, TenantID: "tenant-b",
		Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"tenant b secret"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	runB := PipelineRun{
		ID: "tenant-b-run", Pipeline: "test", OwnerSubject: subject, TenantID: "tenant-b",
		Status: "running", Input: json.RawMessage(`{"secret":"tenant b pipeline"}`), CreatedAt: time.Now().UTC(),
	}
	if err := relay.store.CreatePipelineRunAdmitted(runB, 10, 10); err != nil {
		t.Fatal(err)
	}

	tokenA := createTenantVisibilityToken(t, relay, subject, []string{"tenant-a"})
	tokenB := createTenantVisibilityToken(t, relay, subject, []string{"tenant-b"})
	tokenAB := createTenantVisibilityToken(t, relay, subject, []string{"tenant-a", "tenant-b"})
	unscoped := createTenantVisibilityToken(t, relay, subject, nil)
	caseVariant := createTenantVisibilityToken(t, relay, subject, []string{"Tenant-A"})

	for _, path := range []string{
		"/v1/cluster/jobs/" + jobB.ID,
		"/v1/cluster/jobs/" + jobB.ID + "/events",
		"/v1/cluster/jobs/" + jobB.ID + "/events/stream",
		"/v1/cluster/jobs/" + jobB.ID + "/route",
		"/v1/cluster/jobs/" + jobB.ID + "/estimate",
		"/v1/cluster/pipeline-runs/" + runB.ID,
		"/v1/cluster/pipeline-runs/" + runB.ID + "/activity",
		"/v1/cluster/pipeline-runs/" + runB.ID + "/events",
		"/v1/cluster/pipeline-runs/" + runB.ID + "/events/stream",
	} {
		status, body := relayHTTPTest(t, http.MethodGet, server.URL+path, tokenA, nil)
		if status != http.StatusNotFound || !strings.Contains(string(body), "not found") {
			t.Errorf("tenant-a credential GET %s = %d: %s", path, status, body)
		}
	}

	status, body := relayHTTPTest(t, http.MethodDelete, server.URL+"/v1/cluster/jobs/"+jobB.ID, tokenA, nil)
	if status != http.StatusNotFound || !strings.Contains(string(body), "job not found") {
		t.Fatalf("cross-tenant cancellation = %d: %s", status, body)
	}
	unchanged, err := relay.store.GetJob(jobB.ID)
	if err != nil || unchanged.Status != JobQueued {
		t.Fatalf("cross-tenant cancellation changed job: %#v err=%v", unchanged, err)
	}

	status, body = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs", tokenA, nil)
	if status != http.StatusOK || !strings.Contains(string(body), jobA.ID) || strings.Contains(string(body), jobB.ID) {
		t.Fatalf("legacy tenant-scoped history = %d: %s", status, body)
	}
	status, body = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs?page=1", tokenA, nil)
	if status != http.StatusOK || !strings.Contains(string(body), jobA.ID) || strings.Contains(string(body), jobB.ID) {
		t.Fatalf("paged tenant-scoped history = %d: %s", status, body)
	}

	for name, token := range map[string]string{"tenant-b": tokenB, "multi-tenant": tokenAB, "unscoped": unscoped, "admin": admin} {
		status, body = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+jobB.ID, token, nil)
		if status != http.StatusOK || !strings.Contains(string(body), "tenant b secret") {
			t.Errorf("%s credential lost legitimate job access: %d: %s", name, status, body)
		}
		status, body = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/pipeline-runs/"+runB.ID, token, nil)
		if status != http.StatusOK || !strings.Contains(string(body), "tenant b pipeline") {
			t.Errorf("%s credential lost legitimate pipeline access: %d: %s", name, status, body)
		}
	}

	status, _ = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+jobA.ID, caseVariant, nil)
	if status != http.StatusNotFound {
		t.Fatalf("case-variant tenant unexpectedly matched: %d", status)
	}

	observerB, _, err := relay.store.CreateTokenWithPolicies("observer", "tenant-b-ui", nil, time.Hour, ProducerLimits{}, ObserverLimits{
		AllowedSubjects: []string{subject}, AllowedTenants: []string{"tenant-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+jobB.ID, observerB, nil)
	if status != http.StatusOK || !strings.Contains(string(body), "tenant b secret") {
		t.Fatalf("scoped observer behavior regressed: %d: %s", status, body)
	}
}

func createTenantVisibilityToken(t *testing.T, relay *Relay, subject string, tenants []string) string {
	t.Helper()
	token, _, err := relay.store.CreateTokenWithLimits("producer", subject, nil, time.Hour, ProducerLimits{AllowedTenants: tenants})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestPipelineChildTenantMustMatchAuthorizedRun(t *testing.T) {
	run := PipelineRun{ID: "run-a", Pipeline: "pipeline-a", OwnerSubject: "shared-producer", TenantID: "tenant-a"}
	child := Job{ID: "job-a", Step: "one", ParentID: run.ID, Pipeline: run.Pipeline, OwnerSubject: run.OwnerSubject, TenantID: run.TenantID}
	if !pipelineChildBelongsToRun(child, run) {
		t.Fatal("matching pipeline child was rejected")
	}
	child.TenantID = "tenant-b"
	if pipelineChildBelongsToRun(child, run) {
		t.Fatal("cross-tenant pipeline child was accepted into activity projection")
	}
}
