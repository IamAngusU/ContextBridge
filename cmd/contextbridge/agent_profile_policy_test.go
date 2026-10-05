package main

import (
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func profileAgentConfig() config.Config {
	cfg := config.Config{AdapterProfiles: map[string]config.AdapterProfile{
		"local-check": {Driver: "test"}, "remote-check": {Driver: "test"},
	}}
	cfg.Cluster.Policies.Execution = cluster.ExecutionPolicyConfig{
		Enabled: true, LocalProviders: []string{"ollama"}, RemoteProviders: []string{"adapter"},
		AdapterProfileClassifications: map[string]string{"local-check": "local", "remote-check": "remote"},
		Default:                       cluster.ExecutionPolicyRule{Egress: "any", AllowedProviders: []string{"ollama", "adapter"}},
	}
	cfg.Cluster.Policies.AgentAuthorities = map[string]config.AgentAuthority{"checks": {
		Enabled: true, Planner: config.AgentPlanner{Provider: "ollama", TimeoutSeconds: 30},
		AllowedProviders: []string{"adapter", "ollama"}, AllowedAdapterProfiles: []string{"local-check"},
		Egress: "local_only", MaxSteps: 3, StepTimeoutSeconds: 30, MaxRuntimeSeconds: 120,
	}}
	return cfg
}

func TestAgentNamedPolicyHonorsExactLocalAdapterProfile(t *testing.T) {
	cfg := profileAgentConfig()
	if _, _, err := configuredAgentPolicy(cfg, "checks"); err != nil {
		t.Fatalf("explicitly local adapter rejected: %v", err)
	}
	// A local adapter planner must use its own classification too.
	policy := cfg.Cluster.Policies.AgentAuthorities["checks"]
	policy.Planner.Provider, policy.Planner.AdapterProfile = "adapter", "local-check"
	cfg.Cluster.Policies.AgentAuthorities["checks"] = policy
	if _, _, err := configuredAgentPolicy(cfg, "checks"); err != nil {
		t.Fatalf("explicitly local planner rejected: %v", err)
	}
}

func TestAgentNamedPolicyChecksEveryAdapterProfile(t *testing.T) {
	for _, profile := range []string{"remote-check", "missing", "unclassified"} {
		t.Run(profile, func(t *testing.T) {
			cfg := profileAgentConfig()
			cfg.AdapterProfiles["unclassified"] = config.AdapterProfile{Driver: "test"}
			p := cfg.Cluster.Policies.AgentAuthorities["checks"]
			p.AllowedAdapterProfiles = append(p.AllowedAdapterProfiles, profile)
			cfg.Cluster.Policies.AgentAuthorities["checks"] = p
			if _, _, err := configuredAgentPolicy(cfg, "checks"); err == nil {
				t.Fatalf("local first profile laundered %s", profile)
			}
		})
	}
}

func TestAgentLocalProfileDoesNotBypassTenantDenialOrCriticalGate(t *testing.T) {
	cfg := profileAgentConfig()
	if !agentNeedsConfirmation("critical", cfg, agentStep{Provider: "adapter", Profile: "local-check"}) {
		t.Fatal("local profile bypassed critical confirmation")
	}
	cfg.Cluster.Policies.Execution.Default.Denied = true
	if _, _, err := configuredAgentPolicy(cfg, "checks"); err == nil || !strings.Contains(err.Error(), "execution policy") {
		t.Fatalf("profile override bypassed relay denial: %v", err)
	}
}

func TestAgentRemoteProfileStillRequiresCostAuthority(t *testing.T) {
	cfg := profileAgentConfig()
	p := cfg.Cluster.Policies.AgentAuthorities["checks"]
	p.Egress = "remote_allowed"
	p.AllowedAdapterProfiles = append(p.AllowedAdapterProfiles, "remote-check")
	cfg.Cluster.Policies.AgentAuthorities["checks"] = p
	if _, _, err := configuredAgentPolicy(cfg, "checks"); err == nil || !strings.Contains(err.Error(), "allow_unknown_cost") {
		t.Fatalf("remote adapter gained cost opt-in: %v", err)
	}
	p.AllowUnknownCost = true
	cfg.Cluster.Policies.AgentAuthorities["checks"] = p
	if _, _, err := configuredAgentPolicy(cfg, "checks"); err != nil {
		t.Fatalf("explicit remote authority rejected: %v", err)
	}
}

func TestAgentProfileClassificationMatchesRelayPrecedence(t *testing.T) {
	cfg := profileAgentConfig()
	cfg.Cluster.Policies.Execution.LocalProviders = []string{"ollama", "adapter"}
	cfg.Cluster.Policies.Execution.RemoteProviders = nil
	for _, test := range []struct{ profile, want string }{
		{"local-check", "local"}, {"remote-check", "remote"}, {"", "local"},
	} {
		got, _ := agentTargetPolicy(cfg, "adapter", test.profile)
		if got != test.want {
			t.Fatalf("profile %q: got %q, want %q", test.profile, got, test.want)
		}
	}
	p := cfg.Cluster.Policies.AgentAuthorities["checks"]
	p.AllowedAdapterProfiles = []string{"local-check", "remote-check"}
	cfg.Cluster.Policies.AgentAuthorities["checks"] = p
	if _, _, err := configuredAgentPolicy(cfg, "checks"); err == nil {
		t.Fatal("provider-wide local classification overrode explicit remote profile")
	}
}
