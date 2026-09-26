package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestGuidedPairingCompletesOnlyUnresolvedValuesAndRedactsRelayPath(t *testing.T) {
	cfg := config.Config{}
	cfg.Cluster.Worker.IdentityFile = filepath.Join(t.TempDir(), "identity.json")
	cfg.Cluster.Worker.Groups = []string{"home", "gpu"}
	var relayURL, identityFile, name string
	var output bytes.Buffer
	confirmed, err := guidePairing(
		strings.NewReader("https://relay.example.test/private/path/\ny\n"),
		&output,
		cfg,
		&relayURL,
		&identityFile,
		&name,
		map[string]bool{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("guided pairing was not confirmed")
	}
	if relayURL != "https://relay.example.test/private/path" {
		t.Fatalf("relay URL = %q", relayURL)
	}
	shown := output.String()
	for _, expected := range []string{"ContextBridge guided pairing", "relay      https://relay.example.test  [prompt]", "identity   " + cfg.Cluster.Worker.IdentityFile + "  [config]", "groups     2  [config]"} {
		if !strings.Contains(shown, expected) {
			t.Fatalf("guided output missing %q: %s", expected, shown)
		}
	}
	if strings.Contains(shown, "private/path") {
		t.Fatalf("guided summary exposed URL path: %s", shown)
	}
}

func TestGuidedPairingResolutionOrder(t *testing.T) {
	cfg := config.Config{}
	cfg.Cluster.Worker.RelayURL = "https://relay-from-config.example.test"
	cfg.Cluster.Worker.IdentityFile = filepath.Join(t.TempDir(), "identity.json")
	cfg.Cluster.Worker.NodeName = "config-node"
	t.Setenv("CONTEXTBRIDGE_RELAY_URL", "https://relay-from-env.example.test")
	t.Setenv("CONTEXTBRIDGE_NODE_NAME", "env-node")
	relayURL := "https://relay-from-flag.example.test/secret"
	identityFile := filepath.Join(t.TempDir(), "flag-identity.json")
	name := "flag-node"
	var output bytes.Buffer
	confirmed, err := guidePairing(strings.NewReader("y\n"), &output, cfg, &relayURL, &identityFile, &name, map[string]bool{"relay": true, "identity": true, "name": true})
	if err != nil || !confirmed {
		t.Fatalf("guided pairing = confirmed %v, err %v", confirmed, err)
	}
	if relayURL != "https://relay-from-flag.example.test/secret" || name != "flag-node" {
		t.Fatalf("flag values lost: relay=%q name=%q", relayURL, name)
	}
	shown := output.String()
	if strings.Count(shown, "[flag]") != 3 || strings.Contains(shown, "from-config") || strings.Contains(shown, "from-env") || strings.Contains(shown, "/secret") {
		t.Fatalf("unexpected resolution summary: %s", shown)
	}
}

func TestGuidedPairingWarnsBeforeReplacingExistingIdentity(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(identityPath, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Cluster.Worker.IdentityFile = identityPath
	relayURL, identityFile, name := "https://relay.example.test", "", "node"
	var output bytes.Buffer
	confirmed, err := guidePairing(strings.NewReader("n\n"), &output, cfg, &relayURL, &identityFile, &name, map[string]bool{"relay": true, "name": true})
	if err != nil || confirmed {
		t.Fatalf("guided pairing = confirmed %v, err %v", confirmed, err)
	}
	if !strings.Contains(output.String(), "existing regular file; replaced only after relay approval") {
		t.Fatalf("missing replacement warning: %s", output.String())
	}
	raw, readErr := os.ReadFile(identityPath)
	if readErr != nil || string(raw) != "existing" {
		t.Fatalf("cancelled guide changed identity: %q, %v", raw, readErr)
	}
}

func TestPairInteractiveNeverPromptsOutsideTerminal(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(filepath.Dir(configPath), "identity.json")
	var output bytes.Buffer
	err := pairCommandWithIO([]string{"--config", configPath, "--interactive", "--identity", identityPath}, strings.NewReader("https://must-not-be-read.example.test\ny\n"), &output, false)
	if err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("non-terminal guided error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("non-terminal guided attempt produced prompts: %q", output.String())
	}
	if _, statErr := os.Stat(identityPath); !os.IsNotExist(statErr) {
		t.Fatalf("non-terminal attempt created identity: %v", statErr)
	}
}

func TestPairInteractiveCancellationHasNoPairingSideEffect(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(filepath.Dir(configPath), "identity.json")
	var output bytes.Buffer
	err := pairCommandWithIO([]string{"--config", configPath, "--interactive", "--relay", "https://relay.example.test", "--identity", identityPath, "--name", "safe-node"}, strings.NewReader("n\n"), &output, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No pairing request was sent") {
		t.Fatalf("missing cancellation evidence: %s", output.String())
	}
	if _, statErr := os.Stat(identityPath); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled pairing created identity: %v", statErr)
	}
}

func TestPairFullySpecifiedNonInteractiveKeepsDirectLocalBehavior(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.Enabled = true
	cfg.Cluster.Relay.Listen = "127.0.0.1:32150"
	cfg.Cluster.Relay.Database = filepath.Join(filepath.Dir(configPath), "cluster.db")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(filepath.Dir(configPath), "direct-identity.json")
	var output bytes.Buffer
	if err := pairCommandWithIO([]string{"--config", configPath, "--relay", "http://127.0.0.1:32150", "--identity", identityPath, "--name", "direct-node"}, strings.NewReader("must-not-be-read\n"), &output, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Local worker paired directly") || strings.Contains(output.String(), "guided") {
		t.Fatalf("non-interactive output changed: %s", output.String())
	}
	if info, statErr := os.Stat(identityPath); statErr != nil || info.Size() == 0 {
		t.Fatalf("direct pairing did not create identity: info=%v err=%v", info, statErr)
	}
}

func TestGuidedPairingRejectsDisguisedCleartextRemoteRelay(t *testing.T) {
	cfg := config.Config{}
	cfg.Cluster.Worker.IdentityFile = filepath.Join(t.TempDir(), "identity.json")
	relayURL, identityFile, name := "http://localhost@192.0.2.1:32150", "", "node"
	var output bytes.Buffer
	_, err := guidePairing(strings.NewReader("y\n"), &output, cfg, &relayURL, &identityFile, &name, map[string]bool{"relay": true, "name": true})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("unsafe relay error = %v", err)
	}
}

func TestGuidedPairingRejectsTerminalControlInIdentityPath(t *testing.T) {
	cfg := config.Config{}
	relayURL, identityFile, name := "https://relay.example.test", "identity\x1b[2J.json", "node"
	var output bytes.Buffer
	_, err := guidePairing(strings.NewReader("y\n"), &output, cfg, &relayURL, &identityFile, &name, map[string]bool{"relay": true, "identity": true, "name": true})
	if err == nil || !strings.Contains(err.Error(), "printable path") {
		t.Fatalf("unsafe identity error = %v", err)
	}
}
