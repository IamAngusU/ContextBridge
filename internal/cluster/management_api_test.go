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

func TestScopedObserverIdentityAndJobHistory(t *testing.T) {
	const admin = "admin_management_api_012345678901234567890123"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	ownedA, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "app-a", TenantID: "tenant-a", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	ownedB, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "app-b", TenantID: "tenant-a", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	ownedC, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "app-a", TenantID: "tenant-b", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	caseVariantOwner, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "App-A", TenantID: "tenant-a", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	caseVariantTenant, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "app-a", TenantID: "Tenant-A", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	observer, record, err := relay.store.CreateTokenWithPolicies("observer", "ui-a", nil, time.Hour, ProducerLimits{}, ObserverLimits{
		AllowedSubjects: []string{"app-a"}, AllowedTenants: []string{"tenant-a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	status, raw := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/whoami", observer, nil)
	if status != http.StatusOK || strings.Contains(string(raw), observer) || !strings.Contains(string(raw), record.ID) || !strings.Contains(string(raw), "tenant-a") {
		t.Fatalf("unsafe whoami response: status=%d body=%s", status, raw)
	}
	status, raw = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs", observer, nil)
	if status != http.StatusOK || !strings.Contains(string(raw), ownedA.ID) || strings.Contains(string(raw), ownedB.ID) || strings.Contains(string(raw), ownedC.ID) || strings.Contains(string(raw), caseVariantOwner.ID) || strings.Contains(string(raw), caseVariantTenant.ID) {
		t.Fatalf("legacy history ignored scope: status=%d body=%s", status, raw)
	}
	for _, id := range []string{ownedB.ID, ownedC.ID, caseVariantOwner.ID, caseVariantTenant.ID} {
		status, _ = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+id, observer, nil)
		if status != http.StatusNotFound {
			t.Fatalf("foreign job %s status = %d", id, status)
		}
	}
	status, raw = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/events", observer, nil)
	if status != http.StatusForbidden || !strings.Contains(string(raw), "scope.global_events_forbidden") {
		t.Fatalf("scoped global events: status=%d body=%s", status, raw)
	}
}

func TestCursorJobHistoryIsStableAndFilterBound(t *testing.T) {
	const admin = "admin_cursor_api_012345678901234567890123456"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	for _, id := range []string{"page-one", "page-two", "page-three"} {
		if _, err := relay.store.CreateJob(SubmitRequest{ID: id, OwnerSubject: "app-a", TenantID: "tenant-a", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	status, raw := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs?page=1&limit=1&owner_subject=app-a", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("first page: status=%d body=%s", status, raw)
	}
	var first jobHistoryPage
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 1 || !first.HasMore || first.NextCursor == "" || first.Jobs[0].ID != "page-three" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	status, raw = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs?page=1&limit=1&owner_subject=app-a&cursor="+first.NextCursor, admin, nil)
	var second jobHistoryPage
	if status != http.StatusOK || json.Unmarshal(raw, &second) != nil || len(second.Jobs) != 1 || second.Jobs[0].ID != "page-two" {
		t.Fatalf("unexpected second page: status=%d body=%s", status, raw)
	}
	status, raw = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs?page=1&limit=1&tenant_id=tenant-a&cursor="+first.NextCursor, admin, nil)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "query.invalid_cursor") {
		t.Fatalf("cursor filter change was accepted: status=%d body=%s", status, raw)
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/cluster/openapi.json", nil)
	request.Header.Set("Authorization", "Bearer "+admin)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var specification map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&specification); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || specification["openapi"] != "3.1.0" || response.Header.Get("X-Request-ID") == "" {
		t.Fatalf("invalid OpenAPI response: status=%d request_id=%q spec=%#v", response.StatusCode, response.Header.Get("X-Request-ID"), specification)
	}
	encodedSpecification, err := json.Marshal(specification)
	if err != nil || !strings.Contains(string(encodedSpecification), `"max_priority"`) {
		t.Fatalf("OpenAPI omits the producer priority ceiling: %s err=%v", encodedSpecification, err)
	}
	paths, ok := specification["paths"].(map[string]interface{})
	if !ok {
		t.Fatalf("OpenAPI paths are missing: %#v", specification)
	}
	tokens, ok := paths["/v1/cluster/tokens"].(map[string]interface{})
	if !ok {
		t.Fatalf("token path is missing: %#v", paths)
	}
	post, ok := tokens["post"].(map[string]interface{})
	if !ok || post["requestBody"] == nil {
		t.Fatalf("token creation request schema is missing: %#v", tokens)
	}
	responses, ok := post["responses"].(map[string]interface{})
	if !ok || responses["201"] == nil || responses["200"] != nil {
		t.Fatalf("token creation status contract is inaccurate: %#v", post)
	}
}
