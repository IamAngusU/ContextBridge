package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestOpenAIIntegrationIsRedactedAndWritesPrivateEnvWithoutOverwrite(t *testing.T) {
	cfg := config.Config{
		Server: config.Server{Listen: "127.0.0.1:32145", Token: "private-local-token"},
		Routes: map[string]config.Route{"zeta": {Provider: "ollama"}, "default": {Provider: "ollama"}},
	}
	info, err := buildOpenAIIntegration(cfg, filepath.Join(t.TempDir(), "config.yml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.BaseURL != "http://127.0.0.1:32145/openai/v1" || info.Model != "contextbridge:default" || !info.TokenConfigured || info.APIKey != "" {
		t.Fatalf("unexpected redacted integration: %#v", info)
	}
	shown, err := buildOpenAIIntegration(cfg, info.ConfigPath, true)
	if err != nil || shown.APIKey != cfg.Server.Token {
		t.Fatalf("explicit token output failed: %#v %v", shown, err)
	}

	path := filepath.Join(t.TempDir(), ".contextbridge.env")
	if err := writeOpenAIIntegrationEnv(path, info.BaseURL, cfg.Server.Token, info.Model); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, expected := range []string{"OPENAI_BASE_URL=" + info.BaseURL, "OPENAI_API_KEY=" + cfg.Server.Token, "OPENAI_MODEL=" + info.Model} {
		if !strings.Contains(content, expected) {
			t.Fatalf("integration file is missing %q: %s", expected, content)
		}
	}
	if err := writeOpenAIIntegrationEnv(path, info.BaseURL, "replacement", info.Model); err == nil {
		t.Fatal("integration writer overwrote an existing secret file")
	}
	after, _ := os.ReadFile(path)
	if string(after) != content {
		t.Fatal("failed overwrite attempt changed the existing integration file")
	}
}

func TestOpenAIIntegrationRejectsUnsafeEnvValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeOpenAIIntegrationEnv(path, "http://127.0.0.1:32145/openai/v1", "token\nINJECTED=yes", "contextbridge:default"); err == nil {
		t.Fatal("newline-bearing token was written to an env file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unsafe integration created a file: %v", err)
	}
}

func TestMCPIntegrationUsesExactExecutableAndConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	info, err := buildMCPIntegration(configPath)
	if err != nil {
		t.Fatal(err)
	}
	servers, ok := info.Config["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("MCP server map is missing: %#v", info.Config)
	}
	entry, ok := servers["contextbridge"].(map[string]interface{})
	if !ok {
		t.Fatalf("MCP command is missing: %#v", servers)
	}
	command, ok := entry["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		t.Fatalf("MCP command is missing: %#v", entry)
	}
	args, ok := entry["args"].([]string)
	if !ok || len(args) != 4 || args[0] != "mcp" || args[1] != "serve" || args[2] != "--config" || args[3] != configPath {
		t.Fatalf("MCP arguments are wrong: %#v", entry["args"])
	}
}
