package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestAdapterCLIUsesSecretFileAndSelectedAccountRelay(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	accountTokenPath := filepath.Join(directory, "account.token")
	if err := os.WriteFile(accountTokenPath, []byte("cb_"+strings.Repeat("a", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clusterLoginCommand([]string{"--config", configPath, "--account", "operator", "--relay", "https://relay.example.test", "--token-file", accountTokenPath}); err != nil {
		t.Fatal(err)
	}
	explicit := "cb_" + strings.Repeat("b", 40)
	explicitPath := filepath.Join(directory, "adapter.token.json")
	if err := os.WriteFile(explicitPath, []byte(`{"token":"`+explicit+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	options, positional, err := parseAdapterCLIFlags("adapter list", []string{"--config", configPath, "--account", "operator", "--token-file", explicitPath}, true)
	if err != nil || len(positional) != 0 {
		t.Fatalf("parse options: positional=%v err=%v", positional, err)
	}
	cfg, token, err := adapterClientConfig(options)
	if err != nil {
		t.Fatal(err)
	}
	if token != explicit {
		t.Fatalf("adapter token = %q", token)
	}
	if got := clusterClientBaseURL(cfg); got != "https://relay.example.test" {
		t.Fatalf("adapter relay = %q", got)
	}
}

func TestAdapterCLIRejectsAmbiguousCredentialSources(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(directory, "adapter.token")
	if err := os.WriteFile(tokenPath, []byte("cb_"+strings.Repeat("c", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := adapterClientConfig(adapterCLIOptions{configPath: configPath, token: "cb_" + strings.Repeat("d", 40), tokenFile: tokenPath})
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("ambiguous credentials were accepted: %v", err)
	}
}
