package main

import (
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
	args := []string{"local-speech", "--config", configPath, "--label", "Local speech", "--driver", "alva-speech",
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
	if err := adapterSetupCommand([]string{"local-speech", "--config", configPath, "--label", "Local speech", "--driver", "alva-speech",
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

func TestAdapterSetupNeverCreatesCredentialImplicitly(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	credentialPath := filepath.Join(directory, "missing", "speech.token")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	err := adapterSetupCommand([]string{"local-speech", "--config", configPath, "--driver", "alva-speech",
		"--task", "speech_to_text", "--token-file", credentialPath})
	if err == nil {
		t.Fatal("setup created a credential without explicit authorization")
	}
	if _, statErr := os.Stat(credentialPath); !os.IsNotExist(statErr) {
		t.Fatalf("credential file unexpectedly exists: %v", statErr)
	}
}
