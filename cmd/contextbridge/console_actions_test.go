package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

func TestConsoleActionCredentialNeverFallsBackToRelayAdmin(t *testing.T) {
	t.Setenv("CONTEXTBRIDGE_CLUSTER_TOKEN", "")
	cfg := config.Config{}
	cfg.Cluster.Relay.AdminToken = "must-not-be-used"
	if token, source := consoleActionCredential(cfg, ""); token != "" || source != "" {
		t.Fatalf("console inherited relay admin authority: token=%q source=%q", token, source)
	}
	cfg.Cluster.ClientToken = "config-producer"
	if token, source := consoleActionCredential(cfg, ""); token != "config-producer" || source != "cluster.client_token" {
		t.Fatalf("config credential resolution = %q %q", token, source)
	}
	t.Setenv("CONTEXTBRIDGE_CLUSTER_TOKEN", "environment-producer")
	if token, source := consoleActionCredential(cfg, ""); token != "environment-producer" || source != "CONTEXTBRIDGE_CLUSTER_TOKEN" {
		t.Fatalf("environment credential resolution = %q %q", token, source)
	}
	if token, source := consoleActionCredential(cfg, "explicit-producer"); token != "explicit-producer" || source != "--token" {
		t.Fatalf("explicit credential resolution = %q %q", token, source)
	}
}

func TestConsoleCommandLimitsUseConfigAndAuthenticatedRelayManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cluster/protocol" || r.Header.Get("Authorization") != "Bearer producer" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(cluster.ProtocolManifest{
			Schema: cluster.ProtocolManifestV1,
			Limits: cluster.ProtocolLimits{MaximumConfiguredJobPayloadBytes: 8192},
		})
	}))
	defer server.Close()
	cfg := config.Config{Terminal: config.Terminal{MaxPromptCharacters: 777}}
	limits := consoleCommandLimits(context.Background(), newClusterAPIClient(server.URL, "producer"), cfg)
	if limits.MaxPromptCharacters != 777 || limits.MaxPayloadBytes != 8192 || limits.PayloadOverheadBytes < 1 || limits.SessionProvider != "adapter" {
		t.Fatalf("console command limits = %#v", limits)
	}
}

func TestConsoleIntentMapsOnlyTypedRoutingFields(t *testing.T) {
	var submitted cluster.SubmitRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-routed", Status: cluster.JobQueued})
	}))
	defer server.Close()
	intent := terminalui.ConsoleIntent{
		Action: terminalui.ConsoleIntentSend, Argument: "bounded prompt",
		Submit: terminalui.ConsoleSubmitOptions{
			Provider: "adapter", Model: "model-a", Group: "pool-a", SessionID: "session-a",
			Profile: "profile-a", Reasoning: "high", Egress: "remote_allowed", MaxCostUSD: 0.25,
			FreshSession: true, EphemeralSession: true,
		},
	}
	job, err := submitConsoleIntent(context.Background(), newClusterAPIClient(server.URL, "producer"), intent)
	if err != nil || job.ID != "job-routed" {
		t.Fatalf("submit routed intent = %#v err=%v", job, err)
	}
	if submitted.Requirements.Provider != "adapter" || submitted.Requirements.Model != "model-a" || submitted.Requirements.Group != "pool-a" ||
		submitted.Requirements.SessionID != "session-a" || submitted.Requirements.AdapterProfile != "profile-a" || submitted.Requirements.Reasoning != "high" ||
		submitted.Requirements.Egress != "remote_allowed" || submitted.Requirements.MaxCostUSD != 0.25 || !submitted.Requirements.AdapterFreshSession || !submitted.Requirements.AdapterEphemeralSession {
		t.Fatalf("typed routing fields were not preserved: %#v", submitted.Requirements)
	}
}

func TestSharedTextJobBuilderPreservesChatRoutingAndOutputContract(t *testing.T) {
	request, err := buildClusterTextSubmitRequest(clusterTextJobOptions{
		Source: "terminal-chat", Prompt: "inspect", SessionID: "session-a",
		Provider: "adapter", Group: "private", Model: "model-a", AdapterProfile: "profile-a",
		Reasoning: "high", Egress: "local_only", MaxCostUSD: 0.25,
		ImageBase64: "aW1hZ2U=", ImageMediaType: "image/png",
		Metadata:            map[string]interface{}{"bounded": true},
		Output:              bridge.OutputSpec{Mode: "text", MaxBytes: 4096, Artifacts: true, MinImages: 1},
		AdapterFreshSession: true, AdapterEphemeralSession: true, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.ContractVersion != cluster.JobContractV1 || request.Source != "terminal-chat" || request.MaxAttempts != 1 ||
		request.Requirements.Task != "generation" || request.Requirements.Provider != "adapter" || request.Requirements.Group != "private" ||
		request.Requirements.Model != "model-a" || request.Requirements.AdapterProfile != "profile-a" || request.Requirements.Reasoning != "high" ||
		!request.Requirements.Vision || !request.Requirements.AdapterFreshSession || !request.Requirements.AdapterEphemeralSession ||
		request.Requirements.InputImageCount != 1 || request.Requirements.InputImageBytes != 5 || request.Requirements.InputImageMaxBytes != 5 || len(request.Requirements.InputImageMediaTypes) != 1 ||
		request.Requirements.Egress != "local_only" || request.Requirements.MaxCostUSD != 0.25 {
		t.Fatalf("shared request lost routing contract: %#v", request)
	}
	var job bridge.Job
	if err := json.Unmarshal(request.Payload, &job); err != nil {
		t.Fatal(err)
	}
	if job.Prompt != "inspect" || job.SessionID != "session-a" || job.ImageMediaType != "image/png" || job.Output.MinImages != 1 || !job.Output.Artifacts || job.MaxCostUSD != 0.25 {
		t.Fatalf("shared request lost payload/output contract: %#v", job)
	}
	local, err := buildClusterTextSubmitRequest(clusterTextJobOptions{Provider: "ollama", Reasoning: "must-not-route", Output: bridge.OutputSpec{Mode: "text"}})
	if err != nil {
		t.Fatal(err)
	}
	if local.Requirements.Reasoning != "" || local.Requirements.AdapterFreshSession || local.Requirements.AdapterEphemeralSession {
		t.Fatalf("adapter-only requirements leaked onto a local route: %#v", local.Requirements)
	}
	multi, err := buildClusterTextSubmitRequest(clusterTextJobOptions{Provider: "ollama", Images: []bridge.ImageInput{{MediaType: "image/png", DataBase64: "YQ=="}, {MediaType: "image/jpeg", DataBase64: "YmM="}}, Output: bridge.OutputSpec{Mode: "text"}})
	if err != nil {
		t.Fatal(err)
	}
	if !multi.Requirements.Vision || multi.Requirements.InputImageCount != 2 || multi.Requirements.InputImageBytes != 3 || multi.Requirements.InputImageMaxBytes != 2 || len(multi.Requirements.InputImageMediaTypes) != 2 {
		t.Fatalf("multi-image routing evidence was not derived: %#v", multi.Requirements)
	}
}

func TestClusterAPIClientUsesScopedPathsIdempotencyAndOwnershipToken(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer producer-token" {
			http.Error(w, "wrong credential", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cluster/jobs":
			if r.Header.Get("Idempotency-Key") != "console-key" || r.URL.Query().Get("compact") != "1" {
				http.Error(w, "missing idempotency or compact contract", http.StatusBadRequest)
				return
			}
			var request cluster.SubmitRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Requirements.Task != "generation" {
				http.Error(w, "invalid submit", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-owned", Status: cluster.JobQueued})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/jobs":
			_ = json.NewEncoder(w).Encode([]cluster.Job{{ID: "job-owned", Status: cluster.JobRunning}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/jobs/job-owned":
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-owned", Status: cluster.JobRunning})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cluster/jobs/job-owned/events":
			_ = json.NewEncoder(w).Encode(cluster.JobEventPage{After: 2, Next: 3, Events: []cluster.JobEvent{{JobID: "job-owned", Sequence: 3, Type: "execution.started", Authority: "authoritative"}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/cluster/jobs/job-owned":
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-owned", Status: cluster.JobCancelled})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newClusterAPIClient(server.URL, "producer-token")
	request := cluster.SubmitRequest{Requirements: cluster.Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"bounded"}`)}
	if job, err := client.Submit(context.Background(), request, "console-key"); err != nil || job.ID != "job-owned" {
		t.Fatalf("submit = %#v err=%v", job, err)
	}
	if jobs, err := client.Jobs(context.Background(), 8); err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %#v err=%v", jobs, err)
	}
	if job, err := client.Job(context.Background(), "job-owned"); err != nil || job.Status != cluster.JobRunning {
		t.Fatalf("job = %#v err=%v", job, err)
	}
	if page, err := client.Events(context.Background(), "job-owned", 2, 100); err != nil || page.Next != 3 {
		t.Fatalf("events = %#v err=%v", page, err)
	}
	if job, err := client.Cancel(context.Background(), "job-owned"); err != nil || job.Status != cluster.JobCancelled {
		t.Fatalf("cancel = %#v err=%v", job, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST /v1/cluster/jobs?compact=1",
		"GET /v1/cluster/jobs?limit=8",
		"GET /v1/cluster/jobs/job-owned?compact=1",
		"GET /v1/cluster/jobs/job-owned/events?after=2&limit=100",
		"DELETE /v1/cluster/jobs/job-owned",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("request sequence:\n%s\nwant:\n%s", strings.Join(requests, "\n"), strings.Join(want, "\n"))
	}
}

func TestConsoleResultNoticeIsBoundedAndExplicit(t *testing.T) {
	result, err := json.Marshal(bridge.Submission{Status: "completed", Output: &bridge.Output{Mode: "text", Text: strings.Repeat("x", maximumConsoleResultRunes+20)}})
	if err != nil {
		t.Fatal(err)
	}
	notice := consoleResultNotice(cluster.Job{ID: "job-large", Status: cluster.JobCompleted, Result: result})
	if !strings.Contains(notice, "console display truncated; retained result is unchanged") || len([]rune(notice)) > maximumConsoleResultRunes+200 {
		t.Fatalf("large result was not bounded explicitly: runes=%d suffix=%q", len([]rune(notice)), notice[len(notice)-80:])
	}
	sealed := consoleResultNotice(cluster.Job{ID: "job-sealed", Status: cluster.JobCompleted, SealedResult: &cluster.SealedEnvelope{Ciphertext: "opaque"}})
	if !strings.Contains(sealed, "originating E2EE client") {
		t.Fatalf("sealed result implied a plaintext downgrade: %q", sealed)
	}
}

func TestConsoleJobNoticesShowAuthoritativeElapsedQueueAndExecution(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	running := cluster.Job{
		ID: "job-running", Status: cluster.JobRunning, Requirements: cluster.Requirements{Task: "generation"}, AssignedNode: "node-a",
		CreatedAt: now.Add(-90 * time.Second), AssignedAt: now.Add(-86 * time.Second), StartedAt: now.Add(-83 * time.Second),
	}
	detail := consoleJobNoticeAt(running, now)
	for _, want := range []string{"running · elapsed 01m23s", "queue · 4s", "execution elapsed · 01m23s"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("job detail is missing %q:\n%s", want, detail)
		}
	}
	list := consoleJobsNoticeAt([]cluster.Job{running}, now)
	if !strings.Contains(list, "job-running · running · elapsed 01m23s · generation") {
		t.Fatalf("job list lacks elapsed timing: %q", list)
	}

	terminal := running
	terminal.Status = cluster.JobCompleted
	terminal.FinishedAt = now.Add(-45 * time.Second)
	first := consoleJobNoticeAt(terminal, now)
	second := consoleJobNoticeAt(terminal, now.Add(24*time.Hour))
	if first != second || !strings.Contains(first, "completed · execution 38s") {
		t.Fatalf("terminal duration was not stable: first=%q second=%q", first, second)
	}
}

func TestPipelineTimingSuffixUsesStableRelayTimestamps(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	run := cluster.PipelineRun{Status: "completed", CreatedAt: now.Add(-26 * time.Hour), FinishedAt: now.Add(-16 * time.Minute)}
	first := pipelineTimingSuffix(run, now)
	second := pipelineTimingSuffix(run, now.Add(48*time.Hour))
	if first != " · completed · elapsed 1d01h44m" || second != first {
		t.Fatalf("pipeline suffix first=%q second=%q", first, second)
	}
}

func TestClusterAPIClientRejectsUnsafeIDsBeforeNetwork(t *testing.T) {
	client := newClusterAPIClient("http://127.0.0.1:1", "producer")
	for _, id := range []string{"", "../foreign", "job/other", "job\\other", "job other", "job?other", "_job", strings.Repeat("a", 129)} {
		if _, err := client.Job(context.Background(), id); err == nil {
			t.Fatalf("unsafe job ID accepted: %q", id)
		}
	}
}

func TestConsolePlaintextSendRecoversLostResponseWithSameIdempotencyKey(t *testing.T) {
	var requests atomic.Int32
	var firstKey atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		key := r.Header.Get("Idempotency-Key")
		if count == 1 {
			firstKey.Store(key)
			panic(http.ErrAbortHandler)
		}
		if key == "" || key != firstKey.Load().(string) {
			http.Error(w, "logical retry changed idempotency key", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job-recovered", Status: cluster.JobQueued})
	}))
	defer server.Close()

	job, err := submitConsoleText(context.Background(), newClusterAPIClient(server.URL, "producer"), "one logical action")
	if err != nil || job.ID != "job-recovered" || requests.Load() != 2 {
		t.Fatalf("lost-response recovery job=%#v requests=%d err=%v", job, requests.Load(), err)
	}
}

func TestConsoleClientUsesRealRelayOwnershipAndProducerGovernance(t *testing.T) {
	const admin = "admin_console_contract_012345678901234567890123"
	relay, err := cluster.NewRelay(cluster.RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin,
		AllowedTasks: []string{"generation"}, MaxJobBytes: 4096,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	mint := func(subject string, hourly int) string {
		t.Helper()
		var created struct {
			Token string `json:"token"`
		}
		input := map[string]interface{}{
			"role": "producer", "subject": subject, "lifetime_hours": 1,
			"producer_limits": map[string]interface{}{"max_jobs_per_hour": hourly},
		}
		if err := clusterPOST(context.Background(), server.URL+"/v1/cluster/tokens", admin, input, &created); err != nil || created.Token == "" {
			t.Fatalf("mint %s: token=%q err=%v", subject, created.Token, err)
		}
		return created.Token
	}
	owner := newClusterAPIClient(server.URL, mint("console-owner", 1))
	other := newClusterAPIClient(server.URL, mint("console-other", 5))
	payload := json.RawMessage(`{"prompt":"same relay boundary"}`)
	request := cluster.SubmitRequest{Source: "console", Requirements: cluster.Requirements{Task: "generation"}, Payload: payload, MaxAttempts: 1}
	job, err := owner.Submit(context.Background(), request, "console-owner-first")
	if err != nil || job.ID == "" {
		t.Fatalf("owner submit = %#v err=%v", job, err)
	}
	if _, err := other.Job(context.Background(), job.ID); err == nil {
		t.Fatal("second producer read another producer's console job")
	} else if apiErr, ok := err.(*clusterAPIError); !ok || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-owner read error = %#v", err)
	}
	if _, err := other.Cancel(context.Background(), job.ID); err == nil {
		t.Fatal("second producer cancelled another producer's console job")
	} else if apiErr, ok := err.(*clusterAPIError); !ok || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-owner cancel error = %#v", err)
	}
	if _, err := owner.Submit(context.Background(), request, "console-owner-second"); err == nil {
		t.Fatal("console bypassed producer hourly governance")
	} else if apiErr, ok := err.(*clusterAPIError); !ok || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("governance error = %#v", err)
	}
	if cancelled, err := owner.Cancel(context.Background(), job.ID); err != nil || cancelled.Status != cluster.JobCancelled {
		t.Fatalf("owner cancel = %#v err=%v", cancelled, err)
	}
}
