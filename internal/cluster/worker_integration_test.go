package cluster

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerRelayParallelCapacityEndToEnd(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:    admin,
		AllowedTasks:  []string{"generation"},
		DispatchEvery: 10 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.dispatchLoop(ctx)
	relayHTTP := httptest.NewServer(relay.Handler())
	defer relayHTTP.Close()

	var running atomic.Int32
	var peak atomic.Int32
	var providerSeen atomic.Bool
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/status":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"queued": 0,
				"routes": map[string]interface{}{
					"default": map[string]interface{}{"task": "generation", "model": "browser-tab", "provider": "browser"},
				},
				"browser": map[string]interface{}{"connected": true, "selectors_ready": true, "active_tabs": 1},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{}},
			})
		case "/v1/jobs":
			var payload struct {
				Provider string `json:"provider"`
				ID       string `json:"id"`
			}
			if json.NewDecoder(req.Body).Decode(&payload) == nil && payload.Provider == "browser" && strings.HasPrefix(payload.ID, "cluster-") {
				providerSeen.Store(true)
			}
			current := running.Add(1)
			for {
				old := peak.Load()
				if current <= old || peak.CompareAndSwap(old, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			running.Add(-1)
			writeJSON(w, http.StatusOK, map[string]interface{}{"mode": "text", "text": "ok", "model": "browser-tab"})
		default:
			if strings.HasPrefix(req.URL.Path, "/v1/browser/jobs/") && strings.HasSuffix(req.URL.Path, "/progress") {
				writeJSON(w, http.StatusOK, JobProgress{Sequence: 1, Text: "working", Phase: "generating", Busy: true})
				return
			}
			http.NotFound(w, req)
		}
	}))
	defer local.Close()

	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "parallel-worker"
	nodeToken, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.json")
	if err := saveIdentity(identityFile, WorkerIdentity{NodeID: nodeID, NodeToken: nodeToken, PrivateKey: privateKey, PublicKey: publicKey, RelayURL: relayHTTP.URL}); err != nil {
		t.Fatal(err)
	}
	worker, err := LoadWorker(WorkerConfig{
		RelayURL:       relayHTTP.URL,
		IdentityFile:   identityFile,
		Name:           "parallel-worker",
		MaxConcurrent:  2,
		LocalURL:       local.URL,
		LocalToken:     "local-test-token",
		HeartbeatEvery: 20 * time.Millisecond,
		RequestTimeout: 5 * time.Second,
		AllowedTasks:   []string{"generation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = worker.Run(ctx, nil) }()

	waitFor(t, 3*time.Second, func() bool {
		node, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && node.Connected
	}, "worker did not connect")

	jobs := make([]Job, 0, 3)
	for index := 0; index < 3; index++ {
		job, createErr := relay.store.CreateJob(SubmitRequest{
			Requirements: Requirements{Task: "generation", Provider: "browser"},
			Payload:      json.RawMessage(`{"route":"default","prompt":"parallel","output":{"mode":"text"}}`),
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		jobs = append(jobs, job)
	}
	relay.signalDispatch()

	for count := 0; count < 2; count++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("two jobs did not start in parallel")
		}
	}
	select {
	case <-started:
		t.Fatal("relay dispatched beyond max_concurrent before a slot was released")
	case <-time.After(200 * time.Millisecond):
	}
	if peak.Load() != 2 {
		t.Fatalf("peak parallelism = %d, want 2", peak.Load())
	}
	waitFor(t, 3*time.Second, func() bool {
		progressing := 0
		for _, submitted := range jobs {
			job, getErr := relay.store.GetJob(submitted.ID)
			if getErr == nil && job.Progress != nil && job.Progress.Text == "working" {
				progressing++
			}
		}
		return progressing == 2
	}, "parallel browser progress did not reach the relay")

	close(release)
	completed := func() bool {
		for _, submitted := range jobs {
			job, getErr := relay.store.GetJob(submitted.ID)
			if getErr != nil || job.Status != JobCompleted {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !completed() {
		time.Sleep(10 * time.Millisecond)
	}
	if !completed() {
		states := make([]string, 0, len(jobs))
		for _, submitted := range jobs {
			job, _ := relay.store.GetJob(submitted.ID)
			states = append(states, job.Status+":"+job.Error)
		}
		t.Fatalf("parallel jobs did not complete: %v", states)
	}
	if !providerSeen.Load() {
		t.Fatal("cluster provider requirement was not enforced by the local bridge request")
	}
}

func TestWorkerCancelsLocalExecutionWhenRelayConnectionDrops(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:    admin,
		AllowedTasks:  []string{"generation"},
		DispatchEvery: 10 * time.Millisecond,
		JobTimeout:    time.Minute,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	defer stopDispatch()
	go relay.dispatchLoop(dispatchCtx)
	relayHTTP := httptest.NewServer(relay.Handler())
	defer relayHTTP.Close()

	localStarted := make(chan struct{})
	localCancelled := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/status":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"queued": 0,
				"routes": map[string]interface{}{
					"default": map[string]interface{}{"task": "generation", "model": "browser-tab", "provider": "browser"},
				},
				"browser": map[string]interface{}{"connected": true, "selectors_ready": true, "active_tabs": 1},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{}},
			})
		case "/v1/jobs":
			_, _ = io.Copy(io.Discard, req.Body)
			close(localStarted)
			<-req.Context().Done()
			close(localCancelled)
		default:
			if strings.HasPrefix(req.URL.Path, "/v1/browser/jobs/") && strings.HasSuffix(req.URL.Path, "/progress") {
				writeJSON(w, http.StatusOK, JobProgress{Sequence: 1, Phase: "generating", Busy: true})
				return
			}
			http.NotFound(w, req)
		}
	}))
	defer local.Close()

	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "disconnect-cancel-worker"
	nodeToken, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.json")
	if err := saveIdentity(identityFile, WorkerIdentity{NodeID: nodeID, NodeToken: nodeToken, PrivateKey: privateKey, PublicKey: publicKey, RelayURL: relayHTTP.URL}); err != nil {
		t.Fatal(err)
	}
	worker, err := LoadWorker(WorkerConfig{
		RelayURL:       relayHTTP.URL,
		IdentityFile:   identityFile,
		Name:           nodeID,
		MaxConcurrent:  1,
		LocalURL:       local.URL,
		LocalToken:     "local-test-token",
		HeartbeatEvery: 20 * time.Millisecond,
		RequestTimeout: time.Minute,
		AllowedTasks:   []string{"generation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx, nil) }()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(3 * time.Second):
			t.Error("worker did not stop after its parent context was cancelled")
		}
	}()

	waitFor(t, 3*time.Second, func() bool {
		node, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && node.Connected
	}, "worker did not connect")
	job, err := relay.store.CreateJob(SubmitRequest{
		Requirements: Requirements{Task: "generation", Provider: "browser"},
		Payload:      json.RawMessage(`{"route":"default","prompt":"cancel me","output":{"mode":"text"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	relay.signalDispatch()
	select {
	case <-localStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("local execution did not start")
	}

	relay.mu.RLock()
	connection := relay.workers[nodeID]
	relay.mu.RUnlock()
	if connection == nil {
		t.Fatal("relay lost the worker before the disconnect test")
	}
	_ = connection.conn.CloseNow()
	select {
	case <-localCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("local HTTP execution context survived the relay connection")
	}
	waitFor(t, 3*time.Second, func() bool {
		failed, loadErr := relay.store.GetJob(job.ID)
		return loadErr == nil && failed.Status == JobFailed && strings.Contains(failed.Error, "explicit resubmission required")
	}, "disconnected execution was not failed closed")
}

func TestRelayCancellationInterruptsAssignedWorkerExecution(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:    admin,
		AllowedTasks:  []string{"generation"},
		DispatchEvery: 10 * time.Millisecond,
		JobTimeout:    time.Minute,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	defer stopDispatch()
	go relay.dispatchLoop(dispatchCtx)
	relayHTTP := httptest.NewServer(relay.Handler())
	defer relayHTTP.Close()

	localStarted := make(chan struct{})
	localCancelled := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/status":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"queued": 0,
				"routes": map[string]interface{}{
					"default": map[string]interface{}{"task": "generation", "model": "browser-tab", "provider": "browser"},
				},
				"browser": map[string]interface{}{"connected": true, "selectors_ready": true, "active_tabs": 1},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{}},
			})
		case "/v1/jobs":
			_, _ = io.Copy(io.Discard, req.Body)
			close(localStarted)
			<-req.Context().Done()
			close(localCancelled)
		default:
			if strings.HasPrefix(req.URL.Path, "/v1/browser/jobs/") && strings.HasSuffix(req.URL.Path, "/progress") {
				writeJSON(w, http.StatusOK, JobProgress{Sequence: 1, Phase: "generating", Busy: true})
				return
			}
			http.NotFound(w, req)
		}
	}))
	defer local.Close()

	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "explicit-cancel-worker"
	nodeToken, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.json")
	if err := saveIdentity(identityFile, WorkerIdentity{NodeID: nodeID, NodeToken: nodeToken, PrivateKey: privateKey, PublicKey: publicKey, RelayURL: relayHTTP.URL}); err != nil {
		t.Fatal(err)
	}
	worker, err := LoadWorker(WorkerConfig{
		RelayURL:       relayHTTP.URL,
		IdentityFile:   identityFile,
		Name:           nodeID,
		MaxConcurrent:  1,
		LocalURL:       local.URL,
		LocalToken:     "local-test-token",
		HeartbeatEvery: 20 * time.Millisecond,
		RequestTimeout: time.Minute,
		AllowedTasks:   []string{"generation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx, nil) }()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(3 * time.Second):
			t.Error("worker did not stop after its parent context was cancelled")
		}
	}()

	waitFor(t, 3*time.Second, func() bool {
		node, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && node.Connected
	}, "worker did not connect")
	job, err := relay.store.CreateJob(SubmitRequest{
		Requirements: Requirements{Task: "generation", Provider: "browser"},
		Payload:      json.RawMessage(`{"route":"default","prompt":"cancel me explicitly","output":{"mode":"text"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	relay.signalDispatch()
	select {
	case <-localStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("local execution did not start")
	}

	request, err := http.NewRequest(http.MethodDelete, relayHTTP.URL+"/v1/cluster/jobs/"+job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+admin)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("cancel status = %d: %s", response.StatusCode, body)
	}
	select {
	case <-localCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit relay cancellation did not cancel the local HTTP request")
	}
	waitFor(t, 3*time.Second, worker.Idle, "worker slot was not released after cancellation")
	cancelled, err := relay.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != JobCancelled {
		t.Fatalf("job status = %q, want %q", cancelled.Status, JobCancelled)
	}
}

func TestMaintenanceTimeoutInterruptsHungWorkerExecution(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:    admin,
		AllowedTasks:  []string{"generation"},
		DispatchEvery: 10 * time.Millisecond,
		JobTimeout:    80 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	relayHTTP := httptest.NewServer(relay.Handler())
	defer relayHTTP.Close()

	localStarted := make(chan struct{})
	localCancelled := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/status":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"queued": 0,
				"routes": map[string]interface{}{
					"default": map[string]interface{}{"task": "generation", "model": "browser-tab", "provider": "browser"},
				},
				"browser": map[string]interface{}{"connected": true, "selectors_ready": true, "active_tabs": 1},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{}},
			})
		case "/v1/jobs":
			_, _ = io.Copy(io.Discard, req.Body)
			close(localStarted)
			<-req.Context().Done()
			close(localCancelled)
		default:
			if strings.HasPrefix(req.URL.Path, "/v1/browser/jobs/") && strings.HasSuffix(req.URL.Path, "/progress") {
				writeJSON(w, http.StatusOK, JobProgress{Sequence: 1, Phase: "generating", Busy: true})
				return
			}
			http.NotFound(w, req)
		}
	}))
	defer local.Close()

	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "maintenance-timeout-worker"
	nodeToken, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.json")
	if err := saveIdentity(identityFile, WorkerIdentity{NodeID: nodeID, NodeToken: nodeToken, PrivateKey: privateKey, PublicKey: publicKey, RelayURL: relayHTTP.URL}); err != nil {
		t.Fatal(err)
	}
	worker, err := LoadWorker(WorkerConfig{
		RelayURL:       relayHTTP.URL,
		IdentityFile:   identityFile,
		Name:           nodeID,
		MaxConcurrent:  1,
		LocalURL:       local.URL,
		LocalToken:     "local-test-token",
		HeartbeatEvery: 20 * time.Millisecond,
		RequestTimeout: time.Minute,
		AllowedTasks:   []string{"generation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx, nil) }()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(3 * time.Second):
			t.Error("worker did not stop after its parent context was cancelled")
		}
	}()

	waitFor(t, 3*time.Second, func() bool {
		node, loadErr := relay.store.GetNode(nodeID)
		return loadErr == nil && node.Connected
	}, "worker did not connect")
	job, err := relay.store.CreateJob(SubmitRequest{
		Requirements: Requirements{Task: "generation", Provider: "browser"},
		Payload:      json.RawMessage(`{"route":"default","prompt":"hang until maintenance cancellation","output":{"mode":"text"}}`),
		MaxAttempts:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	relay.dispatch()
	select {
	case <-localStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("local execution did not start")
	}

	// Use the stored assignment timestamp so the test proves the durable timeout,
	// rather than depending on how quickly the worker connected on this host.
	running, err := relay.store.GetJob(job.ID)
	if err != nil || running.AssignedAt.IsZero() {
		t.Fatalf("job did not enter assigned execution: %#v, %v", running, err)
	}
	relay.runMaintenance(running.AssignedAt.Add(relay.cfg.JobTimeout + time.Millisecond))
	select {
	case <-localCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance timeout failed to cancel the worker's local HTTP request")
	}
	waitFor(t, 3*time.Second, worker.Idle, "worker slot was not released after its cancellation result")
	timedOut, err := relay.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut.Status != JobFailed || !strings.Contains(timedOut.Error, "execution state is ambiguous") {
		t.Fatalf("maintenance timeout did not fail closed: %#v", timedOut)
	}
}

func TestLocalOutputErrorRecognizesSubmissionAndDirectOutput(t *testing.T) {
	if got := localOutputError([]byte(`{"status":"completed","output":{"mode":"text","error":"providers_unavailable"}}`)); got != "providers_unavailable" {
		t.Fatalf("wrapped output error was missed: %q", got)
	}
	if got := localOutputError([]byte(`{"mode":"text","error":"browser_automation_error"}`)); got != "browser_automation_error" {
		t.Fatalf("direct output error was missed: %q", got)
	}
	if got := localOutputError([]byte(`{"status":"completed","output":{"mode":"text","text":"ok"}}`)); got != "" {
		t.Fatalf("successful output was treated as an error: %q", got)
	}
}

func TestExtractUsageDoesNotMisattributeNodeOrResultResourceFields(t *testing.T) {
	usage := extractUsage([]byte(`{"output":{"json":{"peak_vram_bytes":1024}},"hardware":{"memory_total_bytes":999999,"memory_free_bytes":1,"utilization_percent":99}}`))
	if usage.PeakVRAMBytes != 0 || usage.PeakRAMBytes != 0 || usage.PeakGPUUtilization != 0 {
		t.Fatalf("unattributed resource counters were misattributed to a job: %#v", usage)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}
