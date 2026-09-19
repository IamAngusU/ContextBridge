package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func validOpenAICompatibleConfig(t *testing.T) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Routes["default"] = Route{Provider: "deepseek"}
	cfg.Engines["deepseek"] = Engine{
		Type: "openai_compatible", URL: "https://api.example.test/v1", Model: "deepseek-v4.1",
		APIKey: "secret-provider-key", Remote: true, Capabilities: []string{"text"},
	}
	return cfg
}

func TestOpenAICompatibleRemoteRequiresExplicitEgressAndSecret(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid explicit remote provider was rejected: %v", err)
	}
	engine := cfg.Engines["deepseek"]
	engine.Remote = false
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "remote: true") {
		t.Fatalf("implicit remote egress was accepted: %v", err)
	}
	engine.Remote = true
	engine.APIKey = "${MISSING_PROVIDER_KEY}"
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "resolved api_key") {
		t.Fatalf("unresolved remote secret was accepted: %v", err)
	}
	engine.APIKey = "secret-provider-key"
	engine.URL = "http://api.example.test/v1"
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("remote plaintext HTTP was accepted: %v", err)
	}
}

func TestOpenAICompatibleSecretIsNotInPublicConfigJSON(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-provider-key") {
		t.Fatal("provider API key leaked through the public config JSON shape")
	}
}
