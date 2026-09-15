package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestReadClusterAPIResponseAcceptsExactLimitAndRejectsOneByteMore(t *testing.T) {
	exact := bytes.Repeat([]byte{'x'}, int(maximumClusterAPIResponseBytes))
	read, err := readClusterAPIResponse(bytes.NewReader(exact))
	if err != nil || len(read) != len(exact) {
		t.Fatalf("exact response boundary was rejected: len=%d err=%v", len(read), err)
	}
	if _, err := readClusterAPIResponse(bytes.NewReader(append(exact, 'x'))); err == nil {
		t.Fatal("response one byte over the boundary was accepted")
	}
}

func TestClusterSubmitUsesCompactResponsesForSubmitAndPoll(t *testing.T) {
	var compactSubmit atomic.Bool
	var compactPoll atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/jobs":
			compactSubmit.Store(r.URL.Query().Get("compact") == "1")
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-compact-cli", Status: cluster.JobQueued})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/jobs/job-compact-cli":
			compactPoll.Store(r.URL.Query().Get("compact") == "1")
			_ = json.NewEncoder(w).Encode(cluster.Job{
				ID:     "job-compact-cli",
				Status: cluster.JobCompleted,
				Result: json.RawMessage(`{"output":{"mode":"text","text":"ok"},"status":"completed"}`),
			})
		default:
			http.NotFound(w, r)
		}
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
	cfg.Cluster.Relay.PublicURL = ""
	cfg.Cluster.Worker.RelayURL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(t.TempDir(), "job.json")
	jobJSON := `{"requirements":{"task":"generation"},"payload":{"prompt":"hello"}}`
	if err := os.WriteFile(jobPath, []byte(jobJSON), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clusterSubmitCommand([]string{"--config", configPath, "--file", jobPath, "--token", strings.Repeat("t", 40)}); err != nil {
		t.Fatal(err)
	}
	if !compactSubmit.Load() || !compactPoll.Load() {
		t.Fatalf("cluster submit did not request compact envelopes: submit=%v poll=%v", compactSubmit.Load(), compactPoll.Load())
	}
}

func TestClusterSubmitMaterializesMaximumMultiArtifactCompactResult(t *testing.T) {
	const artifactSize = 6 << 20
	firstData := bytes.Repeat([]byte{0x31}, artifactSize)
	secondData := bytes.Repeat([]byte{0x32}, artifactSize)
	submission := bridge.Submission{
		Status: "completed",
		Output: &bridge.Output{
			Mode: "text",
			Text: "x",
			Artifacts: []bridge.Artifact{
				{Name: "first.bin", MediaType: "application/octet-stream", Size: artifactSize, DataBase64: base64.StdEncoding.EncodeToString(firstData)},
				{Name: "second.bin", MediaType: "application/octet-stream", Size: artifactSize, DataBase64: base64.StdEncoding.EncodeToString(secondData)},
			},
		},
	}
	probe, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	textBytes := int(cluster.MaximumJobResultBytes) - len(probe) + 1
	if textBytes <= 0 {
		t.Fatalf("two supported artifacts no longer fit the maximum result: %d bytes", len(probe))
	}
	submission.Output.Text = strings.Repeat("x", textBytes)
	result, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != int(cluster.MaximumJobResultBytes) {
		t.Fatalf("maximum result fixture is %d bytes, want %d", len(result), cluster.MaximumJobResultBytes)
	}

	var compactSubmit atomic.Bool
	var compactPoll atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/jobs":
			compactSubmit.Store(r.URL.Query().Get("compact") == "1")
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-maximum-cli", Status: cluster.JobQueued})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/jobs/job-maximum-cli":
			compactPoll.Store(r.URL.Query().Get("compact") == "1")
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-maximum-cli", Status: cluster.JobCompleted, Result: result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	temporary := t.TempDir()
	configPath := filepath.Join(temporary, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = ""
	cfg.Cluster.Worker.RelayURL = server.URL
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(temporary, "job.json")
	if err := os.WriteFile(jobPath, []byte(`{"requirements":{"task":"generation"},"payload":{"prompt":"maximum artifact test"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(temporary, "artifacts")
	stdout, err := os.CreateTemp(temporary, "stdout-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(temporary, "stderr-*.txt")
	if err != nil {
		_ = stdout.Close()
		t.Fatal(err)
	}
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	defer func() {
		os.Stdout, os.Stderr = oldStdout, oldStderr
		_ = stdout.Close()
		_ = stderr.Close()
	}()

	if err := clusterSubmitCommand([]string{
		"--config", configPath,
		"--file", jobPath,
		"--token", strings.Repeat("t", 40),
		"--artifacts", artifactDir,
	}); err != nil {
		t.Fatal(err)
	}
	if !compactSubmit.Load() || !compactPoll.Load() {
		t.Fatalf("maximum artifact path did not use compact envelopes: submit=%v poll=%v", compactSubmit.Load(), compactPoll.Load())
	}
	for _, item := range []struct {
		name string
		want byte
	}{
		{name: "first.bin", want: 0x31},
		{name: "second.bin", want: 0x32},
	} {
		path := filepath.Join(artifactDir, item.name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("artifact %s was not materialized: %v", item.name, err)
		}
		if info.Size() != artifactSize {
			t.Fatalf("artifact %s size = %d, want %d", item.name, info.Size(), artifactSize)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var first [1]byte
		_, readErr := file.Read(first[:])
		_ = file.Close()
		if readErr != nil || first[0] != item.want {
			t.Fatalf("artifact %s contents were corrupted: first=%x err=%v", item.name, first[0], readErr)
		}
	}
}
