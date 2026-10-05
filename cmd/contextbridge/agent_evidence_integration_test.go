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

const agentEvidenceFixtureGoal = "Keep the project constraints and test results together."

// This fixture controls transport results, not the quality of a real model or
// a persistent-memory adapter. It never connects to the installed pool.
func agentEvidenceFixture(t *testing.T, mode string) (string, func() []cluster.SubmitRequest) {
	t.Helper()
	proposal := agentEvidencePlanForTest(t)
	proposal.Steps = []agentStep{
		{ID: "memory", Provider: "ollama", Instruction: "Return the memory fixture."},
		{ID: "tests", Provider: "ollama", Instruction: "Return the test fixture."},
		{ID: "unrelated", Provider: "ollama", Instruction: "Return unrelated text."},
		{ID: "combine", Provider: "ollama", Instruction: "Compare both selected sources.", InputSteps: []string{"memory", "tests"}, OutputMode: "json"},
	}
	if mode == "unknown" {
		proposal.Steps[3].InputSteps = []string{"unknown"}
	}
	// Fresh proposals require use_previous even when false (saved plans omit it).
	wireSteps := make([]map[string]interface{}, 0, len(proposal.Steps))
	for _, step := range proposal.Steps {
		wire := map[string]interface{}{"id": step.ID, "provider": step.Provider, "instruction": step.Instruction, "use_previous": false}
		if len(step.InputSteps) > 0 {
			wire["input_steps"] = step.InputSteps
		}
		if step.OutputMode != "" {
			wire["output_mode"] = step.OutputMode
		}
		wireSteps = append(wireSteps, wire)
	}
	wire, err := json.Marshal(map[string]interface{}{"version": 1, "summary": "Combine explicit earlier evidence", "steps": wireSteps})
	if err != nil {
		t.Fatal(err)
	}
	var lock sync.Mutex
	var requests []cluster.SubmitRequest
	results := map[string]*bridge.Output{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+agentPreviewTestToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/jobs":
			var input cluster.SubmitRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			var payload bridge.Job
			if err := json.Unmarshal(input.Payload, &payload); err != nil {
				http.Error(w, "invalid payload", 400)
				return
			}
			lock.Lock()
			requests = append(requests, input)
			id := fmt.Sprintf("job-%d", len(requests))
			output := &bridge.Output{Mode: "text", Model: "test-model", Text: "COMBINED-OK"}
			switch {
			case input.Source == "agent-planner":
				output.Mode = "json"
				output.Text = ""
				output.JSON = wire
			case strings.HasSuffix(payload.SessionID, "-memory"):
				output.Text = "PIN: remain offline; unfinished task: fix dates"
				if mode == "oversize" {
					output.Text = strings.Repeat("x", agentMaximumEvidenceBytes)
				}
				if mode == "truncated" {
					output.Truncated = true
				}
			case strings.HasSuffix(payload.SessionID, "-tests"):
				output.Text = "date cases: 3 passed / 10 total"
			case strings.HasSuffix(payload.SessionID, "-unrelated"):
				output.Text = "UNSELECTED-PRIVATE-MARKER"
			case strings.HasSuffix(payload.SessionID, "-combine"):
				if mode != "json-wrapped-text" {
					output.Mode = "json"
					output.Text = ""
					output.JSON = json.RawMessage(`{"result":"COMBINED-OK"}`)
				}
			}
			results[id] = output
			lock.Unlock()
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: id, Status: cluster.JobQueued})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/cluster/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/cluster/jobs/")
			lock.Lock()
			output := results[id]
			lock.Unlock()
			if output == nil {
				http.NotFound(w, r)
				return
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
	cfg.Cluster.Policies.AgentAuthorities["evidence"] = config.AgentAuthority{
		Enabled: true, TenantID: "project", Group: "local-workers",
		Planner:          config.AgentPlanner{Provider: "ollama", Model: "test-model", TimeoutSeconds: 30},
		AllowedProviders: []string{"ollama"}, Egress: "local_only", AllowUnknownCost: true,
		MaxSteps: 4, StepTimeoutSeconds: 30, MaxRuntimeSeconds: 180,
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

func assertAgentEvidenceRequests(t *testing.T, requests []cluster.SubmitRequest) {
	t.Helper()
	if len(requests) != 5 {
		t.Fatalf("want planner + four work jobs, got %d", len(requests))
	}
	for _, req := range requests {
		if req.TenantID != "project" || req.Requirements.Group != "local-workers" || req.Requirements.Egress != "local_only" ||
			req.Requirements.Provider != "ollama" || req.Requirements.AdapterProfile != "" || req.MaxAttempts != 1 {
			t.Fatalf("evidence escaped authority: %#v", req)
		}
	}
	var payload bridge.Job
	if err := json.Unmarshal(requests[4].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Output.Mode != "json" {
		t.Fatal("machine-readable output contract was not submitted")
	}
	var bundle agentEvidenceBundle
	if err := json.Unmarshal([]byte(payload.Text), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Inputs) != 2 || bundle.Goal != agentEvidenceFixtureGoal || strings.Contains(payload.Text, "UNSELECTED-PRIVATE-MARKER") {
		t.Fatalf("wrong selected evidence: %#v", bundle)
	}
	for i, id := range []string{"memory", "tests"} {
		e := bundle.Inputs[i]
		if e.StepID != id || e.JobID != fmt.Sprintf("job-%d", i+2) || e.NodeID != "test-node" ||
			e.Provider != "ollama" || e.SHA256 != agentEvidenceDigest(e.Content) || strings.Contains(payload.Prompt, e.Content) {
			t.Fatalf("incorrect provenance/trust boundary: %#v", e)
		}
	}
}

func TestAgentEvidenceEndToEndSelectedResultsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		mode        string
		submissions int
		errorText   string
	}{
		{"success", 5, ""}, {"unknown", 1, "input_steps"}, {"oversize", 4, "evidence exceeds"}, {"truncated", 2, "partial text"}, {"json-wrapped-text", 5, "requires a JSON result envelope"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			path, requests := agentEvidenceFixture(t, tc.mode)
			err := clusterAgentAutoCommand([]string{"--config", path, "--token", agentPreviewTestToken, "--policy", "evidence", "--goal", agentEvidenceFixtureGoal})
			if tc.errorText == "" {
				if err != nil {
					t.Fatal(err)
				}
				assertAgentEvidenceRequests(t, requests())
			} else if err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("wrong failure: %v", err)
			}
			if len(requests()) != tc.submissions {
				t.Fatalf("unexpected work after failure: %d, want %d", len(requests()), tc.submissions)
			}
		})
	}
}

func TestAgentEvidenceExternalBinary(t *testing.T) {
	binary := os.Getenv("CONTEXTBRIDGE_TEST_BINARY")
	if binary == "" {
		t.Skip("set CONTEXTBRIDGE_TEST_BINARY to an explicitly built binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("binary must be absolute")
	}
	path, requests := agentEvidenceFixture(t, "success")
	planPath := filepath.Join(t.TempDir(), "plan.json")
	invoke := func(ok bool, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if ctx.Err() != nil || (err == nil) != ok {
			t.Fatalf("CLI success=%v want %v: %v\n%s", err == nil, ok, err, out)
		}
		return string(out)
	}
	out := invoke(true, "cluster", "agent", "plan", "--config", path, "--token", agentPreviewTestToken, "--policy", "evidence", "--goal", agentEvidenceFixtureGoal, "--out", planPath)
	if len(requests()) != 1 || !strings.Contains(out, "memory, tests") {
		t.Fatalf("preview lacks selection or executed work: %s", out)
	}
	raw, err := os.ReadFile(planPath)
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
	plan.Steps[3].InputSteps = []string{"tests"}
	_, changed, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"cluster", "agent", "run", "--config", path, "--token", agentPreviewTestToken, "--plan", planPath, "--approve", digest}
	out = invoke(false, args...)
	if len(requests()) != 1 || !strings.Contains(out, "approval mismatch") {
		t.Fatalf("changed inputs inherited approval: %s", out)
	}
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out = invoke(true, args...)
	if !strings.Contains(out, "COMBINED-OK") {
		t.Fatalf("CLI did not finish: %s", out)
	}
	assertAgentEvidenceRequests(t, requests())
	t.Log("external CLI: reviewed input selection; tampering denied; two earlier results preserved without unrelated predecessor or authority expansion")
}
