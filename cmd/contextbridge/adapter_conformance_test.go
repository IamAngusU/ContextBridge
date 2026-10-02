package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSafeAdapterConformanceID(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "research-online", want: true},
		{value: "adapter.profile_1", want: true},
		{value: "", want: false},
		{value: "contains space", want: false},
		{value: "../escape", want: false},
		{value: strings.Repeat("a", 81), want: false},
	} {
		if got := safeAdapterConformanceID(test.value); got != test.want {
			t.Fatalf("safeAdapterConformanceID(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestReadAdapterConformanceObjectRejectsAmbiguousJSON(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	duplicate := filepath.Join(directory, "duplicate.json")
	if err := os.WriteFile(duplicate, []byte(`{"driver":"one","driver":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdapterConformanceObject(duplicate, 1024); err == nil || !strings.Contains(err.Error(), "duplicate JSON property") {
		t.Fatalf("duplicate property error = %v", err)
	}

	multiple := filepath.Join(directory, "multiple.json")
	if err := os.WriteFile(multiple, []byte(`{} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdapterConformanceObject(multiple, 1024); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestAdapterConformanceEnvironmentReplacesCaseInsensitively(t *testing.T) {
	t.Parallel()
	got := adapterConformanceEnvironment([]string{"Path=old", "KEEP=value", "CONTEXTBRIDGE_URL=old"}, map[string]string{
		"PATH":              "new",
		"CONTEXTBRIDGE_URL": "http://127.0.0.1:1",
	})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "Path=old") || strings.Contains(joined, "CONTEXTBRIDGE_URL=old") {
		t.Fatalf("overridden environment leaked old values: %q", joined)
	}
	for _, expected := range []string{"KEEP=value", "PATH=new", "CONTEXTBRIDGE_URL=http://127.0.0.1:1"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment %q does not contain %q", joined, expected)
		}
	}
}

func TestAdapterConformanceCoreRecordsDeliberateViolations(t *testing.T) {
	t.Parallel()
	options := adapterConformanceOptions{
		profile: "test-profile",
		profileConfig: map[string]interface{}{
			"label": "Test profile", "driver": "test", "options": map[string]interface{}{},
		},
		job: map[string]interface{}{
			"prompt": "safe", "output": map[string]interface{}{
				"mode": "json", "required_keys": []interface{}{"required"}, "max_bytes": 1024,
			},
		},
		timeout: 5 * time.Second,
	}

	malformedProgress, err := newAdapterConformanceCore(options, adapterConformanceLifecycle)
	if err != nil {
		t.Fatal(err)
	}
	malformedProgress.claimed = true
	progressRequest := httptest.NewRequest(http.MethodPost, "/v2/adapter/jobs/"+malformedProgress.jobID+"/progress", strings.NewReader(`{"sequence":0,"text":"bad","busy":true}`))
	progressRequest.Header.Set("Authorization", "Bearer "+malformedProgress.token)
	progressRequest.Header.Set("X-ContextBridge-Lease-Generation", "1")
	progressRequest.Header.Set("X-ContextBridge-Lease-Capability", malformedProgress.leaseCapability)
	progressRecorder := httptest.NewRecorder()
	malformedProgress.ServeHTTP(progressRecorder, progressRequest)
	if progressRecorder.Code != http.StatusUnprocessableEntity || !strings.Contains(malformedProgress.observation().violation, "malformed") {
		t.Fatalf("malformed progress was not rejected with evidence: status=%d observation=%+v", progressRecorder.Code, malformedProgress.observation())
	}

	invalidOutput, err := newAdapterConformanceCore(options, adapterConformanceLifecycle)
	if err != nil {
		t.Fatal(err)
	}
	invalidOutput.claimed = true
	completionRequest := httptest.NewRequest(http.MethodPost, "/v2/adapter/jobs/"+invalidOutput.jobID+"/complete", strings.NewReader(`{"mode":"json","json":{"other":true}}`))
	completionRequest.Header.Set("Authorization", "Bearer "+invalidOutput.token)
	completionRequest.Header.Set("X-ContextBridge-Lease-Generation", "1")
	completionRequest.Header.Set("X-ContextBridge-Lease-Capability", invalidOutput.leaseCapability)
	completionRecorder := httptest.NewRecorder()
	invalidOutput.ServeHTTP(completionRecorder, completionRequest)
	if completionRecorder.Code != http.StatusOK || !strings.Contains(invalidOutput.observation().violation, "normalization") {
		t.Fatalf("invalid output was not recorded as a conformance violation: status=%d observation=%+v", completionRecorder.Code, invalidOutput.observation())
	}
}

func TestRunAdapterConformanceWithIndependentProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXTBRIDGE_CONFORMANCE_TEST_HELPER", "1")
	report, err := runAdapterConformance(adapterConformanceOptions{
		executable: executable,
		arguments:  []string{"-test.run=^TestAdapterConformanceHelperProcess$"},
		profile:    "test-profile",
		profileConfig: map[string]interface{}{
			"label": "Test profile", "driver": "test", "options": map[string]interface{}{},
		},
		job: map[string]interface{}{
			"prompt": "side-effect-free test", "output": map[string]interface{}{"mode": "text", "max_bytes": 1024},
		},
		timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || len(report.Checks) < 7 {
		raw, _ := json.Marshal(report)
		t.Fatalf("unexpected conformance report: %s", raw)
	}
	for _, check := range report.Checks {
		if !check.Passed {
			t.Fatalf("check %s failed: %s", check.ID, check.Detail)
		}
	}
}

func TestAdapterConformanceHelperProcess(t *testing.T) {
	if os.Getenv("CONTEXTBRIDGE_CONFORMANCE_TEST_HELPER") != "1" {
		return
	}
	if err := runAdapterConformanceHelper(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runAdapterConformanceHelper() error {
	baseURL := strings.TrimRight(os.Getenv("CONTEXTBRIDGE_URL"), "/")
	token := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_ADAPTER_TOKEN"))
	profile := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_ADAPTER_PROFILE"))
	if baseURL == "" || token == "" || profile == "" {
		return errors.New("conformance helper environment is incomplete")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(method, path string, body interface{}, headers map[string]string) (int, map[string]interface{}, error) {
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		response, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return response.StatusCode, nil, err
		}
		var payload map[string]interface{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &payload); err != nil {
				return response.StatusCode, nil, err
			}
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return response.StatusCode, payload, fmt.Errorf("HTTP %d", response.StatusCode)
		}
		return response.StatusCode, payload, nil
	}

	if _, _, err := request(http.MethodGet, "/v2/adapter/status", nil, nil); err != nil {
		return err
	}
	if _, _, err := request(http.MethodGet, "/v2/adapter/profiles", nil, nil); err != nil {
		return err
	}
	heartbeat := func(capability, state string) (string, error) {
		endpoint := map[string]interface{}{"id": 1, "profile": profile, "state": state}
		if capability != "" {
			endpoint["endpoint_capability"] = capability
		}
		_, payload, err := request(http.MethodPost, "/v2/adapter/heartbeat", map[string]interface{}{
			"connected": true, "ready": state == "idle", "state": state,
			"adapter": "go-conformance-helper", "adapter_version": "1",
			"active_endpoints": 1, "busy_endpoints": boolToInt(state != "idle"),
			"endpoints": []interface{}{endpoint},
		}, nil)
		if err != nil {
			return "", err
		}
		endpoints, _ := payload["endpoints"].([]interface{})
		if len(endpoints) != 1 {
			return "", errors.New("heartbeat response omitted endpoint")
		}
		entry, _ := endpoints[0].(map[string]interface{})
		value, _ := entry["endpoint_capability"].(string)
		if value == "" {
			return "", errors.New("heartbeat response omitted capability")
		}
		return value, nil
	}
	endpointCapability, err := heartbeat("", "idle")
	if err != nil {
		return err
	}
	_, work, err := request(http.MethodGet, "/v2/adapter/jobs/next?profile="+profile+"&endpoint_id=1&wait=0", nil, map[string]string{
		"X-ContextBridge-Endpoint-Capability": endpointCapability,
	})
	if err != nil {
		return err
	}
	job, _ := work["job"].(map[string]interface{})
	jobID, _ := job["id"].(string)
	leaseCapability, _ := work["lease_capability"].(string)
	if jobID == "" || leaseCapability == "" {
		return errors.New("work response omitted lease evidence")
	}
	leaseHeaders := map[string]string{
		"X-ContextBridge-Lease-Generation": "1",
		"X-ContextBridge-Lease-Capability": leaseCapability,
	}
	jobPath := "/v2/adapter/jobs/" + jobID
	if _, _, err := request(http.MethodPost, jobPath+"/claim", map[string]string{"action": "prepare"}, leaseHeaders); err != nil {
		return err
	}
	if _, _, err := request(http.MethodPost, jobPath+"/progress", map[string]interface{}{
		"sequence": 1, "text": "bounded helper progress", "phase": "generating", "percent": 50, "busy": true,
	}, leaseHeaders); err != nil {
		return err
	}
	endpointCapability, err = heartbeat(endpointCapability, "busy")
	if err != nil {
		return err
	}
	if _, _, err := request(http.MethodPost, jobPath+"/complete", map[string]interface{}{
		"mode": "text", "text": "CONFORMANCE-HELPER-OK", "model": "go-conformance-helper",
	}, leaseHeaders); err != nil {
		return err
	}
	_, err = heartbeat(endpointCapability, "idle")
	return err
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
