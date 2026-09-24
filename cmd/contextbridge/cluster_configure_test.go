package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestClusterConfigureClientDisablesExecutionAndRetainsReusableIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	identityPath := cfg.Cluster.Worker.IdentityFile
	if err := os.MkdirAll(filepath.Dir(identityPath), 0700); err != nil {
		t.Fatal(err)
	}
	identityMarker := []byte("operator-owned paired identity")
	if err := os.WriteFile(identityPath, identityMarker, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.Enabled = true
	cfg.Cluster.Worker.Enabled = true
	cfg.Cluster.Worker.RelayURL = "https://old-relay.example.test"
	cfg.Cluster.Worker.NodeName = "kept-node-name"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if err := clusterConfigureCommand([]string{
		"--config", configPath,
		"--mode", "sender",
		"--relay-url", "https://relay.example.test/contextbridge/",
	}); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Cluster.Relay.Enabled || configured.Cluster.Worker.Enabled {
		t.Fatalf("sender mode retained an execution role: relay=%v worker=%v", configured.Cluster.Relay.Enabled, configured.Cluster.Worker.Enabled)
	}
	if configured.Cluster.Worker.RelayURL != "https://relay.example.test/contextbridge" {
		t.Fatalf("sender relay URL = %q", configured.Cluster.Worker.RelayURL)
	}
	if configured.Cluster.Worker.NodeName != "kept-node-name" {
		t.Fatalf("sender mode changed the reusable worker name: %q", configured.Cluster.Worker.NodeName)
	}
	if configured.Cluster.Worker.IdentityFile != identityPath {
		t.Fatalf("worker identity path changed: %q", configured.Cluster.Worker.IdentityFile)
	}
	storedIdentity, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(storedIdentity) != string(identityMarker) {
		t.Fatal("switching to sender mode changed the saved worker identity")
	}
}

func TestClusterConfigureClientRequiresSafeRelayAndDoesNotPartiallySave(t *testing.T) {
	for _, test := range []struct {
		name     string
		relayURL string
		want     string
	}{
		{name: "missing", want: "relay-url is required"},
		{name: "remote cleartext", relayURL: "http://relay.example.test", want: "cleartext relay URL"},
		{name: "credentials", relayURL: "https://user:secret@relay.example.test", want: "credentials"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yml")
			if err := config.Default(configPath); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--config", configPath, "--mode", "client"}
			if test.relayURL != "" {
				args = append(args, "--relay-url", test.relayURL)
			}
			err = clusterConfigureCommand(args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("cluster configure error = %v, want substring %q", err, test.want)
			}
			after, readErr := os.ReadFile(configPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(before) {
				t.Fatal("rejected client configuration partially changed the config")
			}
		})
	}
}
