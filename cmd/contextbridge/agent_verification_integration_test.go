package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// Transport fixture, not a claim that a real checker or model passed tests.
func agentGateFixture(t *testing.T, mode string) ([]string, func() []cluster.SubmitRequest) {
	t.Helper()
	var lock sync.Mutex
	var requests []cluster.SubmitRequest
	results := map[string]*bridge.Output{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+agentPreviewTestToken {
			http.Error(w, "unauthorized", 401)
			return
		}
		lock.Lock()
		defer lock.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/cluster/jobs":
			var request cluster.SubmitRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "bad request", 400)
				return
			}
			var payload bridge.Job
			if err := json.Unmarshal(request.Payload, &payload); err != nil {
				http.Error(w, "bad payload", 400)
				return
			}
			requests = append(requests, request)
			id := fmt.Sprintf("job-%d", len(requests))
			output := &bridge.Output{Mode: "json", Model: "test", JSON: json.RawMessage(`{"candidate":"corrected"}`)}
			if payload.Provider == "adapter" {
				status := "passed"
				if mode != "early-pass" && strings.HasSuffix(payload.SessionID, "-check") || mode == "exhausted" {
					status = "failed"
				}
				if mode == "inconclusive" {
					status = "inconclusive"
				}
				receipt := gateTestReceipt(payload.Text, status)
				if mode == "wrong-input" {
					receipt["input_sha256"] = gateTestDefinition
				}
				output.JSON, _ = json.Marshal(map[string]interface{}{"agent_verification": receipt})
				if mode == "prose" {
					output.Text, output.Mode, output.JSON = string(output.JSON), "text", nil
				}
			}
			results[id] = output
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: id, Status: cluster.JobQueued})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/cluster/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/cluster/jobs/")
			raw, _ := json.Marshal(bridge.Submission{Status: "completed", Output: results[id]})
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: id, Status: cluster.JobCompleted, AssignedNode: "test-node", Result: raw})
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
	cfg.AdapterProfiles["profile-two"] = gateTestProfile()
	cfg.Providers.Adapter.Principals["gate-test"] = config.AdapterPrincipal{Token: "gate_test_adapter_principal_token_123456", AllowedProfiles: []string{"profile-two"}}
	if mode == "missing-pin" {
		delete(cfg.AdapterProfiles["profile-two"].Options, config.AdapterAgentVerificationChecksOption)
	}
	cfg.Routes["check"] = config.Route{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two"}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := validAgentPlanForTest(t)
	plan.Policy.TenantID, plan.Policy.Group, plan.Policy.Egress = "project", "test-workers", "local_only"
	plan.Evidence.CostStatus = cluster.CostUnknown
	plan.Steps = []agentStep{
		{ID: "check", Provider: "adapter", Profile: "profile-two", Instruction: `{"candidate":"original"}`, VerificationGate: &agentVerificationGate{Check: "check-v1"}},
		{ID: "repair", Provider: "ollama", Instruction: "Fix the tested candidate.", UsePrevious: true},
		{ID: "verify", Provider: "adapter", Profile: "profile-two", Instruction: agentPreviousAdapterJSON, UsePrevious: true, VerificationGate: &agentVerificationGate{Check: "check-v1"}},
	}
	plan.Binding, err = agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	digest, raw, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return []string{"--config", path, "--token", agentPreviewTestToken, "--plan", planPath, "--approve", digest}, func() []cluster.SubmitRequest {
		lock.Lock()
		defer lock.Unlock()
		return append([]cluster.SubmitRequest(nil), requests...)
	}
}

func TestAgentVerificationExecutionAndExternalBinary(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, tc := range []struct {
			mode    string
			jobs    int
			failure string
		}{
			{"early-pass", 1, ""}, {"repair-pass", 3, ""}, {"exhausted", 3, "bounded repairs exhausted"},
			{"inconclusive", 1, "inconclusive"}, {"wrong-input", 1, "exact submitted input"},
			{"prose", 1, "complete JSON"}, {"missing-pin", 0, "not pinned"},
		} {
			t.Run(fmt.Sprintf("%s/external=%v", tc.mode, external), func(t *testing.T) {
				binary := os.Getenv("CONTEXTBRIDGE_TEST_BINARY")
				if external && binary == "" {
					t.Skip("explicitly built CLI binary required")
				}
				args, snapshot := agentGateFixture(t, tc.mode)
				var err error
				var message string
				if external {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					out, failure := exec.CommandContext(ctx, binary, append([]string{"cluster", "agent", "run"}, args...)...).CombinedOutput()
					err, message = failure, string(out)
					if tc.mode == "early-pass" && (!strings.Contains(message, "2 remaining step(s) skipped") || !strings.Contains(message, "1 reviewed step(s)")) {
						t.Fatalf("incorrect completion summary: %s", message)
					}
				} else {
					err = clusterAgentRunCommand(args)
					if err != nil {
						message = err.Error()
					}
				}
				if (err == nil) != (tc.failure == "") || tc.failure != "" && !strings.Contains(message, tc.failure) {
					t.Fatalf("unexpected outcome %v: %s", err, message)
				}
				requests := snapshot()
				if len(requests) != tc.jobs {
					t.Fatalf("got %d jobs, want %d", len(requests), tc.jobs)
				}
				for _, req := range requests {
					if req.TenantID != "project" || req.Requirements.Group != "test-workers" || req.Requirements.Egress != "local_only" || req.MaxAttempts != 1 {
						t.Fatal("gate expanded authority or retried")
					}
					if req.Requirements.Provider == "adapter" && req.Requirements.AdapterProfile != "profile-two" {
						t.Fatal("gate escaped profile scope")
					}
				}
			})
		}
	}
}
