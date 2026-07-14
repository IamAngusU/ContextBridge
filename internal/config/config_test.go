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
