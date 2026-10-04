package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentProposalAcceptsStructuredAdapterInstruction(t *testing.T) {
	const instruction = `{"schema":"example.request.v1","action":"inspect","id":9007199254740993}`
	proposal, err := decodeAgentProposal([]byte(`{"version":1,"summary":"Inspect the project.","steps":[{"id":"inspect","provider":"adapter","profile":"profile-two","instruction":` + instruction + `}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Steps) != 1 || proposal.Steps[0].Instruction != instruction {
		t.Fatalf("structured input changed exact request values: %#v", proposal)
	}
	quoted, _ := json.Marshal(instruction)
	stringProposal, err := decodeAgentProposal([]byte(`{"version":1,"summary":"Inspect the project.","steps":[{"id":"inspect","provider":"adapter","profile":"profile-two","instruction":` + string(quoted) + `}]}`))
	if err != nil || stringProposal.Steps[0] != proposal.Steps[0] {
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
