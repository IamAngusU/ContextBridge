package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func validAgentPlanForTest(t *testing.T) agentPlan {
	t.Helper()
	policy, err := newAgentPolicy("ollama,browser", "gemini", 3, 120, 600)
	if err != nil {
		t.Fatal(err)
	}
	return agentPlan{
		Version: agentPlanVersion,
		Goal:    "Compare two short answers.",
		Summary: "Draft locally, then review in Gemini.",
		Policy:  policy,
		Evidence: agentPlannerEvidence{
			Provider: "deepseek", JobID: "job-planner", NodeID: "node-one", CostStatus: "upper_bound",
		},
		Steps: []agentStep{
			{ID: "draft", Provider: "ollama", Instruction: "Produce a concise draft."},
			{ID: "review", Provider: "browser", Profile: "gemini", Instruction: "Review the submitted draft for factual errors.", UsePrevious: true},
		},
	}
}

func TestAgentPlanBindsAndValidatesExplicitPolicy(t *testing.T) {
	plan := validAgentPlanForTest(t)
	if err := validateAgentPlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.Steps[1].Profile = "chatgpt"
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "explicitly approved") {
		t.Fatalf("unapproved browser profile was not rejected: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Steps[0].Provider = "deepseek"
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "outside the approved policy") {
		t.Fatalf("unapproved provider was not rejected: %v", err)
	}
}

func TestAgentPlanRejectsUnsafeShape(t *testing.T) {
	plan := validAgentPlanForTest(t)
	plan.Steps[0].UsePrevious = true
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "first agent step") {
		t.Fatalf("first-step previous result was not rejected: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Steps[1].ID = plan.Steps[0].ID
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate step ID was not rejected: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Steps[0].Instruction = strings.Repeat("x", agentMaximumInstruction+1)
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized instruction was not rejected: %v", err)
	}
}

func TestAgentProposalRejectsPolicyAndTrailingJSON(t *testing.T) {
	withPolicy := []byte(`{"version":1,"summary":"x","steps":[],"policy":{"max_steps":6}}`)
	if _, err := decodeAgentProposal(withPolicy); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("planner-controlled policy was not rejected: %v", err)
	}
	trailing := []byte(`{"version":1,"summary":"x","steps":[]} {"second":true}`)
	if _, err := decodeAgentProposal(trailing); err == nil || !strings.Contains(err.Error(), "multiple JSON") {
		t.Fatalf("second JSON value was not rejected: %v", err)
	}
}

func TestAgentPlanDigestCoversPolicyAndNormalizesWhitespace(t *testing.T) {
	plan := validAgentPlanForTest(t)
	digest, raw, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAgentPlan(append([]byte(" \n"), append(raw, []byte("\n ")...)...))
	if err != nil {
		t.Fatal(err)
	}
	digestAgain, _, err := encodeAgentPlan(decoded)
	if err != nil || digestAgain != digest {
		t.Fatalf("stable plan digest mismatch: %s / %s / %v", digest, digestAgain, err)
	}
	decoded.Policy.MaxRuntimeSeconds++
	changed, _, err := encodeAgentPlan(decoded)
	if err != nil || changed == digest {
		t.Fatalf("policy change did not alter approval digest: %s / %s / %v", digest, changed, err)
	}
}

func TestAgentPlannerPromptMakesAuthorityBoundaryExplicit(t *testing.T) {
	policy, err := newAgentPolicy("ollama,browser", "gemini", 2, 120, 300)
	if err != nil {
		t.Fatal(err)
	}
	prompt := agentPlannerPrompt(policy)
	for _, required := range []string{"untrusted data", "separate hash approval", `"ollama"`, `"gemini"`, "shell commands, tools"} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(required)) {
			t.Errorf("planner prompt lacks %q", required)
		}
	}
	var proposal agentPlannerProposal
	if err := json.Unmarshal([]byte(`{"version":1,"summary":"one","steps":[{"id":"draft","provider":"ollama","instruction":"Draft."}]}`), &proposal); err != nil || proposal.Steps[0].Provider != "ollama" {
		t.Fatalf("documented planner schema is not decodable: %#v / %v", proposal, err)
	}
}
