package config

import (
	"path/filepath"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestRelayRetentionDefaultsAreBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	relay := cfg.Cluster.Relay
	if relay.RetentionDays != cluster.DefaultRetentionDays ||
		relay.MaxTerminalJobs != cluster.DefaultMaxTerminalJobs ||
		relay.MaxEvents != cluster.DefaultMaxEvents ||
		relay.MaxTerminalPipelineRuns != cluster.DefaultMaxTerminalPipelineRuns ||
		relay.RetentionSweepSeconds != cluster.DefaultRetentionSweepSeconds {
		t.Fatalf("unexpected relay retention defaults: %#v", relay)
	}
}

func TestRelayRetentionLimitsAreValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	base, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"negative age", func(cfg *Config) { cfg.Cluster.Relay.RetentionDays = -1 }},
		{"excessive age", func(cfg *Config) { cfg.Cluster.Relay.RetentionDays = cluster.MaximumRetentionDays + 1 }},
		{"zero jobs", func(cfg *Config) { cfg.Cluster.Relay.MaxTerminalJobs = 0 }},
		{"excessive jobs", func(cfg *Config) { cfg.Cluster.Relay.MaxTerminalJobs = cluster.MaximumRetainedTerminalJobs + 1 }},
		{"zero events", func(cfg *Config) { cfg.Cluster.Relay.MaxEvents = 0 }},
		{"zero runs", func(cfg *Config) { cfg.Cluster.Relay.MaxTerminalPipelineRuns = 0 }},
		{"short sweep", func(cfg *Config) { cfg.Cluster.Relay.RetentionSweepSeconds = cluster.MinimumRetentionSweepSeconds - 1 }},
		{"long sweep", func(cfg *Config) { cfg.Cluster.Relay.RetentionSweepSeconds = cluster.MaximumRetentionSweepSeconds + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid retention configuration was accepted")
			}
		})
	}
}
