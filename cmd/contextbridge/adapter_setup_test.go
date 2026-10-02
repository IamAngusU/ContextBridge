package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestAdapterSetupCreatesScopedCredentialAndIdempotentConfig(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	credentialPath := filepath.Join(directory, "secrets", "speech.token")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	args := []string{"local-speech", "--config", configPath, "--label", "Local speech", "--driver", "example-speech",
		"--route", "speech_local", "--task", "speech_to_text", "--model", "whisper-large-v3-turbo",
		"--token-file", credentialPath, "--create-token"}
	if err := adapterSetupCommand(args); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if loaded.Routes["speech_local"].Task != "speech_to_text" || loaded.Routes["speech_local"].AdapterProfile != "local-speech" {
		t.Fatalf("unexpected adapter route: %+v", loaded.Routes["speech_local"])
	}
	principal := loaded.Providers.Adapter.Principals["local-speech"]
	if principal.TokenFile != credentialPath || principal.ResolvedToken == "" || len(principal.AllowedProfiles) != 1 || principal.AllowedProfiles[0] != "local-speech" {
		t.Fatalf("unexpected scoped principal: %+v", principal)
	}
	before, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterSetupCommand([]string{"local-speech", "--config", configPath, "--label", "Local speech", "--driver", "example-speech",
		"--route", "speech_local", "--task", "speech_to_text", "--model", "whisper-large-v3-turbo", "--token-file", credentialPath}); err != nil {
		t.Fatalf("idempotent setup failed: %v", err)
	}
	after, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("idempotent setup rotated the adapter credential")
	}
}

func TestAdapterSetupPersistsRemoteClassificationAndTypedOptions(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	credentialPath := filepath.Join(directory, "secrets", "research.token")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	args := []string{"research-online", "--config", configPath, "--label", "Bounded online research", "--driver", "contextbridge-research",
		"--route", "research_online", "--task", "generation", "--classification", "remote",
		"--option", `allowed_actions=["web.read","rss.read"]`, "--option", `max_response_bytes=524288`,
		"--token-file", credentialPath, "--create-token"}
	if err := adapterSetupCommand(args); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Cluster.Policies.Execution.AdapterProfileClassifications["research-online"]; got != "remote" {
		t.Fatalf("classification = %q", got)
	}
	profile := loaded.AdapterProfiles["research-online"]
	encoded, err := json.Marshal(profile.Options)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"allowed_actions":["web.read","rss.read"],"max_response_bytes":524288}` {
		t.Fatalf("options = %s", encoded)
	}
	args = []string{"research-online", "--config", configPath, "--label", "Bounded online research", "--driver", "contextbridge-research",
		"--route", "research_online", "--task", "generation", "--classification", "remote",
		"--option", `allowed_actions=["web.read","rss.read"]`, "--option", `max_response_bytes=524288`,
		"--token-file", credentialPath}
	if err := adapterSetupCommand(args); err != nil {
		t.Fatalf("idempotent typed setup failed: %v", err)
	}
}

func TestAdapterSetupRejectsUnsafeClassificationAndMalformedOptions(t *testing.T) {
	tests := [][]string{
		{"research", "--classification", "trusted", "--option", `allowed_actions=["web.read"]`},
		{"research", "--classification", "remote", "--option", `allowed_actions=[`},
		{"research", "--classification", "remote", "--option", `Allowed=true`, "--option", `allowed=false`},
		{"research", "--classification", "remote", "--option", `bad key=true`},
		{"research", "--classification", "remote", "--option", `naïve=true`},
		{"research", "--classification", "remote", "--option", `nested={"Allowed":true,"allowed":false}`},
		{"research", "--classification", "remote", "--option", `nested={"allowed":true,"allowed":false}`},
	}
	for _, extra := range tests {
		directory := t.TempDir()
		configPath := filepath.Join(directory, "config.yml")
		credentialPath := filepath.Join(directory, "research.token")
		if err := config.Default(configPath); err != nil {
			t.Fatal(err)
		}
		args := append([]string{}, extra...)
		args = append(args, "--config", configPath, "--driver", "contextbridge-research", "--task", "generation", "--token-file", credentialPath, "--create-token")
		if err := adapterSetupCommand(args); err == nil {
			t.Fatalf("unsafe setup was accepted: %v", extra)
		}
		if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
			t.Fatalf("credential survived rejected setup %v: %v", extra, err)
		}
	}
}

func TestAdapterSetupNeverCreatesCredentialImplicitly(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	credentialPath := filepath.Join(directory, "missing", "speech.token")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	err := adapterSetupCommand([]string{"local-speech", "--config", configPath, "--driver", "example-speech",
		"--task", "speech_to_text", "--token-file", credentialPath})
	if err == nil {
		t.Fatal("setup created a credential without explicit authorization")
	}
	if _, statErr := os.Stat(credentialPath); !os.IsNotExist(statErr) {
		t.Fatalf("credential file unexpectedly exists: %v", statErr)
	}
}
