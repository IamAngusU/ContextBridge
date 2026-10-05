package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOllamaThinkingRequiresExplicitBoundedEngine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []Engine{
		{Type: "ollama", OllamaThink: true, MaxOutputTokens: 1024},
		{Type: "ollama", Model: "auto", OllamaThink: true, MaxOutputTokens: 1024},
		{Type: "ollama", Model: "fixed", OllamaThink: true},
		{Type: "adapter", Model: "fixed", OllamaThink: true, MaxOutputTokens: 1024},
	} {
		cfg.Engines["thinking-test"] = engine
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ollama_think") {
			t.Fatalf("unsafe or inapplicable thinking configuration accepted: %#v %v", engine, err)
		}
	}
	cfg.Engines["thinking-test"] = Engine{Type: "ollama", Model: "fixed", OllamaThink: true, MaxOutputTokens: 1024}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
