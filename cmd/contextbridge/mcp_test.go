package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestMCPStdioLifecycleAndBoundedTools(t *testing.T) {
	const token = "mcp-test-token"
	var requests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("missing local bearer token: %q", request.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/status":
			_, _ = w.Write([]byte(`{"ok":true,"service":"contextbridge","queued":0}`))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/jobs":
			if request.URL.Query().Get("compact") != "1" {
				t.Error("MCP submission did not request a compact response")
			}
			var job bridge.Job
			if err := json.NewDecoder(request.Body).Decode(&job); err != nil {
				t.Errorf("decode submitted job: %v", err)
			}
			if job.Source != mcpSource || job.Prompt != "Reply exactly MCP-OK" {
				t.Errorf("unexpected submitted job: %#v", job)
			}
			_, _ = w.Write([]byte(`{"job":{"id":"job-mcp-1"},"status":"completed","output":{"mode":"text","text":"MCP-OK"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/jobs/job-mcp-1":
			_, _ = w.Write([]byte(`{"mode":"text","text":"MCP-OK"}`))
		default:
			http.Error(w, `{"error":"unexpected test endpoint"}`, http.StatusNotFound)
		}
	}))
	defer local.Close()

	server := newTestMCPServer(local.URL, token)
	responses := runMCPTranscript(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"contextbridge.status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"contextbridge.submit","arguments":{"job":{"route":"default","prompt":"Reply exactly MCP-OK","output":{"mode":"text","max_bytes":1024}}}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"contextbridge.result","arguments":{"job_id":"job-mcp-1"}}}`,
	)
	if len(responses) != 5 {
		t.Fatalf("expected five request responses, got %d: %#v", len(responses), responses)
	}
	initialize := responseResult(t, responses[0])
	if initialize["protocolVersion"] != "2025-11-25" {
		t.Fatalf("unexpected negotiated version: %#v", initialize)
	}
	capabilities := initialize["capabilities"].(map[string]interface{})
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("server did not declare tools: %#v", capabilities)
	}
	if _, ok := capabilities["tasks"]; ok {
		t.Fatalf("server advertised unsupported MCP tasks: %#v", capabilities)
	}
	listed := responseResult(t, responses[1])["tools"].([]interface{})
	if len(listed) != 4 {
		t.Fatalf("expected exactly four bounded tools, got %#v", listed)
	}
	for index, expected := range []string{"contextbridge.status", "contextbridge.cluster_contract_validate", "contextbridge.submit", "contextbridge.result"} {
		tool := listed[index].(map[string]interface{})
		if tool["name"] != expected {
			t.Fatalf("tool %d = %#v, want %s", index, tool, expected)
		}
	}
	if got := toolStructured(t, responses[2])["service"]; got != "contextbridge" {
		t.Fatalf("unexpected status result: %#v", got)
	}
	if got := toolStructured(t, responses[3])["status"]; got != "completed" {
		t.Fatalf("unexpected submit result: %#v", got)
	}
	if got := toolStructured(t, responses[4])["text"]; got != "MCP-OK" {
		t.Fatalf("unexpected saved result: %#v", got)
	}
	if requests.Load() != 3 {
		t.Fatalf("expected three local requests, got %d", requests.Load())
	}
}

func TestMCPProtocolVersionNegotiation(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		want      string
	}{
		{name: "supported legacy version is echoed", requested: "2024-11-05", want: "2024-11-05"},
		{name: "unsupported version falls back to latest", requested: "2099-01-01", want: mcpLatestProtocolVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestMCPServer("http://127.0.0.1:1", "token")
			initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + test.requested + `","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
			responses := runMCPTranscript(t, server,
				initialize,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
			)
			if len(responses) != 2 {
				t.Fatalf("expected initialize and tools/list responses, got %d: %#v", len(responses), responses)
			}
			if got := responseResult(t, responses[0])["protocolVersion"]; got != test.want {
				t.Fatalf("negotiated protocol version = %#v, want %q", got, test.want)
			}
			if tools, ok := responseResult(t, responses[1])["tools"].([]interface{}); !ok || len(tools) != 4 {
				t.Fatalf("server was not usable after negotiation: %#v", responses[1])
			}
		})
	}
}

func TestMCPSubmitSchemaDescribesSafeNativeJobFields(t *testing.T) {
	tools := mcpTools()
	if len(tools) != 4 {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	input := tools[2]["inputSchema"].(map[string]interface{})
	job := input["properties"].(map[string]interface{})["job"].(map[string]interface{})
	properties := job["properties"].(map[string]interface{})
	for _, name := range []string{"prompt", "text", "session_id", "adapter_profile", "model", "reasoning", "image_base64", "output"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("submit schema omitted %s: %#v", name, properties)
		}
	}
	output := properties["output"].(map[string]interface{})["properties"].(map[string]interface{})
	if _, ok := output["artifacts"]; ok {
		t.Fatalf("artifact output escaped into bounded MCP schema: %#v", output)
	}
	if job["additionalProperties"] != false {
		t.Fatalf("native job schema is not closed: %#v", job)
	}
	metadata := properties["metadata"].(map[string]interface{})
	if metadata["additionalProperties"] != false {
		t.Fatalf("native job metadata schema is not closed: %#v", metadata)
	}
	metadataProperties := metadata["properties"].(map[string]interface{})
	if _, ok := metadataProperties["contextbridge_input_file"]; ok {
		t.Fatalf("filesystem-bearing metadata escaped into bounded MCP schema: %#v", metadataProperties)
	}
	if _, ok := metadataProperties["contextbridge_new_session"]; !ok {
		t.Fatalf("safe fresh-session metadata is missing from bounded MCP schema: %#v", metadataProperties)
	}
	contractInput := tools[1]["inputSchema"].(map[string]interface{})
	contractJob := contractInput["properties"].(map[string]interface{})["job"].(map[string]interface{})
	contractProperties := contractJob["properties"].(map[string]interface{})
	if _, ok := contractProperties["assignment_secret"]; ok {
		t.Fatalf("one-time reservation secret escaped into read-only MCP schema: %#v", contractProperties)
	}
	if _, ok := contractProperties["sealed_payload"]; ok {
		t.Fatalf("unverifiable sealed payload escaped into read-only MCP schema: %#v", contractProperties)
	}
}

func TestMCPClusterContractValidationIsReadOnlyAndAuthenticated(t *testing.T) {
	var validations atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/cluster/contracts/validate" || request.Header.Get("Authorization") != "Bearer producer-token" {
			http.Error(w, `{"error":"unexpected request"}`, http.StatusForbidden)
			return
		}
		var job map[string]interface{}
		if err := json.NewDecoder(request.Body).Decode(&job); err != nil {
			http.Error(w, `{"error":"bad job"}`, http.StatusBadRequest)
			return
		}
		validations.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"contract_version":"contextbridge.job.v1","valid":true,"payload_mode":"cleartext","max_attempts":3}`))
	}))
	defer relay.Close()
	server := newTestMCPServer("http://127.0.0.1:1", "local-token")
	server.config.Cluster.Worker.RelayURL = relay.URL
	server.config.Cluster.ClientToken = "producer-token"
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.cluster_contract_validate","arguments":{"job":{"requirements":{"task":"generation"},"payload":{"prompt":"safe"}}}}}`,
	)
	if validations.Load() != 1 {
		t.Fatalf("contract validation requests = %d", validations.Load())
	}
	structured := toolStructured(t, responses[1])
	if structured["valid"] != true || structured["contract_version"] != "contextbridge.job.v1" {
		t.Fatalf("unexpected MCP contract result: %#v", structured)
	}
}

func TestMCPClusterContractValidationRejectsReservationCredentialsLocally(t *testing.T) {
	server := newTestMCPServer("http://127.0.0.1:1", "local-token")
	server.config.Cluster.Worker.RelayURL = "http://127.0.0.1:2"
	server.config.Cluster.ClientToken = "producer-token"
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.cluster_contract_validate","arguments":{"job":{"requirements":{"task":"generation"},"sealed_payload":{"algorithm":"x"},"assignment_secret":"must-not-leave"}}}}`,
	)
	result := responseResult(t, responses[1])
	if result["isError"] != true {
		t.Fatalf("reservation secret was not rejected: %#v", result)
	}
	content := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(content, "atomic native submission") {
		t.Fatalf("reservation rejection was not explicit: %q", content)
	}
}

func TestMCPRejectsFilesystemAndRecoveryMetadataBeforeLocalSubmit(t *testing.T) {
	for _, field := range []string{"contextbridge_input_file", "contextbridge_resume_only"} {
		t.Run(field, func(t *testing.T) {
			var requests atomic.Int32
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer local.Close()
			server := newTestMCPServer(local.URL, "token")
			request := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.submit","arguments":{"job":{"prompt":"bounded","metadata":{"` + field + `":true},"output":{"mode":"text"}}}}}`
			responses := runMCPTranscript(t, server,
				initializeMCPTestMessage(),
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				request,
			)
			if requests.Load() != 0 {
				t.Fatalf("forbidden metadata reached the local service %d times", requests.Load())
			}
			result := responseResult(t, responses[1])
			if result["isError"] != true {
				t.Fatalf("forbidden metadata was not rejected: %#v", result)
			}
			content := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
			if !strings.Contains(content, field) {
				t.Fatalf("metadata rejection is not explicit: %q", content)
			}
		})
	}
}

func TestMCPRejectsLocalResponseOverTwoMiBWithoutTruncation(t *testing.T) {
	var requests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"payload":"` + strings.Repeat("x", mcpMaximumToolBytes) + `"}`))
	}))
	defer local.Close()

	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.status","arguments":{}}}`,
	)
	if requests.Load() != 1 {
		t.Fatalf("expected one bounded local read, got %d", requests.Load())
	}
	result := responseResult(t, responses[1])
	if result["isError"] != true {
		t.Fatalf("oversized response was not rejected as a tool error: %#v", result)
	}
	content := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(content, "exceeds the MCP stdio limit") || !strings.Contains(content, "2097152") {
		t.Fatalf("oversized response error is not explicit: %q", content)
	}
	if strings.Contains(content, strings.Repeat("x", 128)) {
		t.Fatal("oversized response content leaked into the bounded MCP error")
	}
}

func TestMCPStatusRedactsEndpointCredentialsAndURLs(t *testing.T) {
	const secret = "mcp-status-secret"
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"contextbridge","providers":{"ollama":{"url":"https://user:` + secret + `@example.invalid/api?token=` + secret + `#fragment"}},"runtime":{"engines":{"managed":{"control_url":"https://example.invalid/` + secret + `","state":"online"}}},"adapter":{"endpoints":[{"endpoint_id":42,"session_key":"cb:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]},"pairing_token":"` + secret + `"}`))
	}))
	defer local.Close()

	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.status","arguments":{}}}`,
	)
	result := responseResult(t, responses[1])
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte("user:")) || bytes.Contains(encoded, []byte("example.invalid")) {
		t.Fatalf("credential-bearing status URL escaped through MCP: %s", encoded)
	}
	structured := result["structuredContent"].(map[string]interface{})
	if structured["service"] != "contextbridge" {
		t.Fatalf("safe status fields were lost: %#v", structured)
	}
	provider := structured["providers"].(map[string]interface{})["ollama"].(map[string]interface{})
	if _, exists := provider["url"]; exists {
		t.Fatalf("status URL survived MCP scrubbing: %#v", provider)
	}
	endpoint := structured["adapter"].(map[string]interface{})["endpoints"].([]interface{})[0].(map[string]interface{})
	if _, exists := endpoint["session_key"]; exists {
		t.Fatalf("opaque adapter session key survived MCP scrubbing: %#v", endpoint)
	}
}

func TestMCPRejectsInputFrameOverSixteenMiB(t *testing.T) {
	server := newTestMCPServer("http://127.0.0.1:1", "token")
	input := append(bytes.Repeat([]byte("x"), mcpMaximumMessageBytes+1), '\n')
	var output bytes.Buffer
	err := server.serve(context.Background(), bytes.NewReader(input), &output)
	if err == nil || !strings.Contains(err.Error(), "read MCP stdio message") {
		t.Fatalf("oversized input did not fail at the stdio boundary: %v", err)
	}
	var response map[string]interface{}
	if decodeErr := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); decodeErr != nil {
		t.Fatalf("oversized input did not return valid JSON-RPC error: %v; output=%q", decodeErr, output.String())
	}
	errorValue, ok := response["error"].(map[string]interface{})
	if !ok || errorValue["code"] != float64(-32700) {
		t.Fatalf("oversized input response is not a parse error: %#v", response)
	}
	data, ok := errorValue["data"].(map[string]interface{})
	if !ok || data["maximum_message_bytes"] != float64(mcpMaximumMessageBytes) {
		t.Fatalf("oversized input response omitted the frame limit: %#v", response)
	}
}

func TestMCPSubmitFailureIsReturnedOnceWithoutRetry(t *testing.T) {
	var submits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		submits.Add(1)
		http.Error(w, `{"error":"capacity unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer local.Close()
	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.submit","arguments":{"job":{"route":"default","prompt":"one attempt","output":{"mode":"text"}}}}}`,
	)
	if submits.Load() != 1 {
		t.Fatalf("state-changing MCP submission was attempted %d times", submits.Load())
	}
	result := responseResult(t, responses[1])
	if result["isError"] != true {
		t.Fatalf("execution failure was not an MCP tool error: %#v", result)
	}
	content := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(content, "503") || !strings.Contains(content, "capacity unavailable") {
		t.Fatalf("tool error is not actionable: %q", content)
	}
}

func TestMCPMarksNormalizedProviderFailureAsToolError(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job":{"id":"job-failed"},"status":"completed","output":{"mode":"text","provider":"contextbridge","error":"adapter_lease_lost"}}`))
	}))
	defer local.Close()
	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.submit","arguments":{"job":{"prompt":"one attempt","output":{"mode":"text"}}}}}`,
	)
	result := responseResult(t, responses[1])
	if result["isError"] != true {
		t.Fatalf("normalized provider failure was presented as success: %#v", result)
	}
	structured := result["structuredContent"].(map[string]interface{})
	if structured["output"].(map[string]interface{})["error"] != "adapter_lease_lost" {
		t.Fatalf("provider failure evidence was lost: %#v", structured)
	}
}

func TestMCPRejectsArtifactSubmissionBeforeProviderAction(t *testing.T) {
	var requests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer local.Close()
	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.submit","arguments":{"job":{"prompt":"create a file","output":{"mode":"text","artifacts":true,"min_artifacts":1}}}}}`,
	)
	if requests.Load() != 0 {
		t.Fatalf("artifact job reached the provider-facing service %d times", requests.Load())
	}
	result := responseResult(t, responses[1])
	if result["isError"] != true {
		t.Fatalf("artifact rejection was not a tool execution error: %#v", result)
	}
}

func TestMCPRejectsInternalAdapterEndpointRoutingBeforeLocalSubmit(t *testing.T) {
	var requests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer local.Close()
	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.submit","arguments":{"job":{"prompt":"steer internally","contextbridge_adapter_endpoint_id":42,"output":{"mode":"text"}}}}}`,
	)
	if requests.Load() != 0 {
		t.Fatalf("internal adapter endpoint field reached the local service %d times", requests.Load())
	}
	result := responseResult(t, responses[1])
	if result["isError"] != true {
		t.Fatalf("internal routing field was not rejected: %#v", result)
	}
	content := result["content"].([]interface{})[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(content, "contextbridge_adapter_endpoint_id") {
		t.Fatalf("internal routing rejection is not explicit: %q", content)
	}
}

func TestMCPRejectsStoredArtifactResultWithoutExposingBytes(t *testing.T) {
	const secretArtifact = "c2Vuc2l0aXZlLWFydGlmYWN0LWJ5dGVz"
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/jobs/job-artifact-result" {
			http.Error(w, `{"error":"unexpected test endpoint"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"text","text":"created","artifacts":[{"name":"secret.png","media_type":"image/png","data_base64":"` + secretArtifact + `"}]}`))
	}))
	defer local.Close()

	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.result","arguments":{"job_id":"job-artifact-result"}}}`,
	)
	result := responseResult(t, responses[1])
	if result["isError"] != true {
		t.Fatalf("stored artifact result was not rejected: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secretArtifact)) || bytes.Contains(encoded, []byte("secret.png")) {
		t.Fatalf("stored artifact bytes or metadata escaped through MCP: %s", encoded)
	}
}

func TestMCPRequiresLifecycleAndNeverExecutesToolNotifications(t *testing.T) {
	var requests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer local.Close()
	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"contextbridge.status","arguments":{}}}`,
	)
	if len(responses) != 2 {
		t.Fatalf("notifications produced responses: %#v", responses)
	}
	errorValue := responses[1]["error"].(map[string]interface{})
	if errorValue["code"] != float64(-32002) {
		t.Fatalf("tools were available before initialized notification: %#v", errorValue)
	}
	if requests.Load() != 0 {
		t.Fatalf("tool notification caused %d side effects", requests.Load())
	}
}

func TestMCPNotificationsCancelledDoesNotPretendToCancelProviderWork(t *testing.T) {
	var requests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/v1/status" {
			t.Errorf("cancellation notification reached local service as %s %s", request.Method, request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer local.Close()

	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":77,"reason":"client stopped waiting"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.status","arguments":{}}}`,
	)
	if len(responses) != 2 {
		t.Fatalf("notifications/cancelled produced a response: %#v", responses)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected only the subsequent status read, got %d local requests", requests.Load())
	}
	if toolStructured(t, responses[1])["ok"] != true {
		t.Fatalf("server was not usable after cancellation notification: %#v", responses[1])
	}
}

func TestMCPResultValidatesJobIDBeforeLocalRequest(t *testing.T) {
	var requests atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
	}))
	defer local.Close()
	server := newTestMCPServer(local.URL, "token")
	responses := runMCPTranscript(t, server,
		initializeMCPTestMessage(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.result","arguments":{"job_id":"../secret"}}}`,
	)
	if requests.Load() != 0 {
		t.Fatalf("invalid result ID reached local service %d times", requests.Load())
	}
	if responseResult(t, responses[1])["isError"] != true {
		t.Fatalf("invalid ID was not returned as a tool error: %#v", responses[1])
	}
}

func newTestMCPServer(localURL, token string) *mcpStdioServer {
	return &mcpStdioServer{
		baseURL: localURL,
		token:   token,
		config: config.Config{Routes: map[string]config.Route{
			"default": {Provider: "adapter", TimeoutSeconds: 5},
		}},
		client: &http.Client{},
	}
}

func initializeMCPTestMessage() string {
	return `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
}

func runMCPTranscript(t *testing.T, server *mcpStdioServer, messages ...string) []map[string]interface{} {
	t.Helper()
	var output bytes.Buffer
	input := strings.NewReader(strings.Join(messages, "\n") + "\n")
	if err := server.serve(context.Background(), input, &output); err != nil {
		t.Fatalf("serve MCP transcript: %v", err)
	}
	var responses []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]interface{}
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid MCP response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func responseResult(t *testing.T, response map[string]interface{}) map[string]interface{} {
	t.Helper()
	result, ok := response["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("response has no object result: %#v", response)
	}
	return result
}

func toolStructured(t *testing.T, response map[string]interface{}) map[string]interface{} {
	t.Helper()
	structured, ok := responseResult(t, response)["structuredContent"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool response has no structured content: %#v", response)
	}
	return structured
}
