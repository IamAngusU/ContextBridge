package config

import (
	"os"
	"path/filepath"
	"testing"
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
	if len(cfg.Server.Token) < 40 {
		t.Fatal("generated token is too short")
	}
	if _, err := os.Stat(cfg.Storage.Inbox); !os.IsNotExist(err) {
		t.Fatal("loading config should not create the inbox")
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
