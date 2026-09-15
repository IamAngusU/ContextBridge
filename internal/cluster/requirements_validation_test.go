package cluster

import (
	"strings"
	"testing"
)

func TestValidateRequirementsRejectsUnsafeRoutingLabels(t *testing.T) {
	relay := &Relay{cfg: RelayConfig{AllowedTasks: []string{"generation"}}}
	valid := Requirements{
		Task:           "generation",
		Provider:       "browser",
		Model:          "GPT-5.6 Sol",
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
}
