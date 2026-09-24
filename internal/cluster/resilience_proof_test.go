package cluster

import (
	"strings"
	"testing"
)

func TestRunResilienceProofPassesAllIsolatedDurabilityChecks(t *testing.T) {
	report, err := RunResilienceProof("v-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != ResilienceProofV1 || report.ContextBridgeVersion != "v-test" || !report.Passed || len(report.Checks) != 5 {
		t.Fatalf("unexpected resilience report: %#v", report)
	}
	seen := map[string]bool{}
	for _, check := range report.Checks {
		if !check.Passed || check.ID == "" || check.Detail == "" || seen[check.ID] {
			t.Fatalf("invalid resilience check: %#v", check)
		}
		seen[check.ID] = true
	}
	if !strings.Contains(report.Scope, "no network") || !strings.Contains(report.Scope, "no AI request") {
		t.Fatalf("proof scope is not honest: %q", report.Scope)
	}
}
