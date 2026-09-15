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

func TestModelControlDiagnosticsAreBounded(t *testing.T) {
	var heartbeat BrowserClientStatus
	if err := decodeJSON(bytes.NewBufferString(`{"state":"waiting","tabs":[{"id":42,"dom_status":"ready","dom":{"model_controls":[{"tag":"button","text":"Gemini Flash","visible":true}]}}]}`), &heartbeat, 128<<10); err != nil {
		t.Fatalf("the extension's bounded Gemini heartbeat must be accepted: %v", err)
	}
	if heartbeat.Tabs[0].DOMStatus != "ready" || heartbeat.Tabs[0].DOM.ModelControls[0].Text != "Gemini Flash" {
		t.Fatalf("Gemini model-control data was not decoded: %#v", heartbeat.Tabs)
	}
	controls := limitedModelControls([]BrowserDOMControl{{
		Tag: "button", ID: "model-picker", TestID: "bard-mode-menu-button",
		AriaLabel: "Modusauswahl öffnen, derzeit ausgewählt: Gemini Flash",
		Text:      "Gemini Flash", Type: "button", Accept: "private", Visible: true,
	}, {
		Tag: "button", ID: "prompt: secret", TestID: "secret private data",
		AriaLabel: "private prompt", Text: "private prompt", Visible: true,
	}}, 12)
	if len(controls) != 2 || controls[0].Text != "Gemini Flash" || controls[0].ID != "model-picker" {
		t.Fatalf("expected bounded Gemini mode metadata: %#v", controls)
	}
	if controls[0].AriaLabel != "" || controls[0].Accept != "" || controls[0].Type != "" ||
		controls[1].ID != "" || controls[1].TestID != "" || controls[1].Text != "" {
		t.Fatalf("private page metadata escaped the model-control boundary: %#v", controls)
	}
}

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
			"default": {Provider: "browser", TimeoutSeconds: 30},
		},
		Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 5}},
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
	// workers remain blocked in the browser provider.
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

func TestBrowserJobRoundTrip(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "browser", TimeoutSeconds: 5, BrowserProfile: "test"},
		},
		Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 5}},
		BrowserProfiles: map[string]config.BrowserProfile{
			"test": {
				Label: "Test AI", MatchURL: "https://example.test/*",
				Selectors: config.Selectors{Input: []string{"textarea"}, Submit: []string{"button"}, Response: []string{".answer"}},
			},
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

	var work browserJob
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/browser/jobs/next?wait=0&profile=test", nil)
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
		t.Fatal("browser job was not queued")
	}
	if work.Job.ContextBridgeSessionKey != "cb:producer-scoped-test" {
		t.Fatal("worker-derived session scope was lost before reaching the browser extension")
	}
	leaseReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/jobs/"+work.Job.ID+"/lease", nil)
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
	claimReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/jobs/"+work.Job.ID+"/claim", bytes.NewReader([]byte(`{"action":"send"}`)))
	claimReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	claimReq.Header.Set("Content-Type", "application/json")
	claimReq.Header.Set("X-ContextBridge-Lease-Generation", strconv.FormatUint(work.LeaseGeneration, 10))
	claimResp, claimErr := http.DefaultClient.Do(claimReq)
	if claimErr != nil {
		t.Fatal(claimErr)
	}
	claimResp.Body.Close()
	if claimResp.StatusCode != http.StatusOK {
		t.Fatalf("send claim returned %s", claimResp.Status)
	}
	progressRaw := []byte(`{"sequence":1,"text":"partial answer","phase":"generating","busy":true}`)
	progressReq, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/jobs/"+work.Job.ID+"/progress", bytes.NewReader(progressRaw))
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
	progressReq, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/browser/jobs/"+work.Job.ID+"/progress", nil)
	progressReq.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	progressResp, progressErr = http.DefaultClient.Do(progressReq)
	if progressErr != nil {
		t.Fatal(progressErr)
	}
	var progress BrowserProgress
	if err := json.NewDecoder(progressResp.Body).Decode(&progress); err != nil {
		t.Fatal(err)
	}
	progressResp.Body.Close()
	if progress.Sequence != 1 || progress.Text != "partial answer" || !progress.Busy {
		t.Fatalf("unexpected browser progress: %#v", progress)
	}

	decisionRaw := []byte(`{"verdict":"allow","flags":[],"confidence":0.9,"model":"test-ai"}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/jobs/"+work.Job.ID+"/complete", bytes.NewReader(decisionRaw))
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

func TestBrowserHeartbeatAndDashboardStatus(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "browser", TimeoutSeconds: 5, BrowserProfile: "test"},
		},
		Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 5}},
		BrowserProfiles: map[string]config.BrowserProfile{
			"test": {Label: "Test AI", MatchURL: "https://example.test/*"},
		},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	heartbeat := []byte(`{"state":"waiting","origin":"https://example.test","tab_title":"Test tab","profile_label":"Visual test","selectors_ready":true,"extension_version":"0.2.0","browser":"firefox","tabs":[{"id":42,"title":"Test tab","model_scan":"composer trigger, expanded=true, submenu=false, 10 candidates, open=pointer","reasoning_scan":"private page text","last_failure":{"code":"browser_submit_unavailable","reason":"raw private page content"},"dom":{"page_visibility":"hidden","was_discarded":true,"inputs":[{"tag":"div","id":"prompt","visible":true}],"input_has_text":true,"input_characters":999999,"last_response_characters":999999,"gemini_user_turns":999999,"gemini_last_user_turn_characters":999999,"gemini_last_user_turn_has_content_id":true,"gemini_response_after_last_user_turn":true,"busy_indicators":["aria_busy","stop_button","aria_busy","streaming_attribute","image_loading","one too many"],"stop_button_disabled":true,"stop_button_spinning":true,"file_inputs":[{"tag":"input","id":"upload-photos","type":"file","accept":"image/*","visible":false}],"assistant_turns":3,"last_response_images":2,"last_response_loaded_images":999999}}]}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/heartbeat", bytes.NewReader(heartbeat))
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
		Browser BrowserClientStatus `json:"browser"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Browser.Connected || status.Browser.TabTitle != "Test tab" {
		t.Fatalf("unexpected browser status: %#v", status.Browser)
	}
	if len(status.Browser.Tabs) != 1 || status.Browser.Tabs[0].DOM == nil || status.Browser.Tabs[0].DOM.FileInputs[0].ID != "upload-photos" {
		t.Fatalf("selector diagnostics were not reported: %#v", status.Browser.Tabs)
	}
	if status.Browser.Tabs[0].LastFailure.Reason != "other" || status.Browser.Tabs[0].DOM.InputCharacters != 100000 || !status.Browser.Tabs[0].DOM.InputHasText {
		t.Fatalf("browser diagnostics were not safely bounded: %#v", status.Browser.Tabs[0])
	}
	if status.Browser.Tabs[0].ModelScan != "composer trigger, expanded=true, submenu=false, 10 candidates, open=pointer" || status.Browser.Tabs[0].ReasoningScan != "" {
		t.Fatalf("model scan diagnostics were not safely bounded: %#v", status.Browser.Tabs[0])
	}
	if dom := status.Browser.Tabs[0].DOM; dom.PageVisibility != "hidden" || !dom.WasDiscarded || dom.LastResponseCharacters != 100000 || dom.GeminiUserTurns != 10000 || dom.GeminiLastUserTurnCharacters != 100000 || !dom.GeminiLastUserTurnHasContentID || !dom.GeminiResponseAfterLastUserTurn || dom.LastResponseLoadedImages != 100 || len(dom.BusyIndicators) != 4 || dom.BusyIndicators[0] != "aria_busy" || !dom.StopButtonDisabled || !dom.StopButtonSpinning {
		t.Fatalf("busy diagnostics were not safely bounded: %#v", dom)
	}
	invalidVisibility := bytes.Replace(heartbeat, []byte(`"page_visibility":"hidden"`), []byte(`"page_visibility":"private page text"`), 1)
	req, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/heartbeat", bytes.NewReader(invalidVisibility))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bounded page-visibility heartbeat returned %s", resp.Status)
	}
	if visibility := server.store.BrowserStatus().Tabs[0].DOM.PageVisibility; visibility != "" {
		t.Fatalf("unrecognized page visibility was retained: %q", visibility)
	}
	for _, reason := range []string{"upload_input_missing", "upload_preview_missing", "attachment_busy", "submitted_prompt_unverified", "local_bridge_unavailable"} {
		updated := bytes.Replace(heartbeat, []byte(`"raw private page content"`), []byte(`"`+reason+`"`), 1)
		req, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/heartbeat", bytes.NewReader(updated))
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("upload diagnostic heartbeat returned %s", resp.Status)
		}
		req, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var updatedStatus struct {
			Browser BrowserClientStatus `json:"browser"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&updatedStatus); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if updatedStatus.Browser.Tabs[0].LastFailure.Reason != reason {
			t.Fatalf("bounded upload reason %q was not retained: %#v", reason, updatedStatus.Browser.Tabs[0].LastFailure)
		}
	}

	resp, err = http.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("ContextBridge")) {
		t.Fatalf("dashboard was not served: %s", resp.Status)
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("dashboard security headers are missing")
	}
}

func TestBrowserJSONOutputRoundTrip(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "browser", TimeoutSeconds: 5},
		},
		Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 5}},
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

	var work browserJob
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/browser/jobs/next?wait=0", nil)
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
		t.Fatal("generic browser job was not queued")
	}

	outputRaw := []byte(`{"mode":"json","json":{"summary":"Hello"},"model":"browser-model"}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/jobs/"+work.Job.ID+"/complete", bytes.NewReader(outputRaw))
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
		if submission.Output.Model != "browser-tab" {
			t.Fatalf("browser supplied untrusted model metadata: %#v", submission.Output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generic submission did not complete")
	}
}

func TestExpiredBrowserLeaseCannotComplete(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "expired-job", Prompt: "Review"}
	store.Queue(job, nil, time.Second)
	work := store.NextBrowserJob("", time.Millisecond)
	if work == nil {
		t.Fatal("browser job was not leased")
	}
	time.Sleep(5 * time.Millisecond)
	decision := ReviewDecision("browser", "test", "late", 0)
	if store.Complete(job.ID, work.LeaseGeneration, Output{Mode: "decision", Decision: &decision}) {
		t.Fatal("an expired browser lease must not accept a late result")
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
	cfg := config.Config{Version: 1, Server: config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"}, Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()}, Routes: map[string]config.Route{"default": {Provider: "browser"}}, Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 5}}}
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
