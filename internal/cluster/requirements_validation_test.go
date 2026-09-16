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
		BrowserProfile: "chatgpt",
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
		"profile newline":   func(value *Requirements) { value.BrowserProfile = "chatgpt\nspoof" },
		"oversized profile": func(value *Requirements) { value.BrowserProfile = strings.Repeat("p", 81) },
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
	nonBrowser := valid
	nonBrowser.Provider = "ollama"
	if err := relay.validateRequirements(nonBrowser); err == nil {
		t.Fatal("browser profile was accepted for a non-browser provider")
	}
	nonBrowser = Requirements{Task: "generation", Provider: "ollama", BrowserFreshChat: true}
	if err := relay.validateRequirements(nonBrowser); err == nil {
		t.Fatal("fresh browser chat was accepted for a non-browser provider")
	}
	ephemeralWithoutFresh := valid
	ephemeralWithoutFresh.BrowserEphemeralChat = true
	if err := relay.validateRequirements(ephemeralWithoutFresh); err == nil {
		t.Fatal("ephemeral browser chat was accepted without fresh-chat routing")
	}
}
