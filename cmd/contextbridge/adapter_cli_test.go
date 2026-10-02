package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
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

func TestAdapterCLIAllowsOptionsAfterFriendlySelector(t *testing.T) {
	options, positional, err := parseAdapterCLIFlags("adapter details", []string{"messaging-primary", "--json", "--account", "operator"}, true)
	if err != nil || len(positional) != 1 || positional[0] != "messaging-primary" || !options.asJSON || options.account != "operator" {
		t.Fatalf("interspersed adapter flags: options=%+v positional=%v err=%v", options, positional, err)
	}
}

func TestAdapterCLIResolvesFriendlySelectorOnlyWhenUnambiguous(t *testing.T) {
	uidA := "adp_" + strings.Repeat("a", 32)
	uidB := "adp_" + strings.Repeat("b", 32)
	items := []cluster.AdapterPresence{
		{AdapterUID: uidA, AdapterID: "messaging", InstanceID: "messaging-primary"},
		{AdapterUID: uidA, AdapterID: "messaging", InstanceID: "messaging-secondary"},
	}
	for _, selector := range []string{"messaging", "MESSAGING-PRIMARY"} {
		uid, err := resolveAdapterUIDFromItems(selector, items)
		if err != nil || uid != uidA {
			t.Fatalf("selector %q = %q, %v", selector, uid, err)
		}
	}
	if _, err := resolveAdapterUIDFromItems("missing", items); err == nil || !strings.Contains(err.Error(), "no visible leased adapter") {
		t.Fatalf("missing selector = %v", err)
	}
	items = append(items, cluster.AdapterPresence{AdapterUID: uidB, AdapterID: "support", InstanceID: "messaging-primary"})
	if _, err := resolveAdapterUIDFromItems("messaging-primary", items); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous selector = %v", err)
	}
}

func TestAdapterCLIAcceptsOnlyCanonicalStableUIDShape(t *testing.T) {
	valid := "adp_" + strings.Repeat("0a", 16)
	for value, want := range map[string]bool{
		valid: true, "ADP_" + strings.Repeat("0a", 16): false,
		"adp_" + strings.Repeat("z", 32): false, "adp_" + strings.Repeat("a", 31): false,
	} {
		if got := looksLikeAdapterUID(value); got != want {
			t.Fatalf("looksLikeAdapterUID(%q) = %v, want %v", value, got, want)
		}
	}
}
