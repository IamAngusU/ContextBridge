package config

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestManagedConfigRedactsValidatesAndAtomicallyApplies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	before, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := ReadManagedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(managed.YAML, before.Server.Token) || strings.Contains(managed.YAML, before.Cluster.Relay.AdminToken) {
		t.Fatal("managed config leaked a credential")
	}
	if strings.Count(managed.YAML, ManagedSecretMarker) < 2 {
		t.Fatalf("managed config did not mark configured secrets: %s", managed.YAML)
	}
	proposed := strings.Replace(managed.YAML, "style: panel", "style: classic", 1)
	validated, err := ValidateManagedConfig(path, []byte(proposed), managed.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !validated.Valid || !validated.RestartRequired || !containsString(validated.ChangedSections, "terminal") {
		t.Fatalf("unexpected validation: %#v", validated)
	}
	unchanged, err := Load(path)
	if err != nil || unchanged.Terminal.Style != "panel" {
		t.Fatalf("dry-run changed config: style=%q err=%v", unchanged.Terminal.Style, err)
	}
	applied, err := ApplyManagedConfig(path, []byte(proposed), managed.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.RestartRequired || applied.Revision == managed.Revision {
		t.Fatalf("unexpected apply response: %#v", applied)
	}
	after, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Terminal.Style != "classic" || after.Server.Token != before.Server.Token || after.Cluster.Relay.AdminToken != before.Cluster.Relay.AdminToken {
		t.Fatalf("apply did not preserve credentials and update style: %#v", after)
	}
	if _, err := ApplyManagedConfig(path, []byte(proposed), managed.Revision); !errors.Is(err, ErrManagedConfigConflict) {
		t.Fatalf("stale revision was accepted: %v", err)
	}
}

func TestManagedConfigRejectsMissingSecretAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	managed, err := ReadManagedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	missing := strings.Replace(managed.YAML, "token: "+ManagedSecretMarker, "", 1)
	missing += "\nunknown_management_field: true\n"
	if _, err := ValidateManagedConfig(path, []byte(missing), managed.Revision); err == nil || !strings.Contains(err.Error(), "unknown_management_field") {
		t.Fatalf("unknown field was accepted: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	removed := false
	for index, line := range lines {
		if !removed && strings.HasPrefix(line, "  token:") {
			lines = append(lines[:index], lines[index+1:]...)
			removed = true
			break
		}
	}
	withoutServerToken := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(withoutServerToken), 0600); err != nil {
		t.Fatal(err)
	}
	// A marker can only refer to a secret that existed in the current revision.
	revision := managedRevision([]byte(withoutServerToken))
	proposed := strings.Replace(withoutServerToken, "server:\n", "server:\n  token: "+ManagedSecretMarker+"\n", 1)
	if _, err := ValidateManagedConfig(path, []byte(proposed), revision); err == nil || !strings.Contains(err.Error(), "has no existing secret") {
		t.Fatalf("orphan secret marker was accepted: %v", err)
	}
}

func TestConfigRejectsOutOfRangeModelDimensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Models["invalid-dimensions"] = Model{Repository: "owner/model", File: "model.gguf", Dimensions: 32769}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("out-of-range model dimensions were accepted: %v", err)
	}
}

func TestConfigRejectsUnsafePipelineRequirements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := cluster.Pipeline{Steps: []cluster.PipelineStep{{Name: "unsafe", Requirements: cluster.Requirements{Task: "generation"}}}}
	tests := []struct {
		name   string
		mutate func(*cluster.Requirements)
		match  string
	}{
		{"image count", func(requirements *cluster.Requirements) { requirements.InputImageCount = 13 }, "input_image_count"},
		{"VRAM overflow", func(requirements *cluster.Requirements) {
			requirements.MinFreeVRAM = cluster.MaximumNodeHardwareBytes + 1
		}, "min_free_vram_bytes"},
		{"non-finite cost", func(requirements *cluster.Requirements) { requirements.MaxCostUSD = math.Inf(1) }, "max_cost_usd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cfg
			candidatePipeline := pipeline
			candidatePipeline.Steps = append([]cluster.PipelineStep(nil), pipeline.Steps...)
			requirements := &candidatePipeline.Steps[0].Requirements
			test.mutate(requirements)
			candidate.Cluster.Pipelines = map[string]cluster.Pipeline{"unsafe": candidatePipeline}
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("unsafe pipeline requirements were accepted: %v", err)
			}
		})
	}
}

func TestManagedConfigRedactsEveryCredentialPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	raw := `server:
  token: local-secret-value
engines:
  remote:
    api_key: engine-secret-value
providers:
  adapter:
    principals:
      helper:
        token: adapter-secret-value
cluster:
  client_token: client-secret-value
  relay:
    admin_token: admin-secret-value
  worker:
    local_token: worker-secret-value
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	managed, err := ReadManagedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"local-secret-value", "engine-secret-value", "adapter-secret-value", "client-secret-value", "admin-secret-value", "worker-secret-value"} {
		if strings.Contains(managed.YAML, secret) {
			t.Fatalf("managed config leaked %q: %s", secret, managed.YAML)
		}
	}
	if strings.Count(managed.YAML, ManagedSecretMarker) != 6 {
		t.Fatalf("unexpected redaction coverage: %s", managed.YAML)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
