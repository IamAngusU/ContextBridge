package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestFormatActivityProjectionIsBoundedHonestAndUsesAuthoritativeTime(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	projection := cluster.ActivityProjection{
		Schema: cluster.ActivityProjectionV1, GroupID: "run-a", Kind: "pipeline", Name: "release", State: "running", CreatedAt: now.Add(-89 * time.Minute),
		Items: []cluster.ActivityItem{
			{ID: "publish", JobID: "job-a", State: "ambiguous", StartedAt: now.Add(-68 * time.Second), FinishedAt: now},
			{ID: "render", JobID: "job-b", State: cluster.JobRunning, StartedAt: now.Add(-83 * time.Minute)},
		},
		Summary: cluster.ActivitySummary{Active: 1, Completed: 7, Ambiguous: 1}, DetailOverflow: 4, HistoryComplete: false,
	}
	got := formatActivityProjection(projection, now)
	for _, want := range []string{
		"WORK · pipeline release · running · 01h29m",
		"? publish · ambiguous · 01m08s",
		"● render · running · 01h23m",
		"+ 4 more active/exception rows",
		"✓ 7+ earlier steps completed",
		"no exact missing count is inferred",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("activity view is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "%") || strings.Contains(strings.ToLower(got), " eta ") {
		t.Fatalf("activity view invented progress or ETA: %s", got)
	}
}

func TestFormatHistoricalRuntimeEstimateLabelsAdvisoryEvidence(t *testing.T) {
	estimate := cluster.HistoricalRuntimeEstimate{
		Schema: cluster.HistoricalRuntimeEstimateV1, Status: "available", Source: "local_success_history",
		Profile: "node_route_load", Samples: 27, ElapsedMS: 252000, TotalP50MS: 510000, TotalP90MS: 780000,
		RemainingP50MS: 258000, RemainingP90MS: 528000,
	}
	got := formatHistoricalRuntimeEstimate(estimate)
	for _, want := range []string{"27 successful samples", "non-authoritative", "elapsed · 04m12s", "typical total · 08m30s–13m00s", "estimated remaining · 04m18s–08m48s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime estimate is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "%") || strings.Contains(strings.ToLower(got), "guarantee") {
		t.Fatalf("runtime estimate implies false precision: %s", got)
	}

	outside := estimate
	outside.Status, outside.OutsideTypical, outside.Reason = "outside_typical_range", true, "elapsed_exceeds_typical_history"
	outside.RemainingP50MS, outside.RemainingP90MS = 0, 0
	if got := formatHistoricalRuntimeEstimate(outside); !strings.Contains(got, "outside typical range; estimate uncertain") {
		t.Fatalf("outside-range estimate kept a fake countdown: %s", got)
	}
}

func TestClusterEstimateUsesScopedEndpointAndInterspersedFlags(t *testing.T) {
	var seen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cluster/jobs/job-estimate-cli/estimate" || r.Header.Get("Authorization") != "Bearer producer-estimate-token" {
			http.NotFound(w, r)
			return
		}
		seen.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cluster.HistoricalRuntimeEstimate{
			Schema: cluster.HistoricalRuntimeEstimateV1, Status: "available", Source: "local_success_history", Profile: "node_route", Samples: 5,
			TotalP50MS: 1000, TotalP90MS: 3000, RemainingP50MS: 1000, RemainingP90MS: 3000,
		})
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
	stdout, err := os.CreateTemp(temporary, "estimate-output-*.json")
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = stdout
	defer func() {
		os.Stdout = oldStdout
		_ = stdout.Close()
	}()
	if err := clusterEstimateCommand([]string{"job-estimate-cli", "--json", "--config", configPath, "--token", "producer-estimate-token"}); err != nil {
		t.Fatal(err)
	}
	if !seen.Load() {
		t.Fatal("historical estimate endpoint was not called")
	}
	if err := stdout.Sync(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	var estimate cluster.HistoricalRuntimeEstimate
	if err := json.Unmarshal(raw, &estimate); err != nil || estimate.Schema != cluster.HistoricalRuntimeEstimateV1 || estimate.Samples != 5 {
		t.Fatalf("estimate CLI output = %#v err=%v raw=%s", estimate, err, raw)
	}
}

func TestClusterSubmitUsesCompactResponsesForSubmitAndPoll(t *testing.T) {
	var compactSubmit atomic.Bool
	var compactPoll atomic.Bool
	var idempotencyKey atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/jobs":
			compactSubmit.Store(r.URL.Query().Get("compact") == "1")
			idempotencyKey.Store(r.Header.Get("Idempotency-Key"))
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
	if err := clusterSubmitCommand([]string{"--config", configPath, "--file", jobPath, "--token", strings.Repeat("t", 40), "--idempotency-key", "release-demo-42"}); err != nil {
		t.Fatal(err)
	}
	if !compactSubmit.Load() || !compactPoll.Load() {
		t.Fatalf("cluster submit did not request compact envelopes: submit=%v poll=%v", compactSubmit.Load(), compactPoll.Load())
	}
	if got, _ := idempotencyKey.Load().(string); got != "release-demo-42" {
		t.Fatalf("cluster submit idempotency header = %q", got)
	}
}

func TestClusterEventsSupportsCursorOwnershipTokenAndInterspersedFlags(t *testing.T) {
	var seen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cluster/jobs/job-events-cli/events" || r.URL.Query().Get("after") != "7" || r.URL.Query().Get("limit") != "12" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer producer-events-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		seen.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cluster.JobEventPage{
			After: 7, Next: 8, OldestRetained: 1, Newest: 8,
			Events: []cluster.JobEvent{{
				Schema: cluster.JobEventSchemaV1, JobID: "job-events-cli", Sequence: 8,
				Type: "job.completed", Source: "relay", Authority: "authoritative",
			}},
		})
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
	stdout, err := os.CreateTemp(temporary, "events-output-*.json")
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = stdout
	defer func() {
		os.Stdout = oldStdout
		_ = stdout.Close()
	}()

	// The documented positional-first form must keep working. Go's flag
	// package alone would stop parsing at the job ID.
	if err := clusterEventsCommand([]string{"job-events-cli", "--config", configPath, "--token", "producer-events-token", "--after", "7", "--limit", "12", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !seen.Load() {
		t.Fatal("event endpoint was not called with the requested cursor")
	}
	if err := stdout.Sync(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	var page cluster.JobEventPage
	if err := json.Unmarshal(raw, &page); err != nil || page.Next != 8 || len(page.Events) != 1 || page.Events[0].Type != "job.completed" {
		t.Fatalf("event CLI output = %#v err=%v raw=%s", page, err, raw)
	}
}

func TestClusterEventsRejectsUnsafePollingAndLimits(t *testing.T) {
	for _, args := range [][]string{
		{"job-a", "--limit", "0"},
		{"job-a", "--limit", "501"},
		{"job-a", "--poll", "99ms"},
		{"job-a", "--poll", "31s"},
	} {
		if err := clusterEventsCommand(args); err == nil {
			t.Fatalf("unsafe event options were accepted: %v", args)
		}
	}
}

func TestClusterEventsPipelineFlagUsesPipelineRunEndpoint(t *testing.T) {
	var seen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cluster/pipeline-runs/run-events-cli/events" || r.URL.Query().Get("after") != "2" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		seen.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cluster.JobEventPage{After: 2, Next: 3, Events: []cluster.JobEvent{{
			Schema: cluster.JobEventSchemaV1, RunID: "run-events-cli", Sequence: 3,
			Type: "pipeline.completed", Source: "relay", Authority: "authoritative",
		}}})
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
	if err := clusterEventsCommand([]string{"run-events-cli", "--pipeline", "--config", configPath, "--token", "producer-events-token", "--after", "2", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !seen.Load() {
		t.Fatal("pipeline event endpoint was not called")
	}
}

func TestClusterRouteExplainSupportsPreviewAndDurableJobDecision(t *testing.T) {
	var previewSeen atomic.Bool
	var jobSeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/routes/explain":
			var request cluster.AssignmentRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Requirements.Task != "generation" {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			previewSeen.Store(true)
			_ = json.NewEncoder(w).Encode(cluster.RoutingDecision{
				ID: "route_preview", Preview: true, Requirements: request.Requirements,
				SelectedNodeID: "node-a", SelectedNodeName: "Node A",
				Candidates: []cluster.RoutingCandidateDecision{
					{NodeID: "node-a", NodeName: "Node A", Eligible: true, Score: 12, ScoreComponents: cluster.RoutingScoreComponents{RecentFailures: 12}, RecoveryProbation: true},
					{NodeID: "node-b", NodeName: "Node B", RejectionReasons: []string{"route_circuit_open"}, FailureStreak: 3, CircuitOpenUntil: time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/jobs/job-a/route":
			jobSeen.Store(true)
			_ = json.NewEncoder(w).Encode(cluster.RoutingDecision{
				ID: "route-a", JobID: "job-a", Requirements: cluster.Requirements{Task: "generation"},
				SelectedNodeID: "node-a", SelectedNodeName: "Node A",
			})
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
	if err := os.WriteFile(jobPath, []byte(`{"requirements":{"task":"generation"},"payload":{"prompt":"hello"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.CreateTemp(temporary, "route-output-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = stdout
	defer func() {
		os.Stdout = oldStdout
		_ = stdout.Close()
	}()

	if err := clusterRouteCommand([]string{"explain", "--config", configPath, "--file", jobPath, "--token", "producer-token"}); err != nil {
		t.Fatal(err)
	}
	if err := clusterRouteCommand([]string{"explain", "--config", configPath, "--job", "job-a", "--token", "producer-token", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !previewSeen.Load() || !jobSeen.Load() {
		t.Fatalf("route endpoints were not used: preview=%v job=%v", previewSeen.Load(), jobSeen.Load())
	}
	if err := stdout.Sync(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, wanted := range []string{"Route preview · no job was submitted", "Selected  [Node A · node-a]", "failures +12.00 · single recovery probe", "route_circuit_open · failures 3 · retry after 2026-09-25T12:00:00Z", `"job_id":"job-a"`} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("route output missing %q: %s", wanted, text)
		}
	}
}

func TestClusterSubmitMaterializesMaximumMultiArtifactCompactResult(t *testing.T) {
	const artifactSize = 6 << 20
	firstData := bytes.Repeat([]byte{0x31}, artifactSize)
	secondData := bytes.Repeat([]byte{0x32}, artifactSize)
	firstDigest := sha256.Sum256(firstData)
	secondDigest := sha256.Sum256(secondData)
	submission := bridge.Submission{
		Status: "completed",
		Output: &bridge.Output{
			Mode: "text",
			Text: "x",
			Artifacts: []bridge.Artifact{
				{Name: "first.bin", MediaType: "application/octet-stream", Size: artifactSize, SHA256: hex.EncodeToString(firstDigest[:]), DataBase64: base64.StdEncoding.EncodeToString(firstData)},
				{Name: "second.bin", MediaType: "application/octet-stream", Size: artifactSize, SHA256: hex.EncodeToString(secondDigest[:]), DataBase64: base64.StdEncoding.EncodeToString(secondData)},
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
