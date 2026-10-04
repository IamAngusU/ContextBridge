package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestAgentAutoSubmitsOnlyLocalOllamaJobs(t *testing.T) {
	const token = "agent_auto_test_token_0123456789"
	var lock sync.Mutex
	requests := []cluster.SubmitRequest{}

	resultFor := func(id string) json.RawMessage {
		output := &bridge.Output{Mode: "text", Text: "AUTO-LOCAL-OK", Model: "qwen-test"}
		if id == "planner" {
			output = &bridge.Output{
				Mode:  "json",
				Model: "qwen-test",
				JSON:  json.RawMessage(`{"version":1,"summary":"Answer locally.","steps":[{"id":"answer","provider":"ollama","profile":"","instruction":"Reply exactly AUTO-LOCAL-OK.","use_previous":false}]}`),
			}
		}
		raw, err := json.Marshal(bridge.Submission{Status: "completed", Output: output})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/cluster/jobs":
			var input cluster.SubmitRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			lock.Lock()
			requests = append(requests, input)
			id := "step"
			if len(requests) == 1 {
				id = "planner"
			}
			lock.Unlock()
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: id, Status: cluster.JobQueued})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/planner":
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "planner", Status: cluster.JobCompleted, AssignedNode: "node-local", Result: resultFor("planner")})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/step":
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "step", Status: cluster.JobCompleted, AssignedNode: "node-local", Result: resultFor("step")})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/cluster/routes/explain":
			var input cluster.AssignmentRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			if input.Requirements.Egress != "local_only" || input.Requirements.Provider != "ollama" {
				http.Error(writer, "unsafe route preview", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(writer).Encode(cluster.RoutingDecision{Preview: true, SelectedNodeID: "node-local", SelectedNodeName: "Local test node"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = server.URL
	cfg.AdapterProfiles["profile-two"] = config.AdapterProfile{Label: "Remote B", Driver: "test"}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := clusterAgentAutoCommand([]string{"--config", configPath, "--token", token, "--goal", "Return a local marker.", "--max-steps", "1"}); err != nil {
		t.Fatalf("automatic local agent failed: %v", err)
	}
	lock.Lock()
	defer lock.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected planner and one execution request, got %d", len(requests))
	}
	for index, request := range requests {
		if request.Requirements.Provider != "ollama" || request.Requirements.Egress != "local_only" {
			t.Fatalf("request %d escaped local Ollama boundary: %#v", index+1, request.Requirements)
		}
		if request.Requirements.AdapterProfile != "" || request.Requirements.AdapterFreshSession || request.Requirements.AdapterEphemeralSession {
			t.Fatalf("request %d gained adapter authority: %#v", index+1, request.Requirements)
		}
		if request.MaxAttempts != 1 {
			t.Fatalf("request %d did not remain single-attempt: %d", index+1, request.MaxAttempts)
		}
	}
}

func TestAgentAutoRejectsWiderAuthorityBeforeLoadingConfig(t *testing.T) {
	cases := [][]string{
		{"--goal", "x", "--planner-provider", "deepseek"},
		{"--goal", "x", "--allow-providers", "ollama,adapter"},
		{"--goal", "x", "--allow-adapter-profiles", "profile-two"},
		{"--goal", "x", "--max-steps", "4"},
	}
	for _, args := range cases {
		if err := clusterAgentAutoCommand(args); err == nil {
			t.Fatalf("unsafe authority was accepted: %v", args)
		}
	}
}

func TestAgentAutoNamedPolicyCarriesProjectAuthorityWithoutPlannerEscalation(t *testing.T) {
	const token = "agent_policy_test_token_0123456789"
	var lock sync.Mutex
	requests := []cluster.SubmitRequest{}
	plannerPrompt := ""
	researchPrompt := ""
	researchInput := ""
	researchOutputMode := ""
	summaryInput := ""
	summaryOutputMode := ""
	applyPrompt := ""
	applyInput := ""
	applyOutputMode := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/cluster/jobs":
			var input cluster.SubmitRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			lock.Lock()
			requests = append(requests, input)
			id := "planner"
			var payload bridge.Job
			if err := json.Unmarshal(input.Payload, &payload); err != nil {
				lock.Unlock()
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			switch len(requests) {
			case 1:
				plannerPrompt = payload.Prompt
			case 2:
				id = "research"
				researchPrompt = payload.Prompt
				researchInput = payload.Text
				researchOutputMode = payload.Output.Mode
			case 3:
				id = "summary"
				summaryInput = payload.Text
				summaryOutputMode = payload.Output.Mode
			case 4:
				id = "apply"
				applyPrompt = payload.Prompt
				applyInput = payload.Text
				applyOutputMode = payload.Output.Mode
			}
			lock.Unlock()
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: id, Status: cluster.JobQueued})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/planner":
			output := &bridge.Output{Mode: "json", Model: "qwen-test", JSON: json.RawMessage(`{"version":1,"summary":"Inspect, derive one request, then apply it.","steps":[{"id":"research","provider":"adapter","profile":"profile-two","instruction":"{\"schema\":\"example.request.v1\",\"action\":\"capabilities\"}","use_previous":false},{"id":"summarize","provider":"ollama","profile":"","instruction":"Use the submitted evidence to return exactly one example.request.v1 JSON object.","use_previous":true},{"id":"apply","provider":"adapter","profile":"profile-two","instruction":"contextbridge.previous-json.v1","use_previous":true}]}`)}
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: output})
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "planner", Status: cluster.JobCompleted, AssignedNode: "node-local", Result: raw})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/research":
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: &bridge.Output{Mode: "json", JSON: json.RawMessage(` {"schema":"evidence.v1","answer":"bounded"} `), Model: "profile-two-test"}})
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "research", Status: cluster.JobCompleted, AssignedNode: "node-adapter", Result: raw})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/summary":
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: &bridge.Output{Mode: "json", JSON: json.RawMessage(` {"schema":"example.request.v1","action":"apply"} `), Model: "qwen-test"}})
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "summary", Status: cluster.JobCompleted, AssignedNode: "node-local", Result: raw})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/apply":
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: &bridge.Output{Mode: "json", JSON: json.RawMessage(`{"schema":"example.result.v1","status":"applied"}`), Model: "profile-two-test"}})
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "apply", Status: cluster.JobCompleted, AssignedNode: "node-adapter", Result: raw})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/cluster/routes/explain":
			var input cluster.AssignmentRequest
			_ = json.NewDecoder(request.Body).Decode(&input)
			if input.TenantID != "demo-project" || input.Requirements.Group != "private" || input.Requirements.Egress != "remote_allowed" {
				http.Error(writer, "project scope lost", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(writer).Encode(cluster.RoutingDecision{Preview: true, SelectedNodeID: "node-adapter", SelectedNodeName: "Adapter node"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = server.URL
	defaultRoute := cfg.Routes["default"]
	defaultRoute.Model = "qwen-test"
	cfg.Routes["default"] = defaultRoute
	cfg.AdapterProfiles["profile-two"] = config.AdapterProfile{
		Label: "Remote B", Driver: "test",
		Options: map[string]interface{}{config.AdapterAgentInstructionContractOption: "Instruction must be exactly one example.request.v1 JSON object."},
	}
	cfg.Routes["profile-two"] = config.Route{Provider: "adapter", AdapterProfile: "profile-two", Task: "generation"}
	cfg.Providers.Adapter.Principals["profile-two-test"] = config.AdapterPrincipal{
		Token: "agent_policy_adapter_token_0123456789", AllowedProfiles: []string{"profile-two"},
	}
	cfg.Cluster.Policies.AgentAuthorities["demo"] = config.AgentAuthority{
		Enabled: true, TenantID: "demo-project", Group: "private",
		Planner:          config.AgentPlanner{Provider: "ollama", TimeoutSeconds: 120},
		AllowedProviders: []string{"adapter", "ollama"}, AllowedAdapterProfiles: []string{"profile-two"},
		Egress: "remote_allowed", AllowUnknownCost: true, MaxSteps: 3, StepTimeoutSeconds: 120, MaxRuntimeSeconds: 300,
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := clusterAgentAutoCommand([]string{"--config", configPath, "--token", token, "--goal", "Review this.", "--policy", "demo"}); err != nil {
		t.Fatalf("named automatic policy failed: %v", err)
	}
	lock.Lock()
	defer lock.Unlock()
	if len(requests) != 4 {
		t.Fatalf("expected planner and three steps, got %d", len(requests))
	}
	if requests[0].Requirements.Provider != "ollama" || requests[1].Requirements.Provider != "adapter" || requests[2].Requirements.Provider != "ollama" || requests[3].Requirements.Provider != "adapter" {
		t.Fatalf("unexpected provider sequence: %#v", requests)
	}
	wantRoutes := []string{"default", "profile-two", "default", "profile-two"}
	wantProviders := []string{"ollama", "adapter", "ollama", "adapter"}
	wantModels := []string{"qwen-test", "", "qwen-test", ""}
	for index, input := range requests {
		if input.TenantID != "demo-project" || input.Requirements.Group != "private" || input.Requirements.Egress != "remote_allowed" || input.MaxAttempts != 1 {
			t.Fatalf("request %d escaped project authority: %#v", index+1, input)
		}
		var payload bridge.Job
		if err := json.Unmarshal(input.Payload, &payload); err != nil {
			t.Fatalf("request %d payload: %v", index+1, err)
		}
		if payload.Route != wantRoutes[index] {
			t.Fatalf("request %d route = %q; want %q", index+1, payload.Route, wantRoutes[index])
		}
		if payload.Provider != wantProviders[index] || payload.Model != wantModels[index] || input.Requirements.Model != wantModels[index] {
			t.Fatalf("request %d target = provider %q model %q (requirement %q); want provider %q model %q", index+1, payload.Provider, payload.Model, input.Requirements.Model, wantProviders[index], wantModels[index])
		}
	}
	if !requests[1].Requirements.AdapterFreshSession || !requests[1].Requirements.AdapterEphemeralSession {
		t.Fatalf("adapter step did not remain isolated: %#v", requests[1].Requirements)
	}
	if !requests[3].Requirements.AdapterFreshSession || !requests[3].Requirements.AdapterEphemeralSession {
		t.Fatalf("previous-result adapter step did not remain isolated: %#v", requests[3].Requirements)
	}
	if !strings.Contains(plannerPrompt, "example.request.v1") || strings.Contains(plannerPrompt, "must-not-leak") {
		t.Fatalf("planner did not receive only the bounded adapter contract: %q", plannerPrompt)
	}
	if researchPrompt != agentAdapterPrompt || researchInput != `{"schema":"example.request.v1","action":"capabilities"}` {
		t.Fatalf("adapter request did not cross the trusted prompt boundary correctly: prompt=%q text=%q", researchPrompt, researchInput)
	}
	if researchOutputMode != "json" {
		t.Fatalf("contracted adapter evidence did not require strict JSON output: %q", researchOutputMode)
	}
	if summaryInput != `{"schema":"evidence.v1","answer":"bounded"}` {
		t.Fatalf("structured adapter evidence was not normalized as untrusted next-step input: %q", summaryInput)
	}
	if summaryOutputMode != "json" {
		t.Fatalf("model-to-adapter handoff did not require strict JSON output: %q", summaryOutputMode)
	}
	if applyPrompt != agentAdapterPrompt || applyInput != `{"schema":"example.request.v1","action":"apply"}` || applyOutputMode != "json" {
		t.Fatalf("previous strict JSON did not cross the isolated adapter boundary: prompt=%q text=%q output=%q", applyPrompt, applyInput, applyOutputMode)
	}
	if err := clusterAgentAutoCommand([]string{"--config", configPath, "--token", token, "--goal", "x", "--policy", "demo", "--max-steps", "6"}); err == nil || !strings.Contains(err.Error(), "cannot override") {
		t.Fatalf("CLI override widened named policy: %v", err)
	}
}
