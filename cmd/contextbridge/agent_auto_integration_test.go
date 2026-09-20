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
			if len(requests) > 1 {
				id = "step"
			}
			lock.Unlock()
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: id, Status: cluster.JobQueued})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/planner":
			output := &bridge.Output{Mode: "json", Model: "qwen-test", JSON: json.RawMessage(`{"version":1,"summary":"Review in Profile Two.","steps":[{"id":"review","provider":"adapter","profile":"profile-two","instruction":"Review the goal and answer briefly.","use_previous":false}]}`)}
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: output})
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "planner", Status: cluster.JobCompleted, AssignedNode: "node-local", Result: raw})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/cluster/jobs/step":
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: &bridge.Output{Mode: "text", Text: "POLICY-OK", Model: "profile-two-test"}})
			_ = json.NewEncoder(writer).Encode(cluster.Job{ID: "step", Status: cluster.JobCompleted, AssignedNode: "node-adapter", Result: raw})
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
	cfg.AdapterProfiles["profile-two"] = config.AdapterProfile{Label: "Remote B", Driver: "test"}
	cfg.Cluster.Policies.AgentAuthorities["demo"] = config.AgentAuthority{
		Enabled: true, TenantID: "demo-project", Group: "private",
		Planner:          config.AgentPlanner{Provider: "ollama", TimeoutSeconds: 120},
		AllowedProviders: []string{"adapter"}, AllowedAdapterProfiles: []string{"profile-two"},
		Egress: "remote_allowed", AllowUnknownCost: true, MaxSteps: 1, StepTimeoutSeconds: 120, MaxRuntimeSeconds: 300,
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := clusterAgentAutoCommand([]string{"--config", configPath, "--token", token, "--goal", "Review this.", "--policy", "demo"}); err != nil {
		t.Fatalf("named automatic policy failed: %v", err)
	}
	lock.Lock()
	defer lock.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected planner and one step, got %d", len(requests))
	}
	if requests[0].Requirements.Provider != "ollama" || requests[1].Requirements.Provider != "adapter" {
		t.Fatalf("unexpected provider sequence: %#v", requests)
	}
	for index, input := range requests {
		if input.TenantID != "demo-project" || input.Requirements.Group != "private" || input.Requirements.Egress != "remote_allowed" || input.MaxAttempts != 1 {
			t.Fatalf("request %d escaped project authority: %#v", index+1, input)
		}
	}
	if !requests[1].Requirements.AdapterFreshSession || !requests[1].Requirements.AdapterEphemeralSession {
		t.Fatalf("adapter step did not remain isolated: %#v", requests[1].Requirements)
	}
	if err := clusterAgentAutoCommand([]string{"--config", configPath, "--token", token, "--goal", "x", "--policy", "demo", "--max-steps", "6"}); err == nil || !strings.Contains(err.Error(), "cannot override") {
		t.Fatalf("CLI override widened named policy: %v", err)
	}
}
