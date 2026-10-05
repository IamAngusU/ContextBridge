package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestAgentOutputModeProposalPlanAndApproval(t *testing.T) {
	proposal, err := decodeAgentProposal([]byte(`{"version":1,"summary":"structured answer","steps":[{"id":"answer","provider":"ollama","instruction":"Return JSON","use_previous":false,"output_mode":"json"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	plan := validAgentPlanForTest(t)
	plan.Steps = proposal.Steps
	if err := validateAgentPlan(plan); err != nil {
		t.Fatal(err)
	}
	jsonHash, _, _ := encodeAgentPlan(plan)
	_, _, spec, err := agentStepJobInput(config.Config{}, plan.Steps[0], "")
	if err != nil || spec.Mode != "json" || spec.Artifacts || spec.MaxBytes != 256<<10 {
		t.Fatalf("wrong structured contract: %#v %v", spec, err)
	}
	plan.Steps[0].OutputMode = ""
	legacyHash, encoded, _ := encodeAgentPlan(plan)
	if jsonHash == legacyHash || strings.Contains(string(encoded), "output_mode") {
		t.Fatal("format is not approval-bound or changed legacy encoding")
	}
	for _, mode := range []string{"html", "JSON", " json", "artifact"} {
		plan.Steps[0].OutputMode = mode
		if err := validateAgentPlan(plan); err == nil {
			t.Fatalf("unsupported output mode accepted: %s", mode)
		}
	}
	plan.Steps = []agentStep{
		{ID: "answer", Provider: "ollama", Instruction: "Return JSON", OutputMode: "text"},
		{ID: "apply", Provider: "adapter", Profile: "profile-two", Instruction: agentPreviousAdapterJSON, UsePrevious: true},
	}
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("text contradicted exact JSON handoff: %v", err)
	}
	plan.Steps[0].OutputMode = "json"
	if err := validateAgentPlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.Steps[1].OutputMode = "json"
	if err := validateAgentPlan(plan); err == nil {
		t.Fatal("adapter output contract was overridden")
	}
}

func TestAgentOutputJSONEnvelopeCannotBeFakedByText(t *testing.T) {
	step := agentStep{ID: "answer", Provider: "ollama", OutputMode: "json"}
	for _, output := range []*bridge.Output{
		nil, {Mode: "text", Text: `{"ok":true}`}, {Mode: "text", Text: "```json\n{}\n```"},
		{Mode: "text", Text: `{"ok":true}`, Error: " \n"},
		{Mode: "json"}, {Mode: "json", JSON: json.RawMessage(`{}`), Text: "extra"},
		{Mode: "json", JSON: json.RawMessage(`{"ok":true,"ok":false}`)},
		{Mode: "json", JSON: json.RawMessage(`{}`), Error: "failed"},
	} {
		if text, err := agentStepResultText(step, output); err == nil || text != "" {
			t.Fatalf("bad machine evidence accepted: %#v", output)
		}
	}
	text, err := agentStepResultText(step, &bridge.Output{Mode: "json", JSON: json.RawMessage(` {"id":9007199254740993} `)})
	if err != nil || text != `{"id":9007199254740993}` {
		t.Fatalf("exact JSON changed: %s %v", text, err)
	}
	step.OutputMode = ""
	if text, err := agentStepResultText(step, &bridge.Output{Mode: "text", Text: "legacy text"}); err != nil || text != "legacy text" {
		t.Fatal("legacy text changed")
	}
}
