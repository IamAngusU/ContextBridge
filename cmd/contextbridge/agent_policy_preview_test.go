package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const agentPreviewTestToken = "agent_preview_test_token_0123456789"

func agentPreviewFixture(t *testing.T) (string, func() []cluster.SubmitRequest) {
	t.Helper()
	var lock sync.Mutex
	var requests []cluster.SubmitRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+agentPreviewTestToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/jobs":
			var input cluster.SubmitRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			lock.Lock()
			requests = append(requests, input)
			lock.Unlock()
			id := "step"
			if input.Source == "agent-planner" {
				id = "planner"
			}
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: id, Status: cluster.JobQueued})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/cluster/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/cluster/jobs/")
			output := &bridge.Output{Mode: "text", Text: "REVIEWED-OK", Model: "test-model"}
			if id == "planner" {
				output = &bridge.Output{Mode: "json", Model: "test-model", JSON: json.RawMessage(`{"version":1,"summary":"One reviewed step","steps":[{"id":"draft","provider":"ollama","instruction":"Return the marker.","use_previous":false}]}`)}
			}
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: output})
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: id, Status: cluster.JobCompleted, AssignedNode: "test-node", Result: raw})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/routes/explain":
			_ = json.NewEncoder(w).Encode(cluster.RoutingDecision{Preview: true, SelectedNodeID: "test-node"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = server.URL
	cfg.Cluster.Policies.AgentAuthorities["review"] = config.AgentAuthority{
		Enabled: true, TenantID: "project", Group: "local-workers",
		Planner:          config.AgentPlanner{Provider: "ollama", Model: "test-model", TimeoutSeconds: 30},
		AllowedProviders: []string{"ollama"}, Egress: "local_only", AllowUnknownCost: true,
		MaxSteps: 2, StepTimeoutSeconds: 30, MaxRuntimeSeconds: 120,
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path, func() []cluster.SubmitRequest {
		lock.Lock()
		defer lock.Unlock()
		return append([]cluster.SubmitRequest(nil), requests...)
	}
}

func saveAgentPreview(t *testing.T, configPath string) (string, agentPlan, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := clusterAgentPlanCommand([]string{"--config", configPath, "--token", agentPreviewTestToken, "--policy", "review", "--goal", "Review this work first.", "--out", path}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := decodeAgentPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest, _, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	return path, plan, digest
}

func TestAgentNamedPolicyPreviewRequiresSeparateExactApproval(t *testing.T) {
	configPath, requests := agentPreviewFixture(t)
	path, plan, digest := saveAgentPreview(t, configPath)
	if plan.AuthorizationMode != agentAuthorizationManual || plan.Policy.AuthorityName != "review" || plan.Policy.TenantID != "project" || plan.Policy.Group != "local-workers" || plan.Policy.Egress != "local_only" {
		t.Fatalf("preview lost reviewed authority: %#v", plan)
	}
	if len(requests()) != 1 || requests()[0].Source != "agent-planner" {
		t.Fatal("preview executed a work step")
	}
	var payload bridge.Job
	if err := json.Unmarshal(requests()[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "test-model" || !strings.Contains(payload.Prompt, "separate hash approval") {
		t.Fatalf("preview did not preserve named planner and review prompt: %#v", payload)
	}
	args := []string{"--config", configPath, "--token", agentPreviewTestToken, "--plan", path, "--approve", "sha256:" + strings.Repeat("0", 64)}
	if err := clusterAgentRunCommand(args); err == nil || !strings.Contains(err.Error(), "approval mismatch") {
		t.Fatalf("wrong approval accepted: %v", err)
	}
	if len(requests()) != 1 {
		t.Fatal("wrong approval submitted work")
	}
	args[len(args)-1] = digest
	if err := clusterAgentRunCommand(args); err != nil {
		t.Fatal(err)
	}
	if len(requests()) != 2 {
		t.Fatalf("want exactly planner + approved step, got %d", len(requests()))
	}
	for _, request := range requests() {
		if request.TenantID != "project" || request.Requirements.Group != "local-workers" || request.Requirements.Egress != "local_only" || request.MaxAttempts != 1 {
			t.Fatalf("request lost authority: %#v", request)
		}
	}
}

func TestAgentNamedPolicyPreviewCannotOverrideAuthority(t *testing.T) {
	for _, flag := range []string{"planner-provider", "planner-profile", "planner-model", "allow-providers", "allow-adapter-profiles", "max-steps", "step-timeout", "max-runtime", "planner-timeout"} {
		t.Run(flag, func(t *testing.T) {
			err := clusterAgentPlanCommand([]string{"--config", "does-not-exist", "--goal", "x", "--policy", "review", "--" + flag, "1"})
			if err == nil || !strings.Contains(err.Error(), "cannot override") {
				t.Fatalf("override did not fail before loading config: %v", err)
			}
		})
	}
}

func TestAgentNamedPolicyPreviewRejectsDisabledAuthority(t *testing.T) {
	path, requests := agentPreviewFixture(t)
	cfg, _ := config.Load(path)
	authority := cfg.Cluster.Policies.AgentAuthorities["review"]
	authority.Enabled = false
	cfg.Cluster.Policies.AgentAuthorities["review"] = authority
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	err := clusterAgentPlanCommand([]string{"--config", path, "--token", agentPreviewTestToken, "--goal", "x", "--policy", "review"})
	if err == nil || !strings.Contains(err.Error(), "disabled") || len(requests()) != 0 {
		t.Fatalf("disabled policy reached planner: %v, %d requests", err, len(requests()))
	}
}

func TestAgentReviewedNamedPolicyRejectsTamperingAndConfigChanges(t *testing.T) {
	for _, mutation := range []string{"plan-budget", "plan-tenant", "plan-planner", "disable-config"} {
		t.Run(mutation, func(t *testing.T) {
			configPath, requests := agentPreviewFixture(t)
			path, plan, digest := saveAgentPreview(t, configPath)
			if mutation == "disable-config" {
				cfg, _ := config.Load(configPath)
				policy := cfg.Cluster.Policies.AgentAuthorities["review"]
				policy.Enabled = false
				cfg.Cluster.Policies.AgentAuthorities["review"] = policy
				if err := config.Save(configPath, cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				switch mutation {
				case "plan-budget":
					plan.Policy.MaxCostUSD = 100
				case "plan-tenant":
					plan.Policy.TenantID = "other-project"
				case "plan-planner":
					plan.Evidence.Provider = "deepseek"
				}
				var raw []byte
				var err error
				digest, raw, err = encodeAgentPlan(plan)
				if err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(t.TempDir(), "tampered.json")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := clusterAgentRunCommand([]string{"--config", configPath, "--token", agentPreviewTestToken, "--plan", path, "--approve", digest})
			if err == nil || len(requests()) != 1 {
				t.Fatalf("changed authority executed: %v, %d requests", err, len(requests()))
			}
		})
	}
}

func TestAgentPreflightRejectsBrokenLaterTargetBeforeAnyExecution(t *testing.T) {
	for _, failure := range []string{"missing-route", "ambiguous-route", "missing-contract"} {
		t.Run(failure, func(t *testing.T) {
			path, requests := agentPreviewFixture(t)
			cfg, _ := config.Load(path)
			plan := validAgentPlanForTest(t)
			plan.Steps = []agentStep{
				{ID: "first", Provider: "ollama", Instruction: "Return evidence."},
				{ID: "draft", Provider: "ollama", Instruction: "Make JSON.", UsePrevious: true},
				{ID: "apply", Provider: "adapter", Profile: "profile-two", Instruction: agentPreviousAdapterJSON, UsePrevious: true},
			}
			cfg.AdapterProfiles["profile-two"] = config.AdapterProfile{Driver: "test"}
			if failure != "missing-route" {
				cfg.Routes["apply"] = config.Route{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two"}
			}
			if failure == "ambiguous-route" {
				cfg.Routes["other"] = cfg.Routes["apply"]
			}
			err := executeAgentPlan(plan, "sha256:"+strings.Repeat("a", 64), cfg, agentPreviewTestToken)
			if err == nil || !strings.Contains(err.Error(), "preflight") || len(requests()) != 0 {
				t.Fatalf("broken later target ran preceding steps: %v, %d requests", err, len(requests()))
			}
		})
	}
}

func TestAgentPreflightAllowsContractedHandoffWithoutInventingRequest(t *testing.T) {
	plan := validAgentPlanForTest(t)
	plan.Steps = []agentStep{
		{ID: "draft", Provider: "ollama", Instruction: "Return JSON."},
		{ID: "apply", Provider: "adapter", Profile: "profile-two", Instruction: agentPreviousAdapterJSON, UsePrevious: true},
	}
	cfg := config.Config{Routes: map[string]config.Route{
		"default": {Provider: "ollama"},
		"apply":   {Provider: "adapter", AdapterProfile: "profile-two"},
	}, AdapterProfiles: map[string]config.AdapterProfile{
		"profile-two": {Driver: "test", Options: map[string]interface{}{config.AdapterAgentInstructionContractOption: "One JSON request."}},
	}}
	if err := validateAgentExecutionTargets(cfg, plan); err != nil {
		t.Fatal(err)
	}
	for _, contract := range []interface{}{"", " \n\t", 123, nil} {
		cfg.AdapterProfiles["profile-two"].Options[config.AdapterAgentInstructionContractOption] = contract
		if err := validateAgentExecutionTargets(cfg, plan); err == nil {
			t.Fatalf("invalid contract accepted: %#v", contract)
		}
	}
}

// An opt-in external-binary proof: a loopback fixture supplies deterministic
// provider evidence. This verifies the actual CLI, not model quality, a real
// worker, or the adapter's implementation. It cannot reach the user's pool.
func TestAgentPolicyPreviewExternalBinary(t *testing.T) {
	binary := os.Getenv("CONTEXTBRIDGE_TEST_BINARY")
	if binary == "" {
		t.Skip("set CONTEXTBRIDGE_TEST_BINARY to an explicitly built contextbridge binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("CONTEXTBRIDGE_TEST_BINARY must be an absolute path")
	}
	configPath, requests := agentPreviewFixture(t)
	path := filepath.Join(t.TempDir(), "reviewed.json")
	invoke := func(wantSuccess bool, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil || (err == nil) != wantSuccess {
			t.Fatalf("CLI success=%v, want %v: %v\n%s", err == nil, wantSuccess, err, output)
		}
		return string(output)
	}
	output := invoke(true, "cluster", "agent", "plan", "--config", configPath, "--token", agentPreviewTestToken, "--policy", "review", "--goal", "Preview this.", "--out", path)
	if !strings.Contains(output, "manual_hash") || len(requests()) != 1 {
		t.Fatalf("CLI preview executed work or lacked manual gate: %s", output)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := decodeAgentPlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest, _, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"cluster", "agent", "run", "--config", configPath, "--token", agentPreviewTestToken, "--plan", path, "--approve", "wrong"}
	output = invoke(false, args...)
	if !strings.Contains(output, "approval mismatch") || len(requests()) != 1 {
		t.Fatalf("CLI accepted incorrect approval: %s", output)
	}
	args[len(args)-1] = digest
	output = invoke(true, args...)
	if !strings.Contains(output, "REVIEWED-OK") || len(requests()) != 2 {
		t.Fatalf("CLI did not execute exactly the reviewed step: %s", output)
	}
	t.Log("external CLI: preview=1 planner/0 work; wrong approval=0 work; exact approval=1 scoped work step")
}
