package cluster

import (
	"strings"
	"testing"
)

func TestValidateRequirementsRejectsUnsafeRoutingLabels(t *testing.T) {
	relay := &Relay{cfg: RelayConfig{AllowedTasks: []string{"generation"}}}
	valid := Requirements{
		Task:           "generation",
		Provider:       "adapter",
		AdapterProfile: "profile-one",
		Model:          "Adapter Model A",
		Group:          "demo",
		RequiredTags:   []string{"vision"},
		PreferredNodes: []string{"node_abc-123"},
	}
	if err := relay.validateRequirements(valid); err != nil {
		t.Fatalf("valid requirements rejected: %v", err)
	}

	cases := map[string]func(*Requirements){
		"task escape":       func(value *Requirements) { value.Task = "generation\x1b[2J" },
		"model newline":     func(value *Requirements) { value.Model = "model\nspoof" },
		"group whitespace":  func(value *Requirements) { value.Group = " group" },
		"empty tag":         func(value *Requirements) { value.RequiredTags = []string{""} },
		"oversized tag":     func(value *Requirements) { value.RequiredTags = []string{strings.Repeat("t", 81)} },
		"node control":      func(value *Requirements) { value.PreferredNodes = []string{"node\rspoof"} },
		"oversized model":   func(value *Requirements) { value.Model = strings.Repeat("m", 161) },
		"profile newline":   func(value *Requirements) { value.AdapterProfile = "profile-one\nspoof" },
		"oversized profile": func(value *Requirements) { value.AdapterProfile = strings.Repeat("p", 81) },
		"oversized node id": func(value *Requirements) { value.PreferredNodes = []string{strings.Repeat("n", 161)} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := relay.validateRequirements(candidate); err == nil {
				t.Fatal("unsafe routing label was accepted")
			}
		})
	}
	nonAdapter := valid
	nonAdapter.Provider = "ollama"
	if err := relay.validateRequirements(nonAdapter); err == nil {
		t.Fatal("adapter profile was accepted for a non-adapter provider")
	}
	nonAdapter = Requirements{Task: "generation", Provider: "ollama", AdapterFreshSession: true}
	if err := relay.validateRequirements(nonAdapter); err == nil {
		t.Fatal("fresh adapter chat was accepted for a non-adapter provider")
	}
	ephemeralWithoutFresh := valid
	ephemeralWithoutFresh.AdapterEphemeralSession = true
	if err := relay.validateRequirements(ephemeralWithoutFresh); err == nil {
		t.Fatal("ephemeral adapter chat was accepted without fresh-session routing")
	}
}

func TestValidateRequirementsBoundsImageRoutingEvidence(t *testing.T) {
	relay := &Relay{cfg: RelayConfig{AllowedTasks: []string{"generation"}}}
	valid := Requirements{Task: "generation", Provider: "ollama", Vision: true, InputImageCount: 2, InputImageBytes: 1024, InputImageMaxBytes: 768, InputImageMediaTypes: []string{"image/png", "image/jpeg"}}
	if err := relay.validateRequirements(valid); err != nil {
		t.Fatalf("valid image routing evidence rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Requirements){
		"count":     func(value *Requirements) { value.InputImageCount = 13 },
		"bytes":     func(value *Requirements) { value.InputImageBytes = (8 << 20) + 1 },
		"max bytes": func(value *Requirements) { value.InputImageMaxBytes = 1025 },
		"media":     func(value *Requirements) { value.InputImageMediaTypes = []string{"image/svg+xml"} },
		"vision":    func(value *Requirements) { value.Vision = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := relay.validateRequirements(candidate); err == nil {
				t.Fatal("invalid image routing evidence was accepted")
			}
		})
	}
}
