package config

import (
	"strings"
	"testing"
)

func TestAgentVerificationPins(t *testing.T) {
	valid := map[string]string{"checks-v1": "sha256:" + strings.Repeat("a", 64)}
	for _, value := range []interface{}{nil, "true", []string{"check"}, map[string]string{}, map[string]int{"check": 1}, map[string]string{"../check": valid["checks-v1"]}, map[string]string{"check": "abc"}} {
		profile := AdapterProfile{Options: map[string]interface{}{AdapterAgentInstructionContractOption: "JSON request", AdapterAgentVerificationChecksOption: value}}
		if _, err := AgentVerificationChecks(profile); err == nil {
			t.Fatalf("accepted invalid pins: %#v", value)
		}
	}
	profile := AdapterProfile{Options: map[string]interface{}{AdapterAgentVerificationChecksOption: valid}}
	if _, err := AgentVerificationChecks(profile); err == nil {
		t.Fatal("accepted pins without machine contract")
	}
	profile.Options[AdapterAgentInstructionContractOption] = "JSON request"
	if pins, err := AgentVerificationChecks(profile); err != nil || pins["checks-v1"] != valid["checks-v1"] {
		t.Fatalf("valid pins rejected: %v", err)
	}
	if pins, err := AgentVerificationChecks(AdapterProfile{}); err != nil || len(pins) != 0 {
		t.Fatal("default must stay disabled")
	}
	for i := 0; i < 9; i++ {
		valid[string(rune('a'+i))] = valid["checks-v1"]
	}
	if _, err := AgentVerificationChecks(profile); err == nil {
		t.Fatal("accepted too many checks")
	}
}
