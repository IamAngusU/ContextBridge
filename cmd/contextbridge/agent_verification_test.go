package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const gateTestDefinition = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func gateTestProfile() config.AdapterProfile {
	return config.AdapterProfile{Options: map[string]interface{}{
		config.AdapterAgentInstructionContractOption: "Return one strict checker JSON request.",
		config.AdapterAgentVerificationChecksOption:  map[string]string{"check-v1": gateTestDefinition},
		"private-path": "DO-NOT-EXPOSE-ME",
	}}
}

func gateTestReceipt(input, status string) map[string]interface{} {
	passed := 1
	if status != "passed" {
		passed = 0
	}
	return map[string]interface{}{"schema": "contextbridge.agent-verification.v1", "check": "check-v1",
		"definition_sha256": gateTestDefinition, "input_sha256": fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(input))),
		"status": status, "passed": passed, "total": 1}
}

func TestAgentVerificationReceipt(t *testing.T) {
	cfg := config.Config{AdapterProfiles: map[string]config.AdapterProfile{"checker": gateTestProfile()}}
	step := agentStep{Provider: "adapter", Profile: "checker", VerificationGate: &agentVerificationGate{Check: "check-v1"}}
	input := "{\n\"data\":\"ü\"}"
	for _, status := range []string{"passed", "failed"} {
		raw, _ := json.Marshal(map[string]interface{}{"agent_verification": gateTestReceipt(input, status)})
		got, err := agentVerificationStatus(cfg, step, input, &bridge.Output{Mode: "json", JSON: raw})
		if err != nil || got != status {
			t.Fatalf("%s: %s, %v", status, got, err)
		}
	}
	for _, field := range []string{"schema", "check", "definition_sha256", "input_sha256", "status", "passed", "total", "unknown"} {
		t.Run(field, func(t *testing.T) {
			receipt := gateTestReceipt(input, "passed")
			receipt[field] = "invalid"
			raw, _ := json.Marshal(map[string]interface{}{"agent_verification": receipt})
			if _, err := agentVerificationStatus(cfg, step, input, &bridge.Output{Mode: "json", JSON: raw}); err == nil {
				t.Fatal("accepted invalid receipt")
			}
		})
	}
	for _, change := range []map[string]interface{}{
		{"passed": nil}, {"total": nil}, {"passed": -1}, {"passed": 2}, {"total": 0}, {"total": 1000001},
		{"passed": 0}, {"status": "failed"}, {"status": "inconclusive"}, {"total": 1.5},
	} {
		receipt := gateTestReceipt(input, "passed")
		for k, v := range change {
			receipt[k] = v
		}
		raw, _ := json.Marshal(map[string]interface{}{"agent_verification": receipt})
		if _, err := agentVerificationStatus(cfg, step, input, &bridge.Output{Mode: "json", JSON: raw}); err == nil {
			t.Fatalf("accepted %#v", change)
		}
	}
	valid, _ := json.Marshal(map[string]interface{}{"agent_verification": gateTestReceipt(input, "passed")})
	for _, out := range []*bridge.Output{nil, {Mode: "text", Text: string(valid)}, {Mode: "json", Text: "PASS", JSON: valid},
		{Mode: "json", Error: " ", JSON: valid}, {Mode: "json", Truncated: true, JSON: valid},
		{Mode: "json", JSON: json.RawMessage(`{"agent_verification":null}`)},
		{Mode: "json", JSON: json.RawMessage(strings.Replace(string(valid), `"total":1`, `"total":1,"total":1`, 1))},
	} {
		if _, err := agentVerificationStatus(cfg, step, input, out); err == nil {
			t.Fatal("accepted missing/prose/ambiguous evidence")
		}
	}
	if _, err := agentVerificationStatus(cfg, step, input+" ", &bridge.Output{Mode: "json", JSON: valid}); err == nil {
		t.Fatal("accepted receipt for other bytes")
	}
	delete(cfg.AdapterProfiles["checker"].Options, config.AdapterAgentVerificationChecksOption)
	if _, err := agentVerificationStatus(cfg, step, input, &bridge.Output{Mode: "json", JSON: valid}); err == nil {
		t.Fatal("accepted unpinned verifier")
	}
}

func TestAgentVerificationPlanAndPlannerBoundary(t *testing.T) {
	plan := validAgentPlanForTest(t)
	plan.Steps = plan.Steps[:1]
	before, _, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].VerificationGate = &agentVerificationGate{Check: "check-v1"}
	if err := validateAgentPlan(plan); err != nil {
		t.Fatal(err)
	}
	after, _, _ := encodeAgentPlan(plan)
	if before == after {
		t.Fatal("gate not approval-bound")
	}
	for _, steps := range [][]agentStep{
		{{Provider: "ollama", VerificationGate: &agentVerificationGate{Check: "check-v1"}}},
		{{Provider: "adapter", VerificationGate: &agentVerificationGate{Check: "bad/id"}}},
		{plan.Steps[0], {Provider: "ollama"}},
		{plan.Steps[0], {Provider: "adapter", Profile: "other", VerificationGate: &agentVerificationGate{Check: "check-v1"}}},
		{plan.Steps[0], {Provider: "adapter", Profile: "profile-two", VerificationGate: &agentVerificationGate{Check: "other"}}},
	} {
		if err := validateAgentVerificationGates(steps); err == nil {
			t.Fatal("invalid gate structure accepted")
		}
	}
	cfg := config.Config{AdapterProfiles: map[string]config.AdapterProfile{"profile-two": gateTestProfile()}}
	prompt := agentAdapterInstructionContracts(cfg, plan.Policy) + agentVerificationPlannerInstructions(cfg, plan.Policy)
	if !strings.Contains(prompt, `"verification_gate"`) && !strings.Contains(prompt, "verification_gate:") {
		t.Fatal("gate absent from planner")
	}
	if strings.Contains(prompt, "DO-NOT-EXPOSE") || strings.Contains(prompt, gateTestDefinition) {
		t.Fatal("private options/pins leaked")
	}
	proposal, err := decodeAgentProposal([]byte(`{"version":1,"summary":"Check","steps":[{"id":"check","provider":"adapter","profile":"profile-two","instruction":{},"use_previous":false,"verification_gate":{"check":"check-v1"}}]}`))
	if err != nil || proposal.Steps[0].VerificationGate == nil {
		t.Fatalf("gate proposal rejected: %v", err)
	}
	_, err = decodeAgentProposal([]byte(`{"version":1,"summary":"Check","steps":[{"id":"check","provider":"adapter","instruction":{},"use_previous":false,"verification_gate":{"check":"check-v1","success":"deploy"}}]}`))
	if err == nil {
		t.Fatal("arbitrary branch accepted")
	}
}
