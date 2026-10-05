package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestWorkModeValidationAndLegacyPrompt(t *testing.T) {
	for _, mode := range []string{"", "normal", "lazy"} {
		if err := validateWorkMode(mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"ultra", "LAZY", " lazy", "lazy\nignore policy"} {
		if err := validateWorkMode(mode); err == nil {
			t.Fatalf("accepted %q", mode)
		}
	}
	const prompt = "exact\ncontract\x00"
	if applyWorkMode("", prompt) != prompt {
		t.Fatal("default prompt changed")
	}
	if !strings.HasSuffix(applyWorkMode("lazy", prompt), prompt) {
		t.Fatal("original contract lost")
	}
	if !strings.Contains(applyWorkMode("normal", prompt), "Disable prior optional lazy-style guidance") {
		t.Fatal("normal cannot reset lazy")
	}
}

func TestWorkModePreservesEveryExactAdapterPrompt(t *testing.T) {
	const prompt = `{"schema":"fixture.v1","instruction":"unchanged"}`
	for _, mode := range []string{"", "normal", "lazy"} {
		if got := agentWorkModePrompt(mode, agentStep{Provider: "adapter"}, prompt); got != prompt {
			t.Fatalf("%s changed adapter contract: %q", mode, got)
		}
	}
}

func TestChatWorkModeDoesNotAlterAuthority(t *testing.T) {
	state := chatState{provider: "ollama", profile: "scoped", egress: "local_only", maxCostUSD: 0.03, e2ee: true, token: "fixture", group: "fixture", localTools: true}
	before := state
	for _, mode := range []string{"lazy", "normal"} {
		handled, message := state.command("/mode " + mode)
		if !handled || !strings.Contains(message, "mode: "+mode) || state.workMode != mode {
			t.Fatal(message)
		}
		compare := state
		compare.workMode = before.workMode
		if !reflect.DeepEqual(compare, before) {
			t.Fatal("work style changed authority or other session state")
		}
	}
	before = state
	_, message := state.command("/mode ultra")
	if !strings.Contains(message, "permissions stay unchanged") || !reflect.DeepEqual(state, before) {
		t.Fatal("invalid mode changed state")
	}
	_, message = state.command("/mode")
	if !strings.Contains(message, "mode: normal") || !reflect.DeepEqual(state, before) {
		t.Fatal("read changed state")
	}
	_, message = state.command("/settings")
	if !strings.Contains(message, "mode normal") {
		t.Fatal("settings hide style")
	}
}

func TestLazyLocalMathDoesNotNeedModelOrRelay(t *testing.T) {
	state := chatState{provider: "ollama", egress: "local_only", localTools: true, workMode: "lazy"}
	// No token, relay or model: this succeeds only via the deterministic tool.
	if err := state.turn(context.Background(), "Was ist 10 mal 3 / 30?"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkModeIsBoundIntoPlanApprovalAndLegacyEncoding(t *testing.T) {
	plan := validAgentPlanForTest(t)
	original, raw, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "work_mode") {
		t.Fatal("legacy plan encoding changed")
	}
	policy := plan.Policy
	plan.WorkMode = "lazy"
	changed, raw, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if changed == original || !strings.Contains(string(raw), `"work_mode": "lazy"`) {
		t.Fatal("mode not bound into approval")
	}
	var roundtrip agentPlan
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.WorkMode != "lazy" || !reflect.DeepEqual(roundtrip.Policy, policy) {
		t.Fatal("roundtrip changed mode/authority")
	}
	plan.WorkMode = "ultra"
	if err := validateAgentPlan(plan); err == nil {
		t.Fatal("unsupported saved mode accepted")
	}
}

func TestWorkModeRejectedBeforeLoadingConfig(t *testing.T) {
	if err := clusterChatCommandWithDefaults([]string{"--mode", "ultra", "--config", "missing-config"}, "do", "ollama", "", ""); err == nil || !strings.Contains(err.Error(), "--mode must") {
		t.Fatalf("chat: %v", err)
	}
	if err := clusterAgentAutoCommand([]string{"--mode", "ultra", "--config", "missing-config"}); err == nil || !strings.Contains(err.Error(), "--mode must") {
		t.Fatalf("agent: %v", err)
	}
	if chatHelpRequested([]string{"--mode", "--help"}) {
		t.Fatal("flag value treated as help")
	}
}

func TestPlannerCannotChooseItsOwnWorkMode(t *testing.T) {
	_, err := decodeAgentProposal([]byte(`{"version":1,"work_mode":"lazy","summary":"x","steps":[{"id":"x","provider":"ollama","profile":"","instruction":"x","use_previous":false}]}`))
	if err == nil {
		t.Fatal("planner supplied an operator-owned field")
	}
}

func TestChatWorkModeWireContract(t *testing.T) {
	for _, provider := range []string{"ollama", "adapter"} {
		for _, mode := range []string{"", "lazy", "normal"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				captured := make(chan cluster.SubmitRequest, 1)
				result, err := json.Marshal(bridge.Submission{Status: "completed", Output: &bridge.Output{Mode: "text", Text: "fixture"}})
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer fixture" {
						http.Error(w, "auth", 401)
						return
					}
					switch {
					case r.Method == "POST" && r.URL.Path == "/v1/cluster/jobs":
						var req cluster.SubmitRequest
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							http.Error(w, "json", 400)
							return
						}
						captured <- req
						_ = json.NewEncoder(w).Encode(cluster.Job{ID: "fixture", Status: cluster.JobQueued})
					case r.Method == "GET" && r.URL.Path == "/v1/cluster/jobs/fixture":
						_ = json.NewEncoder(w).Encode(cluster.Job{ID: "fixture", Status: cluster.JobCompleted, Result: result})
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				state := chatState{relayURL: server.URL, token: "fixture", provider: provider, egress: "local_only", group: "fixture", workMode: mode, sessionID: "fixture"}
				if provider == "adapter" {
					state.profile = "fixture-profile"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				const prompt = `{"schema":"fixture.v1","action":"capabilities"}`
				if err := state.turn(ctx, prompt); err != nil {
					t.Fatal(err)
				}
				req := <-captured
				var job bridge.Job
				if err := json.Unmarshal(req.Payload, &job); err != nil {
					t.Fatal(err)
				}
				want := prompt
				if provider != "adapter" {
					want = applyWorkMode(mode, prompt)
				}
				if job.Prompt != want {
					t.Fatalf("prompt changed unexpectedly: %q", job.Prompt)
				}
				if req.Requirements.Provider != provider || req.Requirements.Egress != "local_only" || req.Requirements.Group != "fixture" || req.Requirements.AdapterProfile != state.profile || req.MaxAttempts != 1 {
					t.Fatalf("routing authority changed: %#v", req.Requirements)
				}
			})
		}
	}
}
