package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestDefaultConfigLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:32145" {
		t.Fatalf("unexpected listen address: %s", cfg.Server.Listen)
	}
	if profile, ok := cfg.BrowserProfiles["gemini"]; !ok || profile.MatchURL != "https://gemini.google.com/*" {
		t.Fatalf("default Gemini browser profile is missing: %#v", profile)
	}
	if len(cfg.Server.Token) < 40 {
		t.Fatal("generated token is too short")
	}
	if len(cfg.Cluster.Relay.AdminToken) < 40 || cfg.Cluster.Relay.AdminToken == cfg.Server.Token {
		t.Fatal("cluster admin token must be strong and independent")
	}
	if cfg.Updates.DefaultEnabled() || cfg.Updates.Channel != "stable" {
		t.Fatal("automatic stable updates should be disabled by default")
	}
	if cfg.Terminal.Style != "panel" {
		t.Fatalf("unexpected terminal style: %q", cfg.Terminal.Style)
	}
	if _, err := os.Stat(cfg.Storage.Inbox); !os.IsNotExist(err) {
		t.Fatal("loading config should not create the inbox")
	}
}

func TestTerminalStyleIsSelectableAndValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Terminal.Style = "classic"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.Terminal.Style != "classic" {
		t.Fatalf("classic style did not survive round-trip: %q, %v", reloaded.Terminal.Style, err)
	}
	reloaded.Terminal.Style = "unknown"
	if err := reloaded.Validate(); err == nil {
		t.Fatal("unsupported terminal style was accepted")
	}
}

func TestConfigSaveRoundTripCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.Enabled = true
	cfg.Cluster.ClientToken = "cb_producer_secret-value-for-test"
	cfg.Cluster.Pipelines["two_step"] = cluster.Pipeline{Steps: []cluster.PipelineStep{{Name: "first", Requirements: cluster.Requirements{Task: "generation"}, Input: "${input}"}}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Cluster.Relay.Enabled || reloaded.Cluster.ClientToken != cfg.Cluster.ClientToken || len(reloaded.Cluster.Pipelines["two_step"].Steps) != 1 {
		t.Fatal("cluster config did not survive save")
	}
	public, err := json.Marshal(reloaded)
	if err != nil || bytes.Contains(public, []byte(cfg.Cluster.ClientToken)) {
		t.Fatal("client token leaked through the public JSON config shape")
	}
}

func TestRejectsRelayJobLimitAboveSupportedMaximum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.Enabled = true
	cfg.Cluster.Relay.MaxJobBytes = cluster.MaximumJobPayloadBytes + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("relay job limit above the supported maximum was accepted")
	}
}

func TestRejectsPublicListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	raw := []byte("version: 1\nserver:\n  listen: 0.0.0.0:32145\n  token: strong-token-value\nroutes:\n  default:\n    provider: ollama\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected public listen address to be rejected")
	}
}

func TestRejectsUnsafeModelAliasAndFilename(t *testing.T) {
	base := Config{Version: 1, Server: Server{Listen: "127.0.0.1:32145", Token: "strong-token-value-for-tests"}, Routes: map[string]Route{"default": {Provider: "ollama"}}, Models: map[string]Model{"../escape": {Repository: "owner/repo", File: "model.gguf"}}}
	if err := base.Validate(); err == nil {
		t.Fatal("expected unsafe model alias to be rejected")
	}
	base.Models = map[string]Model{"safe": {Repository: "owner/repo", File: "../model.gguf"}}
	if err := base.Validate(); err == nil {
		t.Fatal("expected unsafe model filename to be rejected")
	}
}

func TestRejectsShortTokenAndInvalidDigest(t *testing.T) {
	base := Config{Version: 1, Server: Server{Listen: "127.0.0.1:32145", Token: "short"}, Routes: map[string]Route{"default": {Provider: "ollama"}}}
	if err := base.Validate(); err == nil {
		t.Fatal("expected short token to be rejected")
	}
	base.Server.Token = "strong-token-value-for-tests"
	base.Models = map[string]Model{"model": {Repository: "owner/repo", File: "model.gguf", SHA256: "not-a-digest"}}
	if err := base.Validate(); err == nil {
		t.Fatal("expected invalid digest to be rejected")
	}
}

func TestRejectsReservedRuntimeArguments(t *testing.T) {
	base := Config{
		Version: 1,
		Server:  Server{Listen: "127.0.0.1:32145", Token: "strong-token-value-for-tests"},
		Routes:  map[string]Route{"default": {Provider: "engine"}},
		Engines: map[string]Engine{"engine": {Type: "llama_cpp", Model: "model", Listen: "127.0.0.1:32146", Args: []string{"--host=0.0.0.0"}}},
		Models:  map[string]Model{"model": {Repository: "owner/repo", File: "model.gguf"}},
	}
	if err := base.Validate(); err == nil {
		t.Fatal("expected reserved runtime argument to be rejected")
	}
}
