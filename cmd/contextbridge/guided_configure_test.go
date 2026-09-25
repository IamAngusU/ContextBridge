package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestGuidedConfigureCompletesOnlyUnresolvedValuesAndConfirms(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input := strings.NewReader("worker\nhttps://relay.example.test/secret-path/\ny\n")
	if err := clusterConfigureCommandWithIO([]string{"--config", configPath}, input, &output, true, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cluster.Relay.Enabled || !cfg.Cluster.Worker.Enabled {
		t.Fatalf("guided mode = relay:%v worker:%v, want worker only", cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled)
	}
	if cfg.Cluster.Worker.RelayURL != "https://relay.example.test/secret-path" {
		t.Fatalf("relay URL = %q", cfg.Cluster.Worker.RelayURL)
	}
	shown := output.String()
	for _, expected := range []string{"ContextBridge guided setup", "role       worker  [prompt]", "relay      https://relay.example.test  [prompt]", "Cluster mode saved: worker"} {
		if !strings.Contains(shown, expected) {
			t.Fatalf("guided output missing %q: %s", expected, shown)
		}
	}
	if strings.Contains(shown, "secret-path") {
		t.Fatalf("guided summary exposed URL path: %s", shown)
	}
}

func TestGuidedConfigureUsesEnvironmentAfterEmptyConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXTBRIDGE_RELAY_URL", "https://relay-from-env.example.test")
	var output bytes.Buffer
	if err := clusterConfigureCommandWithIO([]string{"--config", configPath}, strings.NewReader("client\ny\n"), &output, true, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cluster.Worker.RelayURL != "https://relay-from-env.example.test" {
		t.Fatalf("relay URL = %q", cfg.Cluster.Worker.RelayURL)
	}
	if !strings.Contains(output.String(), "[environment]") {
		t.Fatalf("output did not explain environment provenance: %s", output.String())
	}
}

func TestGuidedConfigureCancellationLeavesConfigUntouched(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input := strings.NewReader("worker\nhttps://relay.example.test\nn\n")
	if err := clusterConfigureCommandWithIO([]string{"--config", configPath}, input, &output, true, true); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled guided configuration changed the config")
	}
	if !strings.Contains(output.String(), "Cancelled. No configuration was changed.") {
		t.Fatalf("missing cancellation evidence: %s", output.String())
	}
}

func TestGuidedConfigureNeverPromptsOutsideTerminal(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = clusterConfigureCommandWithIO([]string{"--config", configPath, "--interactive", "--mode", "worker"}, strings.NewReader("https://must-not-be-read.example.test\ny\n"), &output, false, false)
	if err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("non-terminal guided error = %v", err)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("non-terminal guided attempt changed the config")
	}
	if output.Len() != 0 {
		t.Fatalf("non-terminal guided attempt produced prompts: %q", output.String())
	}
}

func TestGuidedConfigureBoundsInvalidAnswers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = clusterConfigureCommandWithIO([]string{"--config", configPath}, strings.NewReader("bad\nwrong\nstill-wrong\n"), &output, true, true)
	if err == nil || !strings.Contains(err.Error(), "three invalid answers") {
		t.Fatalf("invalid-answer error = %v", err)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid guided answers changed the config")
	}
}
