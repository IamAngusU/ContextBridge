package bridge

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestScopedLiveActivityValidatedFencedAndCloned(t *testing.T) {
	adapter, other, operator := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("o", 40)
	dir := t.TempDir()
	s, err := NewServer(config.Config{Server: config.Server{Token: operator}, Storage: config.Storage{Directory: dir, Inbox: dir},
		Providers: config.Providers{Adapter: config.AdapterProvider{AuthMode: "scoped", LeaseSeconds: 60,
			Principals: map[string]config.AdapterPrincipal{"a": {Token: adapter, AllowedProfiles: []string{"p"}}, "b": {Token: other, AllowedProfiles: []string{"p"}}}}},
		AdapterProfiles: map[string]config.AdapterProfile{"p": {Driver: "test"}}}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	var status struct {
		Features []string `json:"features"`
	}
	decodeAdapterV2Response(t, adapterV2Request(t, "GET", server.URL+"/v2/adapter/status", adapter, nil, nil), &status)
	if len(status.Features) != 1 || status.Features[0] != "resource_activity_progress_v1" {
		t.Fatal("progress feature not discoverable")
	}
	capability := heartbeatAdapterV2(t, server.URL, adapter, "p", 1)
	s.store.Queue(Job{ID: "live-job", Output: OutputSpec{Activity: true}}, map[string]interface{}{"name": "p"}, time.Minute)
	var lease adapterJob
	decodeAdapterV2Response(t, adapterV2Request(t, "GET", server.URL+"/v2/adapter/jobs/next?wait=0&profile=p&endpoint_id=1", adapter, nil,
		map[string]string{"X-ContextBridge-Endpoint-Capability": capability}), &lease)
	headers := map[string]string{"Content-Type": "application/json", "X-ContextBridge-Lease-Generation": "1", "X-ContextBridge-Lease-Capability": lease.LeaseCapability}
	endpoint := server.URL + "/v2/adapter/jobs/live-job/progress"
	manifest := json.RawMessage(`{"schema":"contextbridge.resource-activity.v1","items":[{"id":"a1","kind":"file","action":"read","label":"README.md"}]}`)
	post := func(token string, progress AdapterProgress) int {
		raw, _ := json.Marshal(progress)
		resp := adapterV2Request(t, "POST", endpoint, token, raw, headers)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(adapter, AdapterProgress{Sequence: 1, Activity: manifest}); code != 200 {
		t.Fatalf("progress = %d", code)
	}
	if code := post(other, AdapterProgress{Sequence: 2, Activity: manifest}); code != 409 {
		t.Fatalf("foreign principal = %d", code)
	}
	headers["X-ContextBridge-Lease-Generation"] = "2"
	if code := post(adapter, AdapterProgress{Sequence: 2, Activity: manifest}); code != 409 {
		t.Fatalf("stale generation = %d", code)
	}
	headers["X-ContextBridge-Lease-Generation"] = "1"
	if code := post(adapter, AdapterProgress{Sequence: 2}); code != 200 {
		t.Fatal(code)
	}
	if code := post(adapter, AdapterProgress{Sequence: 1, Activity: json.RawMessage(`{}`)}); code != 200 {
		t.Fatal(code)
	}
	progress, _, ready := s.store.AdapterProgressScoped("live-job", 1, "a", lease.LeaseCapability)
	if !ready || progress.Sequence != 2 || progress.ActivityStatus != "reported" {
		t.Fatalf("lost snapshot: %+v", progress)
	}
	progress.Activity[0] = 'x'
	fresh, _, _ := s.store.AdapterProgressScoped("live-job", 1, "a", lease.LeaseCapability)
	if fresh.Activity[0] != '{' {
		t.Fatal("getter aliases store bytes")
	}
	if code := post(adapter, AdapterProgress{Sequence: 3, Activity: json.RawMessage(`{"schema":"bad"}`)}); code != 200 {
		t.Fatal("bad optional metadata failed operation")
	}
	var normalized AdapterProgress
	decodeAdapterV2Response(t, adapterV2Request(t, "GET", server.URL+"/v1/operator/adapter/jobs/live-job/progress", operator, nil, nil), &normalized)
	if normalized.ActivityStatus != "invalid" || len(normalized.Activity) != 0 {
		t.Fatal("invalid evidence retained")
	}
	if resp := adapterV2Request(t, http.MethodGet, endpoint, other, nil, headers); resp.StatusCode != 404 {
		resp.Body.Close()
		t.Fatalf("foreign progress read = %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
}

func TestLiveProgressMetadataOptInAndInvalidPropagation(t *testing.T) {
	raw := json.RawMessage(`{"schema":"contextbridge.resource-activity.v1","items":[]}`)
	previous := &AdapterProgress{Activity: raw, ActivityStatus: "reported"}
	for _, enabled := range []bool{false, true} {
		got := normalizeProgressActivity(AdapterProgress{Sequence: 2, Activity: raw}, previous, enabled)
		if (len(got.Activity) > 0) != enabled {
			t.Fatal("opt-in not respected")
		}
	}
	invalid := normalizeProgressActivity(AdapterProgress{ActivityStatus: "invalid"}, previous, true)
	if len(invalid.Activity) != 0 || invalid.ActivityStatus != "invalid" {
		t.Fatal("invalid status replaced by stale evidence")
	}
}
