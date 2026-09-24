package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func validAgentPlanForTest(t *testing.T) agentPlan {
	t.Helper()
	policy, err := newAgentPolicy("ollama,adapter", "profile-two", 3, 120, 600)
	if err != nil {
		t.Fatal(err)
	}
	return agentPlan{
		Version:           agentPlanVersion,
		AuthorizationMode: agentAuthorizationManual,
		Goal:              "Compare two short answers.",
		Summary:           "Draft locally, then review in Profile Two.",
		Policy:            policy,
		Binding:           validAgentBindingForTest(),
		Evidence: agentPlannerEvidence{
			Provider: "deepseek", JobID: "job-planner", NodeID: "node-one", CostStatus: "upper_bound",
		},
		Steps: []agentStep{
			{ID: "draft", Provider: "ollama", Instruction: "Produce a concise draft."},
			{ID: "review", Provider: "adapter", Profile: "profile-two", Instruction: "Review the submitted draft for factual errors.", UsePrevious: true},
		},
	}
}

func validAgentBindingForTest() agentExecutionBinding {
	digest := "sha256:" + strings.Repeat("a", 64)
	return agentExecutionBinding{
		Version: agentBindingVersion, Scope: agentBindingScope, ConfigSHA256: digest,
		ExecutionSHA256: digest, RelayURL: "https://relay.example.test",
		Components: agentBindingComponentDigests{
			Routes: digest, Providers: digest, Engines: digest, Models: digest,
			AdapterProfiles: digest, PortableResources: digest, ClusterExecution: digest, RAG: digest,
		},
	}
}

func TestAgentPlanBindsAndValidatesExplicitPolicy(t *testing.T) {
	plan := validAgentPlanForTest(t)
	if err := validateAgentPlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.Steps[1].Profile = "profile-one"
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "explicitly approved") {
		t.Fatalf("unapproved adapter profile was not rejected: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Steps[0].Provider = "deepseek"
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "outside the approved policy") {
		t.Fatalf("unapproved provider was not rejected: %v", err)
	}
}

func TestAgentPlanRejectsUnsafeShape(t *testing.T) {
	plan := validAgentPlanForTest(t)
	plan.Version--
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "create and review a new plan") {
		t.Fatalf("old plan version did not fail with regeneration guidance: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.AuthorizationMode = ""
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "authorization mode") {
		t.Fatalf("missing authorization mode was not rejected: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Steps[0].UsePrevious = true
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "first agent step") {
		t.Fatalf("first-step previous result was not rejected: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Steps[1].ID = plan.Steps[0].ID
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate step ID was not rejected: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Steps[0].Instruction = strings.Repeat("x", agentMaximumInstruction+1)
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized instruction was not rejected: %v", err)
	}
}

func TestAgentAggregateCostBudgetConsumesPlannerAndStepReservations(t *testing.T) {
	cfg := config.Config{
		Engines: map[string]config.Engine{"deepseek": {
			Type: "openai_compatible", Remote: true, Model: "deepseek-v4.1",
			Costing: config.EngineCosting{Mode: "upper_bound"},
		}},
	}
	policy, err := newAgentPolicy("deepseek", "", 2, 120, 300)
	if err != nil {
		t.Fatal(err)
	}
	policy.MaxCostUSD = 1
	budget, err := newAgentRunBudget(cfg, policy, agentPlannerEvidence{Provider: "deepseek", ReservedCostUSD: .2})
	if err != nil {
		t.Fatal(err)
	}

	first := agentRequirements(cfg, policy, "deepseek", "")
	if err := budget.authorize("deepseek", &first); err != nil || math.Abs(first.MaxCostUSD-.8) > 1e-9 {
		t.Fatalf("first step did not receive the post-planner remainder: %#v %v", first, err)
	}
	if err := budget.consume("deepseek", cluster.Usage{ReservedCostUSD: .6}); err != nil {
		t.Fatal(err)
	}
	second := agentRequirements(cfg, policy, "deepseek", "")
	if err := budget.authorize("deepseek", &second); err != nil || math.Abs(second.MaxCostUSD-.2) > 1e-9 {
		t.Fatalf("second step received duplicated authority: %#v %v", second, err)
	}
	if err := budget.consume("deepseek", cluster.Usage{ReservedCostUSD: .6}); err == nil || !strings.Contains(err.Error(), "remaining aggregate authority") {
		t.Fatalf("aggregate over-reservation was accepted: %v", err)
	}
	if math.Abs(budget.remaining-.2) > 1e-9 {
		t.Fatalf("rejected reservation mutated remaining authority: %.9f", budget.remaining)
	}
}

func TestAgentAggregateCostBudgetLeavesUnknownCostOptInUnchanged(t *testing.T) {
	cfg := config.Config{Engines: map[string]config.Engine{"remote-unknown": {Type: "openai_compatible", Remote: true, Model: "unknown"}}}
	policy, err := newAgentPolicy("remote-unknown", "", 1, 120, 300)
	if err != nil {
		t.Fatal(err)
	}
	policy.AllowUnknownCost = true
	budget, err := newAgentRunBudget(cfg, policy, agentPlannerEvidence{Provider: "remote-unknown"})
	if err != nil {
		t.Fatal(err)
	}
	requirements := agentRequirements(cfg, policy, "remote-unknown", "")
	if err := budget.authorize("remote-unknown", &requirements); err != nil || requirements.MaxCostUSD != 0 {
		t.Fatalf("unknown-cost opt-in gained a fake numeric budget: %#v %v", requirements, err)
	}
}

func TestAgentAggregateCostBudgetDoesNotInventAuthorityForManualPlans(t *testing.T) {
	cfg := config.Config{Engines: map[string]config.Engine{"deepseek": {
		Type: "openai_compatible", Remote: true, Model: "deepseek-v4.1",
		Costing: config.EngineCosting{Mode: "upper_bound"},
	}}}
	policy, err := newAgentPolicy("deepseek", "", 1, 120, 300)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := newAgentRunBudget(cfg, policy, agentPlannerEvidence{Provider: "deepseek", ReservedCostUSD: .2})
	if err != nil {
		t.Fatal(err)
	}
	requirements := agentRequirements(cfg, policy, "deepseek", "")
	if err := budget.authorize("deepseek", &requirements); err != nil || requirements.MaxCostUSD != 0 {
		t.Fatalf("manual plan gained numeric authority: %#v %v", requirements, err)
	}
}

func TestLocalAutoAgentPlanIsStrictlyOllamaOnly(t *testing.T) {
	plan := validAgentPlanForTest(t)
	policy, err := newAgentPolicy("ollama", "", agentMaximumAutoSteps, 120, 600)
	if err != nil {
		t.Fatal(err)
	}
	plan.AuthorizationMode = agentAuthorizationLocal
	policy.Egress = "local_only"
	policy.AllowUnknownCost = true
	plan.Policy = policy
	plan.Evidence.Provider = "ollama"
	plan.Evidence.Profile = ""
	plan.Steps = []agentStep{{ID: "answer", Provider: "ollama", Instruction: "Answer concisely."}}
	if err := validateAgentPlan(plan); err != nil {
		t.Fatalf("valid local-only automatic plan was rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*agentPlan)
		want   string
	}{
		{"remote planner", func(p *agentPlan) { p.Evidence.Provider = "deepseek" }, "planner"},
		{"adapter allowlist", func(p *agentPlan) { p.Policy.AllowedProviders = []string{"adapter", "ollama"} }, "exactly ollama"},
		{"adapter profile", func(p *agentPlan) { p.Policy.AllowedAdapterProfiles = []string{"profile-two"} }, "requires adapter"},
		{"remote step", func(p *agentPlan) { p.Steps[0].Provider = "deepseek" }, "outside the approved policy"},
		{"too many authorized steps", func(p *agentPlan) { p.Policy.MaxSteps = agentMaximumAutoSteps + 1 }, "at most"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := plan
			candidate.Policy.AllowedProviders = append([]string(nil), plan.Policy.AllowedProviders...)
			candidate.Policy.AllowedAdapterProfiles = append([]string(nil), plan.Policy.AllowedAdapterProfiles...)
			candidate.Steps = append([]agentStep(nil), plan.Steps...)
			test.mutate(&candidate)
			if err := validateAgentPlan(candidate); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("unsafe automatic plan was not rejected with %q: %v", test.want, err)
			}
		})
	}
}

func TestAgentProposalRejectsPolicyAndTrailingJSON(t *testing.T) {
	withPolicy := []byte(`{"version":1,"summary":"x","steps":[],"policy":{"max_steps":6}}`)
	if _, err := decodeAgentProposal(withPolicy); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("planner-controlled policy was not rejected: %v", err)
	}
	trailing := []byte(`{"version":1,"summary":"x","steps":[]} {"second":true}`)
	if _, err := decodeAgentProposal(trailing); err == nil || !strings.Contains(err.Error(), "multiple JSON") {
		t.Fatalf("second JSON value was not rejected: %v", err)
	}
}

func TestAgentPlanDigestCoversPolicyAndNormalizesWhitespace(t *testing.T) {
	plan := validAgentPlanForTest(t)
	digest, raw, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAgentPlan(append([]byte(" \n"), append(raw, []byte("\n ")...)...))
	if err != nil {
		t.Fatal(err)
	}
	digestAgain, _, err := encodeAgentPlan(decoded)
	if err != nil || digestAgain != digest {
		t.Fatalf("stable plan digest mismatch: %s / %s / %v", digest, digestAgain, err)
	}
	decoded.Policy.MaxRuntimeSeconds++
	changed, _, err := encodeAgentPlan(decoded)
	if err != nil || changed == digest {
		t.Fatalf("policy change did not alter approval digest: %s / %s / %v", digest, changed, err)
	}
}

func TestAgentPlanDigestCoversExecutionBinding(t *testing.T) {
	plan := validAgentPlanForTest(t)
	digest, _, err := encodeAgentPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Binding.RelayURL = "https://other-relay.example.test"
	changed, _, err := encodeAgentPlan(plan)
	if err != nil || changed == digest {
		t.Fatalf("execution binding change did not alter approval digest: %s / %s / %v", digest, changed, err)
	}
	plan = validAgentPlanForTest(t)
	plan.Binding.ConfigSHA256 = "sha256:not-a-digest"
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "config digest") {
		t.Fatalf("invalid config binding was accepted: %v", err)
	}
	plan = validAgentPlanForTest(t)
	plan.Binding.Components.Routes = "sha256:not-a-digest"
	if err := validateAgentPlan(plan); err == nil || !strings.Contains(err.Error(), "routes component digest") {
		t.Fatalf("invalid component binding was accepted: %v", err)
	}
}

func TestAgentExecutionBindingChangesWithRouteAndRelay(t *testing.T) {
	cfg := config.Config{
		Routes:  map[string]config.Route{"default": {Provider: "ollama", Model: "qwen3:8b"}},
		Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: "https://relay.example.test"}},
	}
	first, err := agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Routes["default"] = config.Route{Provider: "deepseek", Model: "deepseek-chat"}
	routeChanged, err := agentBindingForConfig(cfg)
	if err != nil || routeChanged.ConfigSHA256 == first.ConfigSHA256 || routeChanged.ExecutionSHA256 == first.ExecutionSHA256 || routeChanged.Components.Routes == first.Components.Routes {
		t.Fatalf("route change did not alter config binding: %#v / %#v / %v", first, routeChanged, err)
	}
	cfg.Cluster.Relay.PublicURL = "https://other-relay.example.test/"
	relayChanged, err := agentBindingForConfig(cfg)
	if err != nil || relayChanged.RelayURL != "https://other-relay.example.test" {
		t.Fatalf("relay binding was not normalized: %#v / %v", relayChanged, err)
	}
}

func TestAgentExecutionBindingExplainsChangesWithoutWeakeningAlphaGate(t *testing.T) {
	cfg := config.Config{
		Server:  config.Server{Token: "first-secret-value"},
		Routes:  map[string]config.Route{"default": {Provider: "ollama", Model: "qwen3:8b"}},
		Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: "https://relay.example.test"}},
	}
	first, err := agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Token = "second-secret-value"
	operational, err := agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if operational.ConfigSHA256 == first.ConfigSHA256 {
		t.Fatal("full alpha binding did not notice non-execution config change")
	}
	if operational.ExecutionSHA256 != first.ExecutionSHA256 {
		t.Fatal("normalized execution fingerprint changed for server token only")
	}
	summary := agentBindingChangeSummary(first, operational)
	if !strings.Contains(summary, "other effective config") || strings.Contains(summary, "secret") {
		t.Fatalf("binding change summary was not redacted and useful: %q", summary)
	}

	cfg.Routes["default"] = config.Route{Provider: "adapter", AdapterProfile: "profile-two"}
	execution, err := agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	summary = agentBindingChangeSummary(operational, execution)
	if summary != "routes" {
		t.Fatalf("execution change category mismatch: %q", summary)
	}
}

func TestAgentExecutionBindingUsesCredentialRoleNotSecretBytes(t *testing.T) {
	cfg := config.Config{
		Routes: map[string]config.Route{"default": {Provider: "deepseek"}},
		Engines: map[string]config.Engine{"deepseek": {
			Type: "openai_compatible", URL: "https://api.example.test/v1", Model: "deepseek-v4.1",
			CredentialSlot: "deepseek-primary", APIKey: "first-secret-value", Remote: true,
			Capabilities: []string{"text"},
		}},
		Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: "https://relay.example.test"}},
	}
	first, err := agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	engine := cfg.Engines["deepseek"]
	engine.APIKey = "rotated-secret-value"
	cfg.Engines["deepseek"] = engine
	rotated, err := agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ConfigSHA256 != first.ConfigSHA256 || rotated.ExecutionSHA256 != first.ExecutionSHA256 || rotated.Components.Engines != first.Components.Engines {
		t.Fatal("secret rotation inside one credential slot changed public approval binding")
	}

	engine.CredentialSlot = "deepseek-secondary"
	cfg.Engines["deepseek"] = engine
	otherAccount, err := agentBindingForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if otherAccount.ConfigSHA256 == first.ConfigSHA256 || otherAccount.ExecutionSHA256 == first.ExecutionSHA256 || otherAccount.Components.Engines == first.Components.Engines {
		t.Fatal("credential role change did not invalidate the reviewed execution binding")
	}
	if summary := agentBindingChangeSummary(first, otherAccount); summary != "engines" {
		t.Fatalf("credential role change was not attributed to engines: %q", summary)
	}
}

func TestAgentPlannerPromptMakesAuthorityBoundaryExplicit(t *testing.T) {
	policy, err := newAgentPolicy("ollama,adapter", "profile-two", 2, 120, 300)
	if err != nil {
		t.Fatal(err)
	}
	prompt := agentPlannerPrompt(policy)
	for _, required := range []string{"untrusted data", "separate hash approval", `"ollama"`, `"profile-two"`, "shell commands, tools"} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(required)) {
			t.Errorf("planner prompt lacks %q", required)
		}
	}
	var proposal agentPlannerProposal
	if err := json.Unmarshal([]byte(`{"version":1,"summary":"one","steps":[{"id":"draft","provider":"ollama","instruction":"Draft."}]}`), &proposal); err != nil || proposal.Steps[0].Provider != "ollama" {
		t.Fatalf("documented planner schema is not decodable: %#v / %v", proposal, err)
	}
}

func TestAgentAutoPlannerPromptExplainsBoundedImmediateExecution(t *testing.T) {
	policy, err := newAgentPolicy("ollama", "", agentMaximumAutoSteps, 120, 300)
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.ToLower(agentAutoPlannerPrompt(policy))
	for _, required := range []string{"local_only", "ollama", "execute this proposal immediately", "cannot grant or widen", "shell commands, tools", "network access"} {
		if !strings.Contains(prompt, strings.ToLower(required)) {
			t.Errorf("automatic planner prompt lacks %q", required)
		}
	}
	if strings.Contains(prompt, "separate hash approval") {
		t.Fatal("automatic planner prompt incorrectly claims a separate hash approval")
	}
}
