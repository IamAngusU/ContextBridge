package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestClusterTokenCreatePreservesExplicitZeroPriorityCeiling(t *testing.T) {
	const admin = "admin-token"
	var issued struct {
		ProducerLimits cluster.ProducerLimits `json:"producer_limits"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/cluster/tokens" || request.Header.Get("Authorization") != "Bearer "+admin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&issued); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"token":  "cb_scoped_producer_secret",
			"record": map[string]interface{}{"id": "tok_priority", "role": "producer", "subject": "normal-app"},
		})
	}))
	t.Cleanup(server.Close)

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

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = writeEnd
	commandErr := clusterTokenCreateCommand([]string{"--config", configPath, "--role", "producer", "--subject", "normal-app", "--max-priority", "0"})
	_ = writeEnd.Close()
	os.Stdout = previousStdout
	_, _ = io.ReadAll(readEnd)
	_ = readEnd.Close()
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if issued.ProducerLimits.MaxPriority == nil || *issued.ProducerLimits.MaxPriority != 0 {
		t.Fatalf("cluster token command lost explicit zero priority ceiling: %#v", issued.ProducerLimits)
	}
}
