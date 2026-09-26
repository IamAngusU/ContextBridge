package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestOpenAICompatibleRejectsMisleadingManagedLifecycleFlag(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	engine := cfg.Engines["deepseek"]
	engine.AutoStart = true
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "only for ContextBridge-managed llama_cpp") {
		t.Fatalf("external API accepted a misleading auto_start flag: %v", err)
	}
}

func TestIncrementalOutputCapabilityIsExplicitAndEngineScoped(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	engine := cfg.Engines["deepseek"]
	engine.Capabilities = []string{"text", "incremental_output"}
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err != nil {
		t.Fatalf("reviewed OpenAI-compatible incremental output was rejected: %v", err)
	}
	engine.Type = "ollama"
	engine.URL = "http://127.0.0.1:11434"
	engine.Remote = false
	engine.APIKey = ""
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "only by openai_compatible") {
		t.Fatalf("unsupported incremental engine was accepted: %v", err)
	}
}

func TestOpenAICompatibleSecretIsNotInPublicConfigJSON(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	engine := cfg.Engines["deepseek"]
	engine.CredentialSlot = "deepseek-primary"
	cfg.Engines["deepseek"] = engine
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-provider-key") {
		t.Fatal("provider API key leaked through the public config JSON shape")
	}
	if !strings.Contains(string(raw), `"credential_slot":"deepseek-primary"`) {
		t.Fatal("non-secret credential role was missing from the public config JSON shape")
	}
}

func TestOpenAICompatibleCredentialSlotMustBeStableSafeIdentifier(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	engine := cfg.Engines["deepseek"]
	engine.CredentialSlot = "deepseek-primary"
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid credential slot was rejected: %v", err)
	}

	engine.CredentialSlot = "../other-account"
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "credential_slot") {
		t.Fatalf("unsafe credential slot was accepted: %v", err)
	}
}

func TestOpenAICompatibleSecretFileResolvesWithoutPersistingSecret(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	secretDirectory := filepath.Join(directory, "secrets")
	if err := os.MkdirAll(secretDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(secretDirectory, "deepseek.key")
	const secret = "file-only-provider-secret"
	if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Routes["deepseek"] = Route{Provider: "deepseek", Task: "generation"}
	cfg.Engines["deepseek"] = Engine{
		Type: "openai_compatible", URL: "https://api.example.test/v1", Model: "deepseek-v4.1",
		APIKeyFile: filepath.Join("secrets", "deepseek.key"), Remote: true, Capabilities: []string{"text"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	engine := loaded.Engines["deepseek"]
	if engine.EffectiveAPIKey() != secret || engine.ResolvedAPIKey != secret {
		t.Fatal("file-backed provider secret was not resolved")
	}
	public, err := json.Marshal(loaded)
	if err != nil || strings.Contains(string(public), secret) || strings.Contains(string(public), secretPath) {
		t.Fatal("provider secret or secret path leaked through public JSON")
	}
	secondPath := filepath.Join(directory, "saved.yml")
	if err := Save(secondPath, loaded); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), secret) || !strings.Contains(string(saved), "api_key_file: secrets") {
		t.Fatalf("save copied a file secret or lost its reference: %s", saved)
	}
}

func TestOpenAICompatibleSecretFileFailsClosed(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Routes["deepseek"] = Route{Provider: "deepseek", Task: "generation"}
	cfg.Engines["deepseek"] = Engine{
		Type: "openai_compatible", URL: "https://api.example.test/v1", Model: "deepseek-v4.1",
		APIKeyFile: "missing.key", Remote: true, Capabilities: []string{"text"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "api_key_file") {
		t.Fatalf("missing provider secret file was accepted: %v", err)
	}
	secretPath := filepath.Join(directory, "missing.key")
	if err := os.WriteFile(secretPath, []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("multi-line provider secret was accepted: %v", err)
	}
}

func TestOpenAICompatibleSecretFileRejectsAmbiguousAndOversizedSources(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(directory, "deepseek.key")
	if err := os.WriteFile(secretPath, []byte("file-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Routes["deepseek"] = Route{Provider: "deepseek", Task: "generation"}
	cfg.Engines["deepseek"] = Engine{
		Type: "openai_compatible", URL: "https://api.example.test/v1", Model: "deepseek-v4.1",
		APIKey: "inline-secret", APIKeyFile: "deepseek.key", Remote: true, Capabilities: []string{"text"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("ambiguous inline and file-backed secrets were accepted: %v", err)
	}

	engine := cfg.Engines["deepseek"]
	engine.APIKey = ""
	cfg.Engines["deepseek"] = engine
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, make([]byte, maximumProviderSecretBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "1 to") {
		t.Fatalf("oversized provider secret was accepted: %v", err)
	}
}

func TestOpenAICompatibleSecretFileRejectsSymlinkAndLooseUnixPermissions(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(directory, "target.key")
	if err := os.WriteFile(targetPath, []byte("target-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(directory, "linked.key")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlink creation is unavailable on this host: %v", err)
	}
	cfg.Routes["deepseek"] = Route{Provider: "deepseek", Task: "generation"}
	cfg.Engines["deepseek"] = Engine{
		Type: "openai_compatible", URL: "https://api.example.test/v1", Model: "deepseek-v4.1",
		APIKeyFile: "linked.key", Remote: true, Capabilities: []string{"text"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlinked provider secret was accepted: %v", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	engine := cfg.Engines["deepseek"]
	engine.APIKeyFile = "target.key"
	cfg.Engines["deepseek"] = engine
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("world-readable provider secret was accepted: %v", err)
	}
}

func TestOpenAICompatibleBalanceFloorRequiresBoundedReviewedCosting(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	engine := cfg.Engines["deepseek"]
	engine.BalancePath = "/user/balance"
	engine.MinimumBalanceUSD = 5
	engine.MaxOutputTokens = 1024
	engine.ReasoningEffort = "low"
	engine.Costing = EngineCosting{
		Mode: "upper_bound", Source: "official peak pricing", InputPerMillionUSD: 0.30,
		CachedInputPerMillionUSD: 0.006, OutputPerMillionUSD: 1.20,
	}
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid guarded remote engine was rejected: %v", err)
	}

	engine.BalancePath = "https://attacker.example/balance"
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "balance_path") {
		t.Fatalf("cross-origin balance URL was accepted: %v", err)
	}
	engine.BalancePath = "/user/balance"
	engine.Costing.Mode = ""
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "costing") {
		t.Fatalf("unguarded balance floor was accepted: %v", err)
	}
}

func TestEngineImageLimitsRequireVisionAndAcceptBoundedPassport(t *testing.T) {
	cfg := validOpenAICompatibleConfig(t)
	engine := cfg.Engines["deepseek"]
	engine.Capabilities = []string{"text", "vision"}
	engine.ContextWindowTokens = 128000
	engine.MaxInputImages = 4
	engine.MaxImageBytes = 2 << 20
	engine.MaxTotalImageBytes = 6 << 20
	engine.ImageMediaTypes = []string{"image/png", "image/jpeg"}
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid capability passport was rejected: %v", err)
	}
	engine.Capabilities = []string{"text"}
	cfg.Engines["deepseek"] = engine
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("image limits without vision capability were accepted: %v", err)
	}
}
