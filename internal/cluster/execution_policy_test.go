package cluster

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestExecutionPolicyDisabledProducesDurableDecision(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	decision, err := EvaluateExecutionPolicy(ExecutionPolicyConfig{
		LocalProviders: []string{"ollama"}, RemoteProviders: []string{"adapter"},
	}, "private-tenant-name", Requirements{Provider: "adapter", MaxCostUSD: .75}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Schema != PolicyDecisionV1 || decision.Outcome != "allow" || len(decision.ReasonCodes) != 1 || decision.ReasonCodes[0] != PolicyCodeDisabled {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if decision.ProviderClassification != "remote" || decision.EgressClass != "remote" || decision.CostEnforcement != "not_configured" {
		t.Fatalf("unexpected classification: %+v", decision)
	}
	if decision.CostBudgetUSD != .75 {
		t.Fatalf("disabled policy lost the authenticated producer budget: %+v", decision)
	}
	if decision.EvaluatedAt != now || strings.Contains(decision.RuleID, "private-tenant-name") {
		t.Fatalf("decision leaked identity or lost timestamp: %+v", decision)
	}
}

func TestExecutionPolicyTenantEgressProviderAndGroupBoundaries(t *testing.T) {
	cfg := ExecutionPolicyConfig{
		Enabled: true, TenantMode: "listed_only", RequireTenant: true,
		LocalProviders: []string{"ollama"}, RemoteProviders: []string{"adapter", "deepseek"},
		Default: ExecutionPolicyRule{Egress: "local_only", AllowedProviders: []string{"ollama", "adapter"}, AllowedGroups: []string{"private", "shared"}},
		Tenants: map[string]ExecutionPolicyRule{
			"tenant-a": {AllowedProviders: []string{"ollama"}, AllowedGroups: []string{"private"}},
		},
	}
	requirements := Requirements{Provider: "ollama", Group: "private", Egress: "local_only"}
	decision, err := EvaluateExecutionPolicy(cfg, "tenant-a", requirements, time.Now())
	if err != nil || decision.Outcome != "allow" || decision.ProviderClassification != "local" {
		t.Fatalf("expected local allow, decision=%+v err=%v", decision, err)
	}
	if decision.RuleID == "default" || strings.Contains(decision.RuleID, "tenant-a") {
		t.Fatalf("tenant rule ID must be hashed: %q", decision.RuleID)
	}

	cases := []struct {
		name   string
		tenant string
		req    Requirements
		code   string
	}{
		{"tenant required", "", requirements, PolicyCodeTenantRequired},
		{"tenant denied", "tenant-b", requirements, PolicyCodeTenantDenied},
		{"provider denied", "tenant-a", Requirements{Provider: "adapter", Group: "private"}, PolicyCodeProviderDenied},
		{"group denied", "tenant-a", Requirements{Provider: "ollama", Group: "shared"}, PolicyCodeGroupDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EvaluateExecutionPolicy(cfg, tc.tenant, tc.req, time.Now())
			assertPolicyCode(t, err, tc.code)
		})
	}
}

func TestExecutionPolicyTenantRuleCannotRelaxDefault(t *testing.T) {
	cfg := ExecutionPolicyConfig{
		Enabled: true, LocalProviders: []string{"ollama"}, RemoteProviders: []string{"adapter"},
		Default: ExecutionPolicyRule{Egress: "local_only", AllowedProviders: []string{"ollama"}},
		Tenants: map[string]ExecutionPolicyRule{"tenant-a": {Egress: "remote_only", AllowedProviders: []string{"adapter"}}},
	}
	_, err := EvaluateExecutionPolicy(cfg, "tenant-a", Requirements{Provider: "adapter"}, time.Now())
	assertPolicyCode(t, err, PolicyCodeTenantDenied)
}

func TestExecutionPolicyCostBudgetIsNeverUnknownZero(t *testing.T) {
	base := ExecutionPolicyConfig{
		Enabled: true, LocalProviders: []string{"ollama"}, RemoteProviders: []string{"adapter", "deepseek"},
		CostBoundedProviders: []string{"deepseek"},
	}

	local, err := EvaluateExecutionPolicy(base, "", Requirements{Provider: "ollama", MaxCostUSD: 2}, time.Now())
	if err != nil || local.CostEnforcement != "not_applicable_local" {
		t.Fatalf("local budget should be explicit and non-monetized: %+v %v", local, err)
	}
	localRule := base
	localRule.Default.RequireCostBudget = true
	local, err = EvaluateExecutionPolicy(localRule, "", Requirements{Provider: "ollama"}, time.Now())
	if err != nil || local.CostEnforcement != "not_requested" {
		t.Fatalf("remote cost requirement must not invent a local monetary budget: %+v %v", local, err)
	}
	remote, err := EvaluateExecutionPolicy(base, "", Requirements{Provider: "deepseek", MaxCostUSD: .25}, time.Now())
	if err != nil || remote.CostEnforcement != "worker_upper_bound" || remote.CostBudgetUSD != .25 {
		t.Fatalf("reviewed remote upper bound should pass: %+v %v", remote, err)
	}
	_, err = EvaluateExecutionPolicy(base, "", Requirements{Provider: "adapter", MaxCostUSD: .25}, time.Now())
	assertPolicyCode(t, err, PolicyCodeCostUnverifiable)

	required := base
	required.Default.RequireCostBudget = true
	_, err = EvaluateExecutionPolicy(required, "", Requirements{Provider: "deepseek"}, time.Now())
	assertPolicyCode(t, err, PolicyCodeCostRequired)
	required.Default.MaxCostUSD = .10
	_, err = EvaluateExecutionPolicy(required, "", Requirements{Provider: "deepseek", MaxCostUSD: .11}, time.Now())
	assertPolicyCode(t, err, PolicyCodeCostExceeded)
}

func TestExecutionPolicyFingerprintCanonicalAndAuthorizationEquivalent(t *testing.T) {
	first := ExecutionPolicyConfig{Enabled: true, LocalProviders: []string{"ollama", "arsenal"}, RemoteProviders: []string{"deepseek"}, CostBoundedProviders: []string{"deepseek"}}
	second := ExecutionPolicyConfig{Enabled: true, LocalProviders: []string{"arsenal", "ollama"}, RemoteProviders: []string{"deepseek"}, CostBoundedProviders: []string{"deepseek"}}
	a, err := EvaluateExecutionPolicy(first, "", Requirements{Provider: "ollama"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := EvaluateExecutionPolicy(second, "", Requirements{Provider: "ollama"}, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if a.PolicyFingerprint != b.PolicyFingerprint || !a.EquivalentAuthorization(b) {
		t.Fatalf("canonical policies must have equivalent authorization: %+v %+v", a, b)
	}
	b.CostEnforcement = "worker_upper_bound"
	if a.EquivalentAuthorization(b) {
		t.Fatal("changed enforcement must invalidate authorization")
	}
}

func TestExecutionPolicyValidationRejectsAmbiguousOrUnenforceableConfiguration(t *testing.T) {
	cases := []ExecutionPolicyConfig{
		{LocalProviders: []string{"same"}, RemoteProviders: []string{"SAME"}},
		{RemoteProviders: []string{"deepseek"}, CostBoundedProviders: []string{"unknown"}},
		{RemoteProviders: []string{"adapter"}, CostBoundedProviders: []string{"adapter"}},
		{Default: ExecutionPolicyRule{MaxCostUSD: math.NaN()}},
		{Tenants: map[string]ExecutionPolicyRule{"Support": {}, "support": {}}},
	}
	for index, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("case %d should fail", index)
		}
	}
}

func TestExecutionPolicyUnknownEgressFailsClosed(t *testing.T) {
	cfg := ExecutionPolicyConfig{Enabled: true, LocalProviders: []string{"ollama"}, RemoteProviders: []string{"adapter"}}
	_, err := EvaluateExecutionPolicy(cfg, "", Requirements{Provider: "mystery"}, time.Now())
	assertPolicyCode(t, err, PolicyCodeEgressDenied)
	_, err = EvaluateExecutionPolicy(cfg, "", Requirements{}, time.Now())
	assertPolicyCode(t, err, PolicyCodeEgressDenied)
}

func TestPolicyDecisionValidationRejectsForgedOrContradictoryEvidence(t *testing.T) {
	valid, err := EvaluateExecutionPolicy(ExecutionPolicyConfig{
		Enabled: true, LocalProviders: []string{"ollama"}, RemoteProviders: []string{"deepseek"},
		CostBoundedProviders: []string{"deepseek"},
	}, "", Requirements{Provider: "deepseek", MaxCostUSD: .25}, time.Now())
	if err != nil || valid.ValidateAllowed() != nil {
		t.Fatalf("generated decision should validate: %+v %v", valid, err)
	}

	cases := []struct {
		name   string
		mutate func(*PolicyDecision)
	}{
		{"non-hex fingerprint", func(decision *PolicyDecision) { decision.PolicyFingerprint = "sha256:" + strings.Repeat("z", 64) }},
		{"raw tenant rule", func(decision *PolicyDecision) { decision.RuleID = "tenant:customer-name" }},
		{"unknown enabled provider", func(decision *PolicyDecision) {
			decision.ProviderClassification, decision.EgressClass = "unknown", "unknown"
		}},
		{"local upper bound", func(decision *PolicyDecision) {
			decision.ProviderClassification, decision.EgressClass = "local", "local"
		}},
		{"disabled enforcement on enabled decision", func(decision *PolicyDecision) { decision.CostEnforcement = "not_configured" }},
		{"unknown enforcement", func(decision *PolicyDecision) { decision.CostEnforcement = "claimed_by_worker" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := valid
			tc.mutate(&decision)
			if err := decision.ValidateAllowed(); err == nil {
				t.Fatalf("forged decision should fail: %+v", decision)
			}
		})
	}
}

func assertPolicyCode(t *testing.T, err error, expected string) {
	t.Helper()
	var violation *PolicyViolation
	if !errors.As(err, &violation) || violation.Code() != expected {
		t.Fatalf("expected policy code %q, got %v", expected, err)
	}
}
