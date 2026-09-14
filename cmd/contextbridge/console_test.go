package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestFetchConsoleStatusUsesReadOnlyAuthenticatedRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/status" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected console request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v0.test","queued":2,"completed":3,"browser":{"connected":true,"active_tabs":1,"tabs":[{"id":7,"profile":"chatgpt","state":"working","current_model":"GPT-5.6 Sol"}]},"metrics":{"jobs_total":4,"jobs_failed":1},"runtime":{"hardware":{"gpus":[{"name":"RTX","utilization_percent":12}]},"engines":{"ollama":{"state":"online","models":[{"name":"qwen3:8b","loaded":true}]},"stopped":{"state":"stopped","models":[{"name":"not-ready","loaded":true}]}}}}`))
	}))
	defer server.Close()
	cfg := config.Config{Server: config.Server{Listen: strings.TrimPrefix(server.URL, "http://"), Token: "test-secret"}}
	status, err := fetchConsoleStatus(context.Background(), server.Client(), cfg)
	if err != nil || requests != 1 {
		t.Fatalf("console request failed: %v; requests=%d", err, requests)
	}
	snapshot := toServiceSnapshot(status)
	if snapshot.Version != "v0.test" || snapshot.Queued != 2 || !snapshot.BrowserConnected || snapshot.Tabs[0].CurrentModel != "GPT-5.6 Sol" || snapshot.Tabs[0].State != "working" || snapshot.GPU != "RTX" ||
		len(snapshot.LocalModels) != 1 || snapshot.LocalModels[0].Name != "qwen3:8b" || !snapshot.LocalModels[0].Loaded {
		t.Fatalf("console snapshot lost status data: %#v", snapshot)
	}
}

func TestFetchConsoleStatusRejectsWrongToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	cfg := config.Config{Server: config.Server{Listen: strings.TrimPrefix(server.URL, "http://"), Token: "wrong"}}
	_, err := fetchConsoleStatus(context.Background(), server.Client(), cfg)
	if !errors.Is(err, errConsoleUnauthorized) {
		t.Fatalf("wrong token should not retry indefinitely: %v", err)
	}
}

func TestAdjacentConfigPathPrefersOwnInstallation(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "contextbridge.exe")
	if adjacentConfigPath(executable) != "" {
		t.Fatal("missing adjacent config was selected")
	}
	configPath := filepath.Join(directory, "config.yml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if adjacentConfigPath(executable) != configPath {
		t.Fatal("the binary did not prefer its adjacent config")
	}
	t.Setenv("CONTEXTBRIDGE_CONFIG", configPath)
	if defaultConfigPath() != configPath {
		t.Fatal("explicit environment override lost precedence")
	}
}
