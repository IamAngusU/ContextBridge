package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestNamedClusterAccountsKeepCredentialsAndPoolsSeparate(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	authorityPath := filepath.Join(directory, "alice-authority.json")
	if _, err := cluster.CreatePoolAuthorityFile(authorityPath, "alice-pool", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	aliceToken := "cb_" + strings.Repeat("a", 40)
	aliceTokenPath := filepath.Join(directory, "alice-token.txt")
	if err := os.WriteFile(aliceTokenPath, []byte(aliceToken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clusterLoginCommand([]string{"--config", configPath, "--account", "alice", "--token-file", aliceTokenPath, "--pool-authority-file", authorityPath}); err != nil {
		t.Fatal(err)
	}

	bobToken := "cb_" + strings.Repeat("b", 40)
	bobTokenPath := filepath.Join(directory, "bob-token.txt")
	if err := os.WriteFile(bobTokenPath, []byte(bobToken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clusterLoginCommand([]string{"--config", configPath, "--account", "bob", "--token-file", bobTokenPath, "--activate=false"}); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cluster.ActiveAccount != "alice" || len(cfg.Cluster.Accounts) != 2 {
		t.Fatalf("account activation changed unexpectedly: %#v", cfg.Cluster)
	}
	if got := clusterClientToken(cfg, ""); got != aliceToken {
		t.Fatalf("active account token = %q", got)
	}
	cfg.Cluster.Relay.PublicURL = "https://operator.example.test"
	if got := clusterBaseURL(cfg); got != "https://operator.example.test" {
		t.Fatalf("producer account redirected operator relay to %q", got)
	}
	if got := clusterClientBaseURL(cfg); got == clusterBaseURL(cfg) || got != cfg.Cluster.Accounts["alice"].RelayURL {
		t.Fatalf("client relay did not follow the selected account: %q", got)
	}
	authority, err := configuredPoolAuthority(cfg)
	if err != nil || authority == nil || authority.PoolID != "alice-pool" {
		t.Fatalf("active account authority = %#v, %v", authority, err)
	}
	if err := selectClusterAccount(&cfg, "bob"); err != nil {
		t.Fatal(err)
	}
	if got := clusterClientToken(cfg, ""); got != bobToken {
		t.Fatalf("selected account token = %q", got)
	}
	if authority, err := configuredPoolAuthority(cfg); err != nil || authority != nil {
		t.Fatalf("ordinary account inherited another pool authority: %#v, %v", authority, err)
	}
	if err := selectClusterAccount(&cfg, "missing"); err == nil {
		t.Fatal("unknown account selection was accepted")
	}

	if err := clusterAccountCommand([]string{"use", "bob", "--config", configPath}); err != nil {
		t.Fatal(err)
	}
	if err := clusterAccountCommand([]string{"remove", "alice", "--config", configPath}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cluster.ActiveAccount != "bob" || len(loaded.Cluster.Accounts) != 1 {
		t.Fatalf("account update lost isolation: %#v", loaded.Cluster)
	}
	if _, err := os.Stat(authorityPath); err != nil {
		t.Fatalf("account removal deleted customer authority: %v", err)
	}
}

func TestClusterLogoutRemovesOnlyTheSelectedCredential(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	legacyTokenPath := filepath.Join(directory, "legacy-token.txt")
	if err := os.WriteFile(legacyTokenPath, []byte("cb_"+strings.Repeat("l", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clusterLoginCommand([]string{"--config", configPath, "--token-file", legacyTokenPath}); err != nil {
		t.Fatal(err)
	}
	authorityPath := filepath.Join(directory, "alice-authority.json")
	if _, err := cluster.CreatePoolAuthorityFile(authorityPath, "alice-pool", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		tokenPath := filepath.Join(directory, name+"-token.txt")
		if err := os.WriteFile(tokenPath, []byte("cb_"+strings.Repeat(name[:1], 40)), 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"--config", configPath, "--account", name, "--token-file", tokenPath}
		if name == "alice" {
			args = append(args, "--pool-authority-file", authorityPath)
		}
		if name == "bob" {
			args = append(args, "--activate=false")
		}
		if err := clusterLoginCommand(args); err != nil {
			t.Fatal(err)
		}
	}

	if err := clusterLogoutCommand([]string{"--config", configPath}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cluster.ActiveAccount != "" || cfg.Cluster.ClientToken == "" || len(cfg.Cluster.Accounts) != 1 {
		t.Fatalf("active named logout removed the wrong credential: %#v", cfg.Cluster)
	}
	if _, exists := cfg.Cluster.Accounts["bob"]; !exists {
		t.Fatal("unselected account was removed")
	}
	if _, err := os.Stat(authorityPath); err != nil {
		t.Fatalf("logout deleted the customer authority file: %v", err)
	}

	if err := clusterLogoutCommand([]string{"--config", configPath, "--account", "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := clusterLogoutCommand([]string{"--config", configPath}); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cluster.ClientToken != "" || len(cfg.Cluster.Accounts) != 0 {
		t.Fatalf("logout left producer credentials configured: %#v", cfg.Cluster)
	}
	if err := clusterLogoutCommand([]string{"--config", configPath}); err == nil {
		t.Fatal("logout without a configured credential succeeded")
	}
}
