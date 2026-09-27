package main

import (
	"bytes"
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

func TestClusterSubmitInteractiveResolvesFileAndSubmitsOnceAfterConfirmation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/cluster/jobs" {
			http.NotFound(w, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer configured-producer" {
			http.Error(w, "missing credential", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cluster.Job{ID: "guided-job", Status: cluster.JobQueued})
	}))
	defer server.Close()

	configPath, jobPath := guidedSubmitFixture(t, server.URL)
	var output bytes.Buffer
	input := strings.NewReader(jobPath + "\ny\n")
	if err := clusterSubmitCommandWithIO([]string{"--config", configPath, "--interactive", "--wait=false"}, input, &output, true); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("submission requests = %d, want exactly one", requests.Load())
	}
	shown := output.String()
	for _, expected := range []string{
		"ContextBridge guided cluster submission",
		"contract   " + jobPath + "  [prompt]",
		"relay      " + server.URL + "  [config]",
		"credential config token [secret hidden]",
		"Queued: guided-job",
	} {
		if !strings.Contains(shown, expected) {
			t.Fatalf("guided output missing %q: %s", expected, shown)
		}
	}
	if strings.Contains(shown, "configured-producer") {
		t.Fatalf("guided output exposed credential: %s", shown)
	}
}

func TestClusterSubmitInteractiveCancellationAndNonTTYHaveNoSideEffect(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	configPath, jobPath := guidedSubmitFixture(t, server.URL)

	var output bytes.Buffer
	if err := clusterSubmitCommandWithIO([]string{"--config", configPath, "--interactive", "--wait=false"}, strings.NewReader(jobPath+"\nn\n"), &output, true); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || !strings.Contains(output.String(), "No job was submitted") {
		t.Fatalf("cancelled submission had a side effect: requests=%d output=%s", requests.Load(), output.String())
	}

	output.Reset()
	err := clusterSubmitCommandWithIO([]string{"--config", configPath, "--interactive", "--wait=false"}, strings.NewReader(jobPath+"\ny\n"), &output, false)
	if err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("non-TTY guided submission error = %v", err)
	}
	if requests.Load() != 0 || output.Len() != 0 {
		t.Fatalf("non-TTY submission prompted or sent work: requests=%d output=%q", requests.Load(), output.String())
	}
}

func TestGuidedClusterSubmissionRequiresConfiguredCredentialAndSafePaths(t *testing.T) {
	cfg := config.Config{Cluster: config.Cluster{Worker: config.ClusterWorker{RelayURL: "https://relay.example.test/private"}}}
	file := "job.json"
	var output bytes.Buffer
	_, err := guideClusterSubmission(strings.NewReader("y\n"), &output, cfg, &file, true, false, false, "", "", "config", false, map[string]bool{"file": true})
	if err == nil || !strings.Contains(err.Error(), "credential is not configured") {
		t.Fatalf("missing credential error = %v", err)
	}
	if strings.Contains(output.String(), "/private") {
		t.Fatalf("guided output exposed relay path: %s", output.String())
	}
	if _, err := guidedSubmissionPath("unsafe\x1b[2J.json"); err == nil {
		t.Fatal("terminal control in path was accepted")
	}
}

func guidedSubmitFixture(t *testing.T, relayURL string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = ""
	cfg.Cluster.Worker.RelayURL = relayURL
	cfg.Cluster.ClientToken = "configured-producer"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(directory, "job.json")
	if err := os.WriteFile(jobPath, []byte(`{"requirements":{"task":"generation"},"payload":{"prompt":"hello"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return configPath, jobPath
}
