package bridge

import (
	"bytes"
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

func TestAdapterV2SeparatesOperatorScopeAndBindsLeaseCapability(t *testing.T) {
	operatorToken := strings.Repeat("o", 40)
	adapterAToken := strings.Repeat("a", 40)
	adapterBToken := strings.Repeat("b", 40)
	directory := t.TempDir()
	cfg := config.Config{
		Server:  config.Server{Token: operatorToken},
		Storage: config.Storage{Directory: directory, Inbox: directory},
		Providers: config.Providers{Adapter: config.AdapterProvider{
			LeaseSeconds: 60, AuthMode: "scoped",
			Principals: map[string]config.AdapterPrincipal{
				"adapter-a": {Token: adapterAToken, AllowedProfiles: []string{"profile-a"}},
				"adapter-b": {Token: adapterBToken, AllowedProfiles: []string{"profile-b"}},
			},
		}},
		AdapterProfiles: map[string]config.AdapterProfile{
			"profile-a": {Label: "A", Driver: "test"},
			"profile-b": {Label: "B", Driver: "test"},
		},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	if response := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v1/status", adapterAToken, nil, nil); response.StatusCode != http.StatusUnauthorized {
		response.Body.Close()
		t.Fatalf("adapter credential reached operator status: %s", response.Status)
	} else {
		response.Body.Close()
	}
	if response := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v2/adapter/status", operatorToken, nil, nil); response.StatusCode != http.StatusUnauthorized {
		response.Body.Close()
		t.Fatalf("operator credential reached v2 adapter status: %s", response.Status)
	} else {
		response.Body.Close()
	}
	if response := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v1/adapter/profiles", operatorToken, nil, nil); response.StatusCode != http.StatusGone {
		response.Body.Close()
		t.Fatalf("scoped mode left legacy adapter protocol enabled: %s", response.Status)
	} else {
		response.Body.Close()
	}

	profilesResponse := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v2/adapter/profiles", adapterAToken, nil, nil)
	var profiles map[string]config.AdapterProfile
	decodeAdapterV2Response(t, profilesResponse, &profiles)
	if len(profiles) != 1 || profiles["profile-a"].Label != "A" {
		t.Fatalf("adapter received profiles outside its scope: %#v", profiles)
	}

	endpointCapability := heartbeatAdapterV2(t, httpServer.URL, adapterAToken, "profile-a", 7)
	if renewed := heartbeatAdapterV2WithCapability(t, httpServer.URL, adapterAToken, "profile-a", 7, endpointCapability); renewed != endpointCapability {
		t.Fatalf("healthy heartbeat rotated endpoint capability: old=%q new=%q", endpointCapability, renewed)
	}
	if response := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v2/adapter/jobs/next?wait=0&profile=profile-b&endpoint_id=7", adapterAToken, nil,
		map[string]string{"X-ContextBridge-Endpoint-Capability": endpointCapability}); response.StatusCode != http.StatusForbidden {
		response.Body.Close()
		t.Fatalf("adapter polled outside its profile scope: %s", response.Status)
	} else {
		response.Body.Close()
	}

	done := server.store.Queue(Job{ID: "v2-bound-job", Prompt: "say OK", Output: OutputSpec{Mode: "text"}}, map[string]interface{}{"name": "profile-a"}, time.Minute)
	poll := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v2/adapter/jobs/next?wait=0&profile=profile-a&endpoint_id=7", adapterAToken, nil,
		map[string]string{"X-ContextBridge-Endpoint-Capability": endpointCapability})
	var work adapterJob
	decodeAdapterV2Response(t, poll, &work)
	if work.Job.ID != "v2-bound-job" || work.LeaseGeneration == 0 || len(work.LeaseCapability) < 40 {
		t.Fatalf("v2 lease omitted its opaque capability: %#v", work)
	}
	progressBody := []byte(`{"sequence":1,"text":"working","busy":true}`)
	progressHeaders := map[string]string{
		"Content-Type": "application/json", "X-ContextBridge-Lease-Generation": "1",
		"X-ContextBridge-Lease-Capability": work.LeaseCapability,
	}
	progressURL := httpServer.URL + "/v2/adapter/jobs/v2-bound-job/progress"
	if response := adapterV2Request(t, http.MethodPost, progressURL, adapterAToken, progressBody, progressHeaders); response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("scoped progress update failed: %s %s", response.Status, raw)
	} else {
		response.Body.Close()
	}
	operatorProgress := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v1/operator/adapter/jobs/v2-bound-job/progress", operatorToken, nil, nil)
	var progress AdapterProgress
	decodeAdapterV2Response(t, operatorProgress, &progress)
	if progress.Sequence != 1 || progress.Text != "working" {
		t.Fatalf("operator progress endpoint lost scoped progress: %#v", progress)
	}
	if response := adapterV2Request(t, http.MethodGet, httpServer.URL+"/v1/operator/adapter/jobs/v2-bound-job/progress", adapterAToken, nil, nil); response.StatusCode != http.StatusUnauthorized {
		response.Body.Close()
		t.Fatalf("adapter credential reached operator progress endpoint: %s", response.Status)
	} else {
		response.Body.Close()
	}

	actionURL := httpServer.URL + "/v2/adapter/jobs/v2-bound-job/complete"
	body := []byte(`{"mode":"text","text":"OK"}`)
	headers := map[string]string{
		"Content-Type": "application/json", "X-ContextBridge-Lease-Generation": "1",
		"X-ContextBridge-Lease-Capability": work.LeaseCapability,
	}
	if response := adapterV2Request(t, http.MethodPost, actionURL, adapterBToken, body, headers); response.StatusCode != http.StatusConflict {
		response.Body.Close()
		t.Fatalf("different adapter principal completed another lease: %s", response.Status)
	} else {
		response.Body.Close()
	}
	wrongHeaders := map[string]string{
		"Content-Type": "application/json", "X-ContextBridge-Lease-Generation": "1",
		"X-ContextBridge-Lease-Capability": strings.Repeat("x", len(work.LeaseCapability)),
	}
	if response := adapterV2Request(t, http.MethodPost, actionURL, adapterAToken, body, wrongHeaders); response.StatusCode != http.StatusConflict {
		response.Body.Close()
		t.Fatalf("wrong lease capability completed work: %s", response.Status)
	} else {
		response.Body.Close()
	}
	response := adapterV2Request(t, http.MethodPost, actionURL, adapterAToken, body, headers)
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("valid scoped completion failed: %s %s", response.Status, raw)
	}
	response.Body.Close()
	select {
	case output := <-done:
		if output.Text != "OK" {
			t.Fatalf("unexpected completed output: %#v", output)
		}
	case <-time.After(time.Second):
		t.Fatal("scoped completion did not release the waiting job")
	}
}

func heartbeatAdapterV2(t *testing.T, baseURL, token, profile string, endpointID int) string {
	return heartbeatAdapterV2WithCapability(t, baseURL, token, profile, endpointID, "")
}

func heartbeatAdapterV2WithCapability(t *testing.T, baseURL, token, profile string, endpointID int, capability string) string {
	t.Helper()
	body, err := json.Marshal(AdapterClientStatus{
		State: "waiting", Ready: true, ActiveEndpoints: 1,
		Endpoints: []AdapterEndpointStatus{{ID: endpointID, Profile: profile, State: "idle", EndpointCapability: capability}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := adapterV2Request(t, http.MethodPost, baseURL+"/v2/adapter/heartbeat", token, body, map[string]string{"Content-Type": "application/json"})
	var envelope struct {
		Endpoints []AdapterEndpointCapability `json:"endpoints"`
	}
	decodeAdapterV2Response(t, response, &envelope)
	if len(envelope.Endpoints) != 1 || envelope.Endpoints[0].EndpointCapability == "" {
		t.Fatalf("heartbeat did not issue an endpoint capability: %#v", envelope)
	}
	return envelope.Endpoints[0].EndpointCapability
}

func adapterV2Request(t *testing.T, method, url, token string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeAdapterV2Response(t *testing.T, response *http.Response, target interface{}) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected adapter v2 response: %s %s", response.Status, raw)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
