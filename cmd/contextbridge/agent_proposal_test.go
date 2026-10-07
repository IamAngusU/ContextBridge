package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAgentFreshProposalRequiresExplicitDataFlow(t *testing.T) {
	for _, suffix := range []string{"", `,"use_previous":null`} {
		raw := `{"version":1,"summary":"repair","steps":[{"id":"repair","provider":"ollama","instruction":"Use the test diagnostics"` + suffix + `}]}`
		if _, err := decodeAgentProposal([]byte(raw)); err == nil || !strings.Contains(err.Error(), "use_previous") {
			t.Fatalf("missing explicit data flow accepted: %s / %v", raw, err)
		}
	}
}

func TestAgentProposalAcceptsStructuredAdapterInstruction(t *testing.T) {
	const instruction = `{"schema":"example.request.v1","action":"inspect","id":9007199254740993}`
	proposal, err := decodeAgentProposal([]byte(`{"version":1,"summary":"Inspect the project.","steps":[{"id":"inspect","provider":"adapter","profile":"profile-two","use_previous":false,"instruction":` + instruction + `}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Steps) != 1 || proposal.Steps[0].Instruction != instruction {
		t.Fatalf("structured input changed exact request values: %#v", proposal)
	}
	quoted, _ := json.Marshal(instruction)
	stringProposal, err := decodeAgentProposal([]byte(`{"version":1,"summary":"Inspect the project.","steps":[{"id":"inspect","provider":"adapter","profile":"profile-two","use_previous":false,"instruction":` + string(quoted) + `}]}`))
	if err != nil || !reflect.DeepEqual(stringProposal.Steps[0], proposal.Steps[0]) {
		t.Fatalf("existing quoted contract differs from object form: %#v %v", stringProposal, err)
	}
	plan := validAgentPlanForTest(t)
	plan.Steps = proposal.Steps
	if err := validateAgentPlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Profile = "unapproved"
	if err := validateAgentPlan(plan); err == nil {
		t.Fatal("structured input bypassed profile allowlist")
	}
}

func TestAgentProposalStructuredInstructionsFailClosed(t *testing.T) {
	for _, step := range []string{
		`{"id":"x","provider":"ollama","instruction":{"task":"not text"}}`,
		`{"id":"x","provider":"adapter","instruction":{"action":"inspect"}}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","instruction":[]}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","instruction":null}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","instruction":42}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","instruction":true}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","instruction":{"action":"inspect","action":"delete"}}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","Instruction":{"action":"inspect"}}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","instruction":{"action":"inspect"},"permission":"admin"}`,
		`{"id":"x","provider":"adapter","profile":"profile-two","instruction":{"text":"` + strings.Repeat("a", agentMaximumInstruction) + `"}}`,
	} {
		step = strings.Replace(step, `"id":"x"`, `"id":"x","use_previous":false`, 1)
		if _, err := decodeAgentProposal([]byte(`{"version":1,"summary":"x","steps":[` + step + `]}`)); err == nil {
			t.Fatalf("unsafe proposal accepted: %.150s", step)
		}
	}
}

func TestAgentSavedPlanDoesNotNormalizeStructuredInstructions(t *testing.T) {
	plan := validAgentPlanForTest(t)
	raw, _ := json.Marshal(plan)
	quoted, _ := json.Marshal(plan.Steps[0].Instruction)
	raw = []byte(strings.Replace(string(raw), string(quoted), plan.Steps[0].Instruction, 1))
	if _, err := decodeAgentPlan(raw); err == nil {
		t.Fatal("stored hash-approved plans must keep the existing string-only representation")
	}
}

func TestAgentStructuredCodeKeepsNewlinesAndStepMetadataSeparate(t *testing.T) {
	const raw = `{"version":1,"summary":"Check source.","steps":[{"id":"check","provider":"adapter","profile":"profile-two","use_previous":false,"instruction":{"schema":"example.request.v1","files":[{"path":"candidate.py","content_utf8":"def f():\n    return 1\n"}]}}]}`
	proposal, err := decodeAgentProposal([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Files []struct {
			Content string `json:"content_utf8"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(proposal.Steps[0].Instruction), &request); err != nil {
		t.Fatal(err)
	}
	if request.Files[0].Content != "def f():\n    return 1\n" || strings.Contains(proposal.Steps[0].Instruction, "use_previous") {
		t.Fatal("source bytes or metadata boundary changed")
	}
	p := validAgentPlanForTest(t).Policy
	for _, prompt := range []string{agentPlannerPrompt(p, "{}"), agentConfiguredPlannerPrompt(p, "{}")} {
		if !strings.Contains(prompt, "prefer a nested object") || !strings.Contains(prompt, "never inside its request object") {
			t.Fatal("planner lacks object/metadata separation guidance")
		}
	}
}
