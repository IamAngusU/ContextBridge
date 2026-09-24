package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestClusterNodeDrainAllowsFlagsAfterNodeIDAndUsesAdminToken(t *testing.T) {
	const admin = "admin-token-with-enough-entropy-000000000000"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/cluster/nodes/node-a/drain" {
			http.NotFound(response, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+admin {
			http.Error(response, "wrong token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(response).Encode(cluster.Node{ID: "node-a", Name: "Node A", Connected: true, Draining: true, Capabilities: cluster.Capabilities{Running: 1, MaxConcurrent: 4}})
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = server.URL
	cfg.Cluster.Relay.AdminToken = admin
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "node-drain-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previous }()
	if err := clusterNodeCommand([]string{"drain", "node-a", "--config", configPath, "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := output.Sync(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"draining":true`) || !strings.Contains(string(raw), `"id":"node-a"`) {
		t.Fatalf("unexpected CLI output: %s", raw)
	}
}
