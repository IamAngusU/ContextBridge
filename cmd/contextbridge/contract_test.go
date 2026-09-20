package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestClusterContractValidateUsesNonMutatingEndpoint(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cluster/contracts/validate" || r.Header.Get("Authorization") != "Bearer contract-token" {
			http.Error(w, "unexpected request", http.StatusForbidden)
			return
		}
		var request cluster.SubmitRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Requirements.Task != "generation" || len(request.Payload) == 0 {
			http.Error(w, "invalid contract", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cluster.ContractValidation{
			ContractVersion: cluster.JobContractV1,
			Valid:           true,
			PayloadMode:     "cleartext",
			MaxAttempts:     3,
		})
	}))
	defer server.Close()

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(directory, "job.json")
	if err := os.WriteFile(jobPath, []byte(`{"requirements":{"task":"generation"},"payload":{"prompt":"hello"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clusterContractCommand([]string{"validate", "--config", configPath, "--file", jobPath, "--token", "contract-token"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("validation endpoint calls = %d", calls.Load())
	}
}

func TestClusterContractValidateRejectsUnknownFieldsLocally(t *testing.T) {
	jobPath := filepath.Join(t.TempDir(), "unknown.json")
	if err := os.WriteFile(jobPath, []byte(`{"requirements":{"task":"generation"},"payload":{},"unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	err := clusterContractCommand([]string{"validate", "--file", jobPath, "--token", "contract-token"})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown contract field was not rejected: %v", err)
	}
}
