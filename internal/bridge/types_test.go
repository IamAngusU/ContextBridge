package bridge

import (
	"testing"
	"time"
)

func TestNormalizeDecisionNeverBlocks(t *testing.T) {
	decision := NormalizeDecision([]byte(`{"verdict":"block","flags":["hate"],"confidence":0.9}`), "test", "model", time.Millisecond)
	if decision.Verdict != "review" {
		t.Fatalf("expected review, got %s", decision.Verdict)
	}
}

func TestNormalizeDecisionExtractsJSON(t *testing.T) {
	decision := NormalizeDecision([]byte("result: {\"verdict\":\"allow\",\"flags\":[],\"confidence\":0.8}"), "test", "model", time.Millisecond)
	if decision.Verdict != "allow" {
		t.Fatalf("expected allow, got %s", decision.Verdict)
	}
}
