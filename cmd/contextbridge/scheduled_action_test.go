package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestScheduledActionFilesUseStrictJSON(t *testing.T) {
	directory := t.TempDir()
	duplicate := filepath.Join(directory, "duplicate.json")
	if err := os.WriteFile(duplicate, []byte(`{"schema":"contextbridge.scheduled-action-policy.v1","schema":"other","targets":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readScheduledActionPolicy(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate JSON property") {
		t.Fatalf("duplicate policy property was not rejected: %v", err)
	}
	unknown := filepath.Join(directory, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"schema":"contextbridge.scheduled-action-request.v1","adapter_uid":"adp_0123456789abcdef0123456789abcdef","action_kind":"message.text","destination_ref":"dst_0123456789abcdef0123456789abcdef","payload_ref":"ref_0123456789abcdef0123456789abcdef","start_at":"2026-10-03T13:00:00+02:00","timezone":"Europe/Berlin","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readScheduledActionRequest(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request property was not rejected: %v", err)
	}
}

func TestScheduledActionCLIRequiresProducerCredentialAndRejectsIrrelevantFlags(t *testing.T) {
	t.Setenv("CONTEXTBRIDGE_CLUSTER_TOKEN", "")
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.ClientToken = ""
	cfg.Cluster.Relay.AdminToken = "admin-secret-that-must-not-be-used"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scheduledActionCLIConfig(configPath, "", "", ""); err == nil || !strings.Contains(err.Error(), "exact producer credential") {
		t.Fatalf("scheduled action silently fell back to relay admin authority: %v", err)
	}
	if err := clusterScheduledActionCommandWithOutput([]string{"show", "sact_example", "--token", "cb_producer_" + strings.Repeat("x", 32), "--status", "active"}, os.Stdout); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("show accepted a list-only flag: %v", err)
	}
}

func TestScheduledActionIDIsFixedOpaqueIdentifier(t *testing.T) {
	if err := validateScheduledActionID("sact_" + strings.Repeat("a", 32)); err != nil {
		t.Fatalf("valid scheduled action ID was rejected: %v", err)
	}
	for _, invalid := range []string{"sact_example", "sact_" + strings.Repeat("A", 32), "job_" + strings.Repeat("a", 32)} {
		if err := validateScheduledActionID(invalid); err == nil {
			t.Fatalf("invalid scheduled action ID was accepted: %q", invalid)
		}
	}
}
