package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestSaveOutputArtifactsVerifiesAndDoesNotOverwrite(t *testing.T) {
	directory := t.TempDir()
	data := []byte("hello")
	digest := sha256.Sum256(data)
	output := &bridge.Output{Artifacts: []bridge.Artifact{{Name: "../result.txt", Size: len(data), SHA256: fmt.Sprintf("%x", digest[:]), DataBase64: base64.StdEncoding.EncodeToString(data)}}}
	paths, references, err := saveOutputArtifacts(output, directory)
	if err != nil || references != 0 || len(paths) != 1 {
		t.Fatalf("artifact save failed: %#v %d %v", paths, references, err)
	}
	if filepath.Dir(paths[0]) != directory {
		t.Fatalf("artifact escaped output directory: %s", paths[0])
	}
	if raw, err := os.ReadFile(paths[0]); err != nil || string(raw) != "hello" {
		t.Fatalf("saved artifact is wrong: %q %v", raw, err)
	}

	output.Artifacts[0].DataBase64 = base64.StdEncoding.EncodeToString(data)
	paths, _, err = saveOutputArtifacts(output, directory)
	if err != nil || len(paths) != 1 || paths[0] == filepath.Join(directory, "result.txt") {
		t.Fatalf("existing artifact was overwritten: %#v %v", paths, err)
	}
}

func TestClusterBaseURLUsesWorkerRelayOnAClient(t *testing.T) {
	cfg := config.Config{}
	cfg.Cluster.Worker.RelayURL = "https://relay.example/contextbridge/"
	if got := clusterBaseURL(cfg); got != "https://relay.example/contextbridge" {
		t.Fatalf("worker relay URL was ignored: %s", got)
	}
	cfg.Cluster.Relay.PublicURL = "https://public.example/base/"
	if got := clusterBaseURL(cfg); got != "https://public.example/base" {
		t.Fatalf("public relay URL did not take precedence: %s", got)
	}
}
