package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/updater"
	"github.com/IamAngusU/ContextBridge/internal/vectorstore"
)

func TestDecodeJSONEnforcesExactBodyLimit(t *testing.T) {
	raw := []byte(`{"ok":true}`)
	var exact struct {
		OK bool `json:"ok"`
	}
	if err := decodeJSON(bytes.NewReader(raw), &exact, int64(len(raw))); err != nil || !exact.OK {
		t.Fatalf("exact JSON body limit was rejected: %#v, %v", exact, err)
	}
	var over struct {
		OK bool `json:"ok"`
	}
	if err := decodeJSON(bytes.NewReader(append(append([]byte{}, raw...), ' ')), &over, int64(len(raw))); err == nil {
		t.Fatal("one byte beyond the JSON body limit was accepted")
	}
	if err := decodeJSON(bytes.NewBufferString(`{"ok":true}{"ok":false}`), &over, 64); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
	if err := decodeJSON(bytes.NewReader([]byte{'{', '"', 'o', 'k', '"', ':', '"', 0xff, '"', '}'}), &over, 64); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}

func TestInboxDecoderIsStrictAndBoundedBeforeProcessing(t *testing.T) {
	dir := t.TempDir()
	exactPath := filepath.Join(dir, "exact.processing.json")
	overPath := filepath.Join(dir, "over.processing.json")
	unknownPath := filepath.Join(dir, "unknown.processing.json")
	prefix := []byte(`{"prompt":"`)
	suffix := []byte(`"}`)
	exact := append(append(append([]byte{}, prefix...), bytes.Repeat([]byte{'a'}, int(maximumJobRequestBytes)-len(prefix)-len(suffix))...), suffix...)
	if err := os.WriteFile(exactPath, exact, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overPath, append(append([]byte{}, exact...), ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknownPath, []byte(`{"prompt":"ok","unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	var exactJob Job
	if err := decodeInboxJob(exactPath, &exactJob); err != nil {
		t.Fatalf("exact inbox limit was rejected: %v", err)
	}
	if len(exactJob.Prompt) == 0 {
		t.Fatal("exact inbox payload was not decoded")
	}
	if err := decodeInboxJob(overPath, &Job{}); err == nil {
		t.Fatal("inbox payload one byte over the limit was accepted")
	}
	if err := decodeInboxJob(unknownPath, &Job{}); err == nil {
		t.Fatal("unknown inbox field was accepted")
	}
}

func TestInboxFilesDoNotFollowLinksOrOverwriteExistingResults(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(target, []byte("keep-me"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.processing.json")
	if err := os.Symlink(target, link); err == nil {
		if err := decodeInboxJob(link, &Job{}); err == nil {
			t.Fatal("inbox decoder followed a symbolic link")
		}
	} else {
		t.Logf("symbolic-link input check skipped on this host: %v", err)
	}

	result := filepath.Join(dir, "job.result.json")
	if err := os.WriteFile(result, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeInboxResult(result, []byte("replacement")); err == nil {
		t.Fatal("existing result was overwritten")
	}
	value, err := os.ReadFile(result)
	if err != nil || string(value) != "existing" {
		t.Fatalf("existing result changed: %q, %v", value, err)
	}

	fresh := filepath.Join(dir, "fresh.result.json")
	if err := writeInboxResult(fresh, []byte("safe")); err != nil {
		t.Fatalf("safe result could not be written: %v", err)
	}
	value, err = os.ReadFile(fresh)
	if err != nil || string(value) != "safe" {
		t.Fatalf("fresh result = %q, %v", value, err)
	}
	value, err = os.ReadFile(target)
	if err != nil || string(value) != "keep-me" {
		t.Fatalf("outside target changed: %q, %v", value, err)
	}
}

func TestInboxResultDoesNotFollowSymbolicLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(target, []byte("keep-me"), 0600); err != nil {
		t.Fatal(err)
	}
	resultLink := filepath.Join(dir, "job.result.json")
	if err := os.Symlink(target, resultLink); err != nil {
		t.Skipf("symbolic-link result check is unavailable on this host: %v", err)
	}
	if err := writeInboxResult(resultLink, []byte("replacement")); err == nil {
		t.Fatal("inbox result writer followed or replaced a pre-existing symbolic link")
	}
	value, err := os.ReadFile(target)
	if err != nil || string(value) != "keep-me" {
		t.Fatalf("symbolic-link target changed: %q, %v", value, err)
	}
	info, err := os.Lstat(resultLink)
	if err != nil {
		t.Fatalf("result link disappeared: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("result link was replaced: mode=%v", info.Mode())
	}
}

func TestInboxWatcherBoundsConcurrentFanout(t *testing.T) {
	directory := t.TempDir()
	inbox := filepath.Join(directory, "inbox")
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: directory, Inbox: inbox},
		Routes: map[string]config.Route{
			"default": {Provider: "adapter", TimeoutSeconds: 30},
		},
		Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 5}},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	const jobs = maximumInboxScanBatch + 17
	for index := 0; index < jobs; index++ {
		path := filepath.Join(inbox, fmt.Sprintf("job-%03d.json", index))
		payload := []byte(fmt.Sprintf(`{"id":"inbox-%03d","prompt":"block","route":"default","output":{"mode":"text"}}`, index))
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.watchInbox(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("inbox watcher did not stop")
		}
		deadline := time.Now().Add(3 * time.Second)
		for len(server.inboxSlots) > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if active := len(server.inboxSlots); active != 0 {
			t.Errorf("%d inbox workers did not stop", active)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(server.inboxSlots) < maximumInboxConcurrent {
		time.Sleep(20 * time.Millisecond)
	}
	if active := len(server.inboxSlots); active != maximumInboxConcurrent {
		t.Fatalf("inbox started %d workers, want %d", active, maximumInboxConcurrent)
	}
	// Let the watcher cross another tick and directory scan boundary while all
	// workers remain blocked in the adapter provider.
	time.Sleep(1200 * time.Millisecond)
	if active := len(server.inboxSlots); active != maximumInboxConcurrent {
		t.Fatalf("inbox fanout changed to %d while all slots were occupied", active)
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	processing := 0
	pending := 0
	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.Name(), ".processing.json"):
			processing++
		case strings.HasSuffix(entry.Name(), ".json"):
			pending++
		}
	}
	if processing != maximumInboxConcurrent || pending != jobs-maximumInboxConcurrent {
		t.Fatalf("inbox state has %d processing and %d pending jobs, want %d and %d", processing, pending, maximumInboxConcurrent, jobs-maximumInboxConcurrent)
	}
}

func TestAdapterJobRoundTrip(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "adapter", TimeoutSeconds: 5, AdapterProfile: "test"},
		},
		Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 5}},
		AdapterProfiles: map[string]config.AdapterProfile{
			"test": {Label: "Test adapter", Driver: "test"},
		},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	result := make(chan Submission, 1)
	go func() {
		jobRaw, _ := json.Marshal(Job{Prompt: "Review safely", Text: "hello", SessionID: "shared-name", ContextBridgeSessionKey: "cb:producer-scoped-test"})
		req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/jobs", bytes.NewReader(jobRaw))
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			return
		}
		defer resp.Body.Close()
		var submission Submission
		json.NewDecoder(resp.Body).Decode(&submission)
		result <- submission
	}()

	var work adapterJob
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/adapter/jobs/next?wait=0&profile=test", nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr == nil && resp.StatusCode == http.StatusOK {
			json.NewDecoder(resp.Body).Decode(&work)
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if work.Job.ID == "" {
		t.Fatal("adapter job was not queued")
	}
	if work.Job.ContextBridgeSessionKey != "cb:producer-scoped-test" {
		t.Fatal("worker-derived session scope was lost before reaching the adapter process")
	}
	leaseReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/lease", nil)
	leaseReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	leaseReq.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	leaseResp, leaseErr := http.DefaultClient.Do(leaseReq)
	if leaseErr != nil {
		t.Fatal(leaseErr)
	}
	leaseResp.Body.Close()
	if leaseResp.StatusCode != http.StatusOK {
		t.Fatalf("lease renewal returned %s", leaseResp.Status)
	}
	leaseStatusReq, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/lease", nil)
	leaseStatusReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	leaseStatusReq.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	leaseStatusResp, leaseStatusErr := http.DefaultClient.Do(leaseStatusReq)
	if leaseStatusErr != nil {
		t.Fatal(leaseStatusErr)
	}
	var leaseStatus struct {
		OK             bool      `json:"ok"`
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
	}
	if err := json.NewDecoder(leaseStatusResp.Body).Decode(&leaseStatus); err != nil {
		t.Fatal(err)
	}
	leaseStatusResp.Body.Close()
	if leaseStatusResp.StatusCode != http.StatusOK || !leaseStatus.OK || leaseStatus.LeaseExpiresAt.IsZero() {
		t.Fatalf("unexpected read-only lease status: %s %#v", leaseStatusResp.Status, leaseStatus)
	}
	wrongLeaseStatusReq, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/lease", nil)
	wrongLeaseStatusReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	wrongLeaseStatusReq.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration+1, 10))
	wrongLeaseStatusResp, wrongLeaseStatusErr := http.DefaultClient.Do(wrongLeaseStatusReq)
	if wrongLeaseStatusErr != nil {
		t.Fatal(wrongLeaseStatusErr)
	}
	wrongLeaseStatusResp.Body.Close()
	if wrongLeaseStatusResp.StatusCode != http.StatusConflict {
		t.Fatalf("wrong lease generation returned %s", wrongLeaseStatusResp.Status)
	}
	claimGetReq, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/claim", nil)
	claimGetReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	claimGetResp, claimGetErr := http.DefaultClient.Do(claimGetReq)
	if claimGetErr != nil {
		t.Fatal(claimGetErr)
	}
	claimGetResp.Body.Close()
	if claimGetResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET claim returned %s", claimGetResp.Status)
	}
	claimReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/claim", bytes.NewReader([]byte(`{"action":"commit"}`)))
	claimReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	claimReq.Header.Set("Content-Type", "application/json")
	claimReq.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	claimResp, claimErr := http.DefaultClient.Do(claimReq)
	if claimErr != nil {
		t.Fatal(claimErr)
	}
	claimResp.Body.Close()
	if claimResp.StatusCode != http.StatusOK {
		t.Fatalf("commit claim returned %s", claimResp.Status)
	}
	progressRaw := []byte(`{"sequence":1,"text":"partial answer","phase":"generating","busy":true}`)
	progressReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/progress", bytes.NewReader(progressRaw))
	progressReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	progressReq.Header.Set("Content-Type", "application/json")
	progressReq.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	progressResp, progressErr := http.DefaultClient.Do(progressReq)
	if progressErr != nil {
		t.Fatal(progressErr)
	}
	progressResp.Body.Close()
	if progressResp.StatusCode != http.StatusOK {
		t.Fatalf("progress update returned %s", progressResp.Status)
	}
	progressReq, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/progress", nil)
	progressReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	progressResp, progressErr = http.DefaultClient.Do(progressReq)
	if progressErr != nil {
		t.Fatal(progressErr)
	}
	var progress AdapterProgress
	if err := json.NewDecoder(progressResp.Body).Decode(&progress); err != nil {
		t.Fatal(err)
	}
	progressResp.Body.Close()
	if progress.Sequence != 1 || progress.Text != "partial answer" || !progress.Busy {
		t.Fatalf("unexpected adapter progress: %#v", progress)
	}

	decisionRaw := []byte(`{"verdict":"allow","flags":[],"confidence":0.9,"model":"test-ai"}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/complete", bytes.NewReader(decisionRaw))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("completion returned %s", resp.Status)
	}

	select {
	case submission := <-result:
		if submission.Decision == nil || submission.Decision.Verdict != "allow" {
			t.Fatalf("unexpected submission: %#v", submission)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submission did not complete")
	}
}

func TestAdapterNextEndpointHonorsRelaySelectedEndpoint(t *testing.T) {
	cfg := config.Config{
		Version:   1,
		Server:    config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage:   config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 5}},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	server.store.Queue(Job{ID: "endpoint-exact-endpoint", Prompt: "exact", ContextBridgeAdapterEndpointID: 42}, nil, time.Second)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	poll := func(endpoint string) (*http.Response, adapterJob) {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/adapter/jobs/next?wait=0&endpoint_id="+endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		var work adapterJob
		if response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&work); err != nil {
				t.Fatal(err)
			}
		}
		response.Body.Close()
		return response, work
	}
	if response, _ := poll("41"); response.StatusCode != http.StatusNoContent {
		t.Fatalf("wrong endpoint poll returned %s, want 204", response.Status)
	}
	response, work := poll("42")
	if response.StatusCode != http.StatusOK || work.Job.ContextBridgeAdapterEndpointID != 42 {
		t.Fatalf("designated endpoint did not receive pinned work: %s %#v", response.Status, work)
	}
	release, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/release", nil)
	release.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	release.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	released, err := http.DefaultClient.Do(release)
	if err != nil {
		t.Fatal(err)
	}
	released.Body.Close()
	if released.StatusCode != http.StatusOK {
		t.Fatalf("release returned %s", released.Status)
	}
	if response, _ := poll("41"); response.StatusCode != http.StatusNoContent {
		t.Fatalf("wrong endpoint leased returned work: %s", response.Status)
	}
	if response, _ := poll("invalid"); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed endpoint id returned %s, want 400", response.Status)
	}
}

func TestAutomaticUpdatePreferenceEndpoint(t *testing.T) {
	data := t.TempDir()
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: data, Inbox: t.TempDir()},
		Routes:  map[string]config.Route{"default": {Provider: "ollama"}},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := updater.New(cfg.Updates, data, "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	server.SetUpdater(manager)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	request, _ := http.NewRequest(http.MethodPut, httpServer.URL+"/v1/settings/updates", bytes.NewBufferString(`{"enabled":false}`))
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update preference returned %s", response.Status)
	}
	var status updater.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled || manager.LocalStatus().Enabled {
		t.Fatal("update preference was not persisted")
	}
}

func TestAdapterHeartbeatAndDashboardStatus(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "adapter", TimeoutSeconds: 5, AdapterProfile: "test"},
		},
		Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 5}},
		AdapterProfiles: map[string]config.AdapterProfile{
			"test": {Label: "Test adapter", Driver: "test"},
		},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	heartbeat := []byte(`{"state":"waiting","profile_label":"test","ready":true,"adapter_version":"1.0","adapter":"test","active_endpoints":1,"busy_endpoints":0,"endpoints":[{"id":42,"profile":"test","state":"waiting","current_model":"model-a","models":["model-a"],"last_failure":{"code":"adapter_unavailable","reason":"bounded","lease_reason":"renewal_rejected"}}]}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/adapter/heartbeat", bytes.NewReader(heartbeat))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat returned %s", resp.Status)
	}

	req, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var status struct {
		Adapter AdapterClientStatus `json:"adapter"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Adapter.Connected || !status.Adapter.Ready || status.Adapter.AdapterVersion != "1.0" {
		t.Fatalf("unexpected adapter status: %#v", status.Adapter)
	}
	if len(status.Adapter.Endpoints) != 1 || status.Adapter.Endpoints[0].ID != 42 || status.Adapter.Endpoints[0].CurrentModel != "model-a" {
		t.Fatalf("adapter endpoint was not reported: %#v", status.Adapter.Endpoints)
	}
}

func TestAdapterJSONOutputRoundTrip(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "adapter", TimeoutSeconds: 5},
		},
		Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 5}},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	result := make(chan Submission, 1)
	go func() {
		job := Job{
			Prompt: "Extract a summary", Text: "hello",
			Output: OutputSpec{Mode: "json", RequiredKeys: []string{"summary"}},
		}
		jobRaw, _ := json.Marshal(job)
		req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/jobs", bytes.NewReader(jobRaw))
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			return
		}
		defer resp.Body.Close()
		var submission Submission
		json.NewDecoder(resp.Body).Decode(&submission)
		result <- submission
	}()

	var work adapterJob
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/adapter/jobs/next?wait=0", nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr == nil && resp.StatusCode == http.StatusOK {
			json.NewDecoder(resp.Body).Decode(&work)
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if work.Job.ID == "" {
		t.Fatal("generic adapter job was not queued")
	}

	outputRaw := []byte(`{"mode":"json","json":{"summary":"Hello"},"model":"adapter-model"}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/adapter/jobs/"+work.Job.ID+"/complete", bytes.NewReader(outputRaw))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("completion returned %s", resp.Status)
	}

	select {
	case submission := <-result:
		if submission.Output == nil || string(submission.Output.JSON) != `{"summary":"Hello"}` {
			t.Fatalf("unexpected generic submission: %#v", submission)
		}
		if submission.Output.Model != "adapter-endpoint" {
			t.Fatalf("adapter supplied untrusted model metadata: %#v", submission.Output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generic submission did not complete")
	}
}

func TestExpiredAdapterLeaseCannotComplete(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "expired-job", Prompt: "Review"}
	store.Queue(job, nil, time.Second)
	work := store.NextAdapterJob("", time.Millisecond)
	if work == nil {
		t.Fatal("adapter job was not leased")
	}
	time.Sleep(5 * time.Millisecond)
	decision := ReviewDecision("adapter", "test", "late", 0)
	if store.Complete(job.ID, work.LeaseGeneration, Output{Mode: "decision", Decision: &decision}) {
		t.Fatal("an expired adapter lease must not accept a late result")
	}
}

func TestEmbeddingAndRAGRoundTrip(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		vectors := make([][]float32, len(request.Input))
		for index, input := range request.Input {
			if bytes.Contains([]byte(input), []byte("banana")) {
				vectors[index] = []float32{0, 1}
			} else {
				vectors[index] = []float32{1, 0}
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": vectors})
	}))
	defer ollama.Close()
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir(), Models: t.TempDir()},
		Routes: map[string]config.Route{
			"default":    {Provider: "ollama"},
			"embedding":  {Provider: "ollama", Task: "embedding"},
			"rag_ingest": {Provider: "ollama", Task: "rag_ingest"},
			"rag_query":  {Provider: "ollama", Task: "rag_query"},
		},
		Providers: config.Providers{Ollama: config.OllamaProvider{URL: ollama.URL, Model: "embed-test", Timeout: 5}},
		RAG:       config.RAG{Enabled: true, Backend: "local", Directory: t.TempDir(), EmbeddingRoute: "embedding", MaxDocuments: 10},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	post := func(payload interface{}) Submission {
		raw, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/jobs", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("request failed: %s %s", resp.Status, body)
		}
		var submission Submission
		if err := json.NewDecoder(resp.Body).Decode(&submission); err != nil {
			t.Fatal(err)
		}
		return submission
	}
	ingest := post(Job{Route: "rag_ingest", TenantID: "docs", Documents: []vectorstore.Document{{ID: "apple", Text: "apple document"}, {ID: "banana", Text: "banana document"}}})
	if ingest.Output == nil || ingest.Output.Indexed != 2 {
		t.Fatalf("unexpected ingest output: %#v", ingest)
	}
	query := post(Job{Route: "rag_query", TenantID: "docs", Query: "apple question", TopK: 1})
	if query.Output == nil || len(query.Output.Matches) != 1 || query.Output.Matches[0].ID != "apple" {
		t.Fatalf("unexpected query output: %#v", query)
	}
}

func TestTunnelHeartbeatAppearsInStatus(t *testing.T) {
	cfg := config.Config{Version: 1, Server: config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"}, Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()}, Routes: map[string]config.Route{"default": {Provider: "adapter"}}, Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 5}}}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	payload := []byte(`{"state":"connected","target":"admin@example.test","transport":"SSH with encrypted payloads","local_port":8788,"remote_port":8788}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/tunnel/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat returned %s", resp.Status)
	}
	if status := server.store.TunnelStatus(); !status.Connected || status.Target != "admin@example.test" {
		t.Fatalf("unexpected tunnel status: %#v", status)
	}
}
