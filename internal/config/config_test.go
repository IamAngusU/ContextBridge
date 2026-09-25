package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if len(cfg.AdapterProfiles) != 0 {
		t.Fatalf("default config must not expose private adapters: %#v", cfg.AdapterProfiles)
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

func TestPortableResourceEngineRejectsCredentialInheritance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Engines["portable-secret"] = Engine{
		Type: "openai_compatible", ResourcePack: "portable.test", Endpoint: "api",
		Model: "model", Capabilities: []string{"text"}, APIKey: "must-not-move",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "resource_pack cannot be combined") {
		t.Fatalf("credential-bearing portable engine was accepted: %v", err)
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

func TestAdapterProfilesUseBoundedNeutralIdentifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdapterProfiles["review-endpoint"] = AdapterProfile{
		Label: "Review endpoint", Driver: "custom-driver", Options: map[string]interface{}{"mode": "bounded"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid out-of-tree adapter profile was rejected: %v", err)
	}
	cfg.AdapterProfiles["unsafe profile"] = AdapterProfile{Driver: "custom-driver"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsafe adapter profile identifier was accepted")
	}
	delete(cfg.AdapterProfiles, "unsafe profile")
	cfg.AdapterProfiles["review-endpoint"] = AdapterProfile{Driver: "../private"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsafe adapter driver identifier was accepted")
	}
}

func TestScopedAdapterPrincipalsAreIndependentBoundedAndRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdapterProfiles["review-endpoint"] = AdapterProfile{Label: "Review endpoint", Driver: "custom-driver"}
	cfg.Routes["review"] = Route{Provider: "adapter", AdapterProfile: "review-endpoint", TimeoutSeconds: 30}
	cfg.Providers.Adapter.Principals = map[string]AdapterPrincipal{
		"reviewer": {Token: strings.Repeat("r", 40), AllowedProfiles: []string{"review-endpoint"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid scoped adapter principal was rejected: %v", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(strings.Repeat("r", 40))) {
		t.Fatal("adapter credential leaked through JSON serialization")
	}

	principal := cfg.Providers.Adapter.Principals["reviewer"]
	principal.AllowedProfiles = []string{"missing"}
	cfg.Providers.Adapter.Principals["reviewer"] = principal
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("unknown adapter profile was accepted: %v", err)
	}
	principal.AllowedProfiles = []string{"review-endpoint"}
	principal.Token = cfg.Server.Token
	cfg.Providers.Adapter.Principals["reviewer"] = principal
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must not reuse server.token") {
		t.Fatalf("operator token reuse was accepted: %v", err)
	}
	principal.Token = "short"
	cfg.Providers.Adapter.Principals["reviewer"] = principal
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("weak adapter credential was accepted: %v", err)
	}
}

func TestScopedAdapterPrincipalsRejectDuplicateCredentialsAndUncoveredRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdapterProfiles["one"] = AdapterProfile{Driver: "custom"}
	cfg.AdapterProfiles["two"] = AdapterProfile{Driver: "custom"}
	cfg.Routes["two"] = Route{Provider: "adapter", AdapterProfile: "two", TimeoutSeconds: 30}
	shared := strings.Repeat("s", 40)
	cfg.Providers.Adapter.Principals = map[string]AdapterPrincipal{
		"one": {Token: shared, AllowedProfiles: []string{"one"}},
		"two": {Token: shared, AllowedProfiles: []string{"two"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must not share") {
		t.Fatalf("duplicate adapter credentials were accepted: %v", err)
	}
	delete(cfg.Providers.Adapter.Principals, "two")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "has no scoped adapter principal") {
		t.Fatalf("uncovered adapter route was accepted: %v", err)
	}
	cfg.Providers.Adapter.AuthMode = "dual"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit migration mode should allow an uncovered legacy route: %v", err)
	}
}

func TestScopedAdapterRoutesRequireProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Routes["unscoped"] = Route{Provider: "adapter", TimeoutSeconds: 30}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires adapter_profile") {
		t.Fatalf("unprofiled scoped adapter route was accepted: %v", err)
	}
	cfg.Providers.Adapter.AuthMode = "dual"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit legacy migration mode rejected an unprofiled route: %v", err)
	}
}

func TestModelRevisionMustBeImmutableCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	model := cfg.Models["nuextract3"]
	model.Revision = "main"
	cfg.Models["nuextract3"] = model
	if err := cfg.Validate(); err == nil {
		t.Fatal("mutable model revision was accepted")
	}
	model.Revision = strings.Repeat("a", 40)
	cfg.Models["nuextract3"] = model
	if err := cfg.Validate(); err != nil {
		t.Fatalf("immutable model commit was rejected: %v", err)
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

func TestConfigRejectsRemoteWorkerLocalURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Worker.LocalURL = "https://worker.example.com:32145"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "cluster.worker.local_url") {
		t.Fatalf("remote worker local URL returned %v", err)
	}
	cfg.Cluster.Worker.LocalURL = "http://[::1]:32145"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback worker local URL was rejected: %v", err)
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

func TestConfigRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field": `version: 1
server:
  listen: 127.0.0.1:32145
  token: strong-token-value-for-tests
routes:
  default:
    provider: ollama
cluster:
  policies:
    require_tenants: true
`,
		"multiple documents": `version: 1
server:
  listen: 127.0.0.1:32145
  token: strong-token-value-for-tests
routes:
  default:
    provider: ollama
---
version: 1
`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("ambiguous config was accepted")
			}
		})
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

func TestRejectsNegativeAndOverflowProneRuntimeLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	base, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"negative refresh", func(cfg *Config) { cfg.Runtime.HardwareRefreshSeconds = -1 }},
		{"route duration overflow", func(cfg *Config) {
			route := cfg.Routes["default"]
			route.TimeoutSeconds = int(^uint(0) >> 1)
			cfg.Routes["default"] = route
		}},
		{"negative adapter lease", func(cfg *Config) { cfg.Providers.Adapter.LeaseSeconds = -1 }},
		{"worker heartbeat overflow", func(cfg *Config) { cfg.Cluster.Worker.HeartbeatSeconds = int(^uint(0) >> 1) }},
		{"negative queue", func(cfg *Config) { cfg.Cluster.Relay.MaxQueue = -1 }},
		{"pairing ttl overflow", func(cfg *Config) { cfg.Cluster.Relay.PairingTTLSeconds = int(^uint(0) >> 1) }},
		{"negative job runtime", func(cfg *Config) { cfg.Cluster.Policies.MaxJobRuntime = -1 }},
		{"pipeline runtime overflow", func(cfg *Config) {
			cfg.Cluster.Pipelines["overflow"] = cluster.Pipeline{MaxRuntimeSeconds: int(^uint(0) >> 1), Steps: []cluster.PipelineStep{{Name: "step", Requirements: cluster.Requirements{Task: "generation"}, Input: "${input}"}}}
		}},
		{"pipeline step timeout overflow", func(cfg *Config) {
			cfg.Cluster.Pipelines["overflow"] = cluster.Pipeline{Steps: []cluster.PipelineStep{{Name: "step", TimeoutSeconds: int(^uint(0) >> 1), Requirements: cluster.Requirements{Task: "generation"}, Input: "${input}"}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Routes = make(map[string]Route, len(base.Routes))
			for name, route := range base.Routes {
				candidate.Routes[name] = route
			}
			candidate.Cluster.Pipelines = make(map[string]cluster.Pipeline, len(base.Cluster.Pipelines))
			for name, pipeline := range base.Cluster.Pipelines {
				candidate.Cluster.Pipelines[name] = pipeline
			}
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("unsafe numeric boundary was accepted")
			}
		})
	}
}

func TestAgentAuthorityValidationFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	base, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	valid := AgentAuthority{
		Enabled: true, Planner: AgentPlanner{Provider: "ollama", Model: "qwen3:8b", TimeoutSeconds: 120},
		AllowedProviders: []string{"ollama"}, Egress: "local_only", MaxSteps: 2,
		StepTimeoutSeconds: 120, MaxRuntimeSeconds: 300,
	}
	base.Cluster.Policies.AgentAuthorities["project-safe"] = valid
	if err := base.Validate(); err != nil {
		t.Fatalf("valid authority rejected: %v", err)
	}
	tests := []AgentAuthority{
		func() AgentAuthority { value := valid; value.MaxSteps = -1; return value }(),
		func() AgentAuthority { value := valid; value.MaxRuntimeSeconds = int(^uint(0) >> 1); return value }(),
		func() AgentAuthority { value := valid; value.AllowedProviders = []string{"adapter"}; return value }(),
		func() AgentAuthority { value := valid; value.Egress = "anything"; return value }(),
	}
	for index, authority := range tests {
		candidate := base
		candidate.Cluster.Policies.AgentAuthorities = map[string]AgentAuthority{"unsafe": authority}
		if err := candidate.Validate(); err == nil {
			t.Fatalf("unsafe authority %d was accepted", index)
		}
	}
}

func TestWorkerRelayURLRejectsDisguisedRemoteHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Worker.Enabled = true
	cfg.Cluster.Worker.RelayURL = "http://127.0.0.1:32150@evil.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("disguised remote HTTP relay URL was accepted")
	}
}

func TestRelayPublicURLRejectsDisguisedRemoteHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = "http://localhost:32150@evil.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("disguised remote relay public URL was accepted")
	}
}

func TestLANRelayRequiresPrivateListenerAndHTTPSIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	base, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	base.Cluster.Relay.Enabled = true
	base.Cluster.Relay.LAN.Enabled = true
	base.Cluster.Relay.LAN.Listen = "0.0.0.0:32151"
	base.Cluster.Relay.LAN.PublicURL = "https://192.168.1.20:32151"
	if err := base.Validate(); err != nil {
		t.Fatalf("valid private LAN relay rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"public-listener": func(candidate *Config) { candidate.Cluster.Relay.LAN.Listen = "8.8.8.8:32151" },
		"cleartext-url":   func(candidate *Config) { candidate.Cluster.Relay.LAN.PublicURL = "http://192.168.1.20:32151" },
		"missing-cert":    func(candidate *Config) { candidate.Cluster.Relay.LAN.CertificateFile = "" },
		"missing-key":     func(candidate *Config) { candidate.Cluster.Relay.LAN.PrivateKeyFile = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("unsafe LAN relay configuration %q was accepted", name)
			}
		})
	}
}

func TestListenAddressesRequireParsedLoopbackHostAndPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	base, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, listen := range []string{
		"localhost.evil.example:32145",
		"localhost:32145@evil.example",
		"127.0.0.1:not-a-port",
		"127.0.0.1:0",
		"127.0.0.1:65536",
		"0.0.0.0:32145",
	} {
		candidate := base
		candidate.Server.Listen = listen
		if err := candidate.Validate(); err == nil {
			t.Fatalf("unsafe listen address %q was accepted", listen)
		}
	}
	for _, listen := range []string{"127.0.0.1:32145", "localhost:32145", "[::1]:32145"} {
		candidate := base
		candidate.Server.Listen = listen
		if err := candidate.Validate(); err != nil {
			t.Fatalf("safe listen address %q was rejected: %v", listen, err)
		}
	}
}

func TestLoadRejectsOversizedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, make([]byte, maximumConfigBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("oversized config was accepted")
	}
}
