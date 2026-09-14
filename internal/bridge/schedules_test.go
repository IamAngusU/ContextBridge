package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestScheduleTiming(t *testing.T) {
	parse := func(value string) time.Time {
		result, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	base := parse("2026-09-14T08:00:00Z") // Monday
	cases := []struct {
		name        string
		timing      ScheduleTiming
		after, want time.Time
	}{
		{"interval", ScheduleTiming{Type: "interval", IntervalSeconds: 3600}, base.Add(3*time.Hour + 10*time.Minute), base.Add(4 * time.Hour)},
		{"daily", ScheduleTiming{Type: "daily", Time: "09:30", Timezone: "Europe/Berlin"}, parse("2026-09-14T06:00:00Z"), parse("2026-09-14T07:30:00Z")},
		{"weekdays", ScheduleTiming{Type: "weekdays", Time: "09:00", Timezone: "UTC"}, parse("2026-09-18T10:00:00Z"), parse("2026-09-21T09:00:00Z")},
		{"weekly", ScheduleTiming{Type: "weekly", Time: "09:00", Days: []string{"wed"}, Timezone: "UTC"}, base, parse("2026-09-16T09:00:00Z")},
		{"cron", ScheduleTiming{Type: "cron", Cron: "*/15 9 * * 1-5", Timezone: "UTC"}, parse("2026-09-14T09:14:00Z"), parse("2026-09-14T09:15:00Z")},
		{"spring DST skips nonexistent 02:30", ScheduleTiming{Type: "daily", Time: "02:30", Timezone: "Europe/Berlin"}, parse("2027-03-28T00:00:00Z"), parse("2027-03-29T00:30:00Z")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.timing.next(tc.after, base)
			if err != nil || !got.Equal(tc.want) {
				t.Fatalf("next=%s, err=%v; want %s", got, err, tc.want)
			}
		})
	}
	for _, bad := range []ScheduleTiming{{Type: "interval", IntervalSeconds: 1}, {Type: "weekly", Time: "09:00"}, {Type: "cron", Cron: "99 * * * *"}, {Type: "daily", Time: "25:99"}} {
		if _, err := bad.next(base, base); err == nil {
			t.Fatalf("accepted invalid timing: %+v", bad)
		}
	}
}

func TestScheduleClaimRestartNeverReplays(t *testing.T) {
	dir := t.TempDir()
	store, err := newScheduleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := Schedule{ID: "safe", Name: "safe", Job: Job{Prompt: "hello", Model: "GPT-5.6 Sol", Reasoning: "Sehr hoch"},
		Timing: ScheduleTiming{Type: "interval", IntervalSeconds: 3600}, Fallback: ScheduleFallback{Reasoning: []string{"Hoch"}}, Enabled: true,
		NextRun: now.Add(-time.Second), CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	if _, err := store.add(item); err != nil {
		t.Fatal(err)
	}
	claimed, job, err := store.claim("safe", now, false)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.CurrentRunID != job.ID || job.Metadata["contextbridge_reasoning_fallbacks"] == nil || job.Metadata["contextbridge_new_chat"] != true || job.Metadata["contextbridge_foreground_new_chat"] != true || !claimed.NextRun.After(now) {
		t.Fatalf("bad claim: %+v, %+v", claimed, job)
	}
	reopened, err := newScheduleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := reopened.get("safe")
	if loaded.CurrentRunID != "" || loaded.LastRunID != job.ID || loaded.LastOutcome != "interrupted" || !loaded.NextRun.After(now) {
		t.Fatalf("unsafe restart state: %+v", loaded)
	}
	if _, _, err := reopened.claim("safe", now, false); err == nil {
		t.Fatal("interrupted occurrence was replayed")
	}
	if len(loaded.History) != 1 || loaded.History[0].Outcome != "interrupted" {
		t.Fatalf("interrupted run was not retained in history: %+v", loaded.History)
	}
}

func TestSchedulePromptAndVerifiedArtifact(t *testing.T) {
	previous := Output{Text: "alpha", JSON: json.RawMessage(`{"answer":"alpha"}`)}
	result, context, err := schedulePrompt("Reply to {{previous.text}} using {{previous.json}}", previous)
	if err != nil || strings.Contains(result, "alpha") || !strings.Contains(result, "previous_result.text") || !strings.Contains(context, `"text":"alpha"`) {
		t.Fatalf("untrusted output entered trusted prompt: %q, %q: %v", result, context, err)
	}
	for _, template := range []string{"{{previous.missing}}", "{{previous.artifact_names}}"} {
		if _, _, err := schedulePrompt(template, previous); err == nil {
			t.Fatalf("accepted unavailable variable %q", template)
		}
	}
	bytes := []byte("%PDF-1.7\nverified bytes\n")
	digest := sha256.Sum256(bytes)
	artifact := Artifact{Name: "result.pdf", MediaType: "application/pdf", DataBase64: base64.StdEncoding.EncodeToString(bytes), SHA256: hex.EncodeToString(digest[:])}
	previous.Artifacts = []Artifact{artifact}
	if got, err := verifiedPreviousArtifact(previous, "file"); err != nil || got.Name != artifact.Name {
		t.Fatalf("verified artifact not accepted: %+v, %v", got, err)
	}
	previous.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if _, err := verifiedPreviousArtifact(previous, "file"); err == nil {
		t.Fatal("tampered artifact was handed to the next step")
	}
	previous.Artifacts[0].SHA256 = artifact.SHA256
	previous.Artifacts[0].DataBase64 = ""
	previous.Artifacts[0].URL = "https://example.invalid/result.pdf"
	if _, err := verifiedPreviousArtifact(previous, "file"); err == nil {
		t.Fatal("URL-only artifact was handed to the next step")
	}
}

func TestScheduleFileHandoffRequiresVerifiedBytes(t *testing.T) {
	cfg := config.Config{Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()}, Routes: map[string]config.Route{"default": {Provider: "browser", BrowserProfile: "chatgpt", TimeoutSeconds: 5}}}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("%PDF-1.7\nverified bytes\n")
	digest := sha256.Sum256(data)
	previous := Output{Artifacts: []Artifact{{Name: "result.pdf", MediaType: "application/pdf", DataBase64: base64.StdEncoding.EncodeToString(data), SHA256: hex.EncodeToString(digest[:])}}}
	base := Job{ID: "schedule-test-run", Route: "default", SessionID: "schedule-test", Metadata: map[string]interface{}{"contextbridge_foreground_new_chat": true}}
	step := ScheduleStep{Job: Job{Route: "default", Prompt: "Inspect the carried document.", Output: OutputSpec{Mode: "text"}}, UsePreviousArtifact: "file"}
	job, err := server.nextScheduleStep(base, step, 1, previous)
	if err != nil {
		t.Fatal(err)
	}
	file, ok := job.Metadata["contextbridge_input_file"].(map[string]string)
	if !ok || file["data_base64"] != previous.Artifacts[0].DataBase64 || job.SessionID != base.SessionID {
		t.Fatalf("verified artifact not carried safely: %+v", job)
	}
	previous.Artifacts[0].SHA256 = "wrong"
	if _, err := server.nextScheduleStep(base, step, 1, previous); err == nil {
		t.Fatal("unverified file was accepted for upload")
	}
}

func TestScheduleAPIAndStatusRedaction(t *testing.T) {
	cfg := config.Config{Server: config.Server{Token: "test-token"}, Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()}, Routes: map[string]config.Route{"default": {Provider: "browser", BrowserProfile: "chatgpt", TimeoutSeconds: 5}}}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request := func(method, path string, body []byte) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, httpServer.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	secret := "a private scheduled prompt"
	stepSecret := "a private follow-up prompt"
	input := map[string]interface{}{"name": "test schedule", "job": map[string]interface{}{"prompt": secret, "route": "default", "output": map[string]interface{}{"mode": "text"}}, "steps": []interface{}{map[string]interface{}{"job": map[string]interface{}{"prompt": stepSecret + " {{previous.text}}", "output": map[string]interface{}{"mode": "text"}}}}, "timing": map[string]interface{}{"type": "interval", "interval_seconds": 60}}
	raw, _ := json.Marshal(input)
	resp := request("POST", "/v1/schedules", raw)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: %s %s", resp.Status, data)
	}
	var item Schedule
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	resp = request("GET", "/v1/status", nil)
	statusRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(statusRaw), secret) || strings.Contains(string(statusRaw), stepSecret) {
		t.Fatal("prompt or follow-up leaked into routine status")
	}
	resp = request("POST", "/v1/schedules/"+item.ID+"/pause", nil)
	resp.Body.Close()
	loaded, _ := server.schedules.get(item.ID)
	if loaded.Enabled {
		t.Fatal("pause failed")
	}
	resp = request("POST", "/v1/schedules/"+item.ID+"/resume", nil)
	resp.Body.Close()
	loaded, _ = server.schedules.get(item.ID)
	if !loaded.Enabled {
		t.Fatal("resume failed")
	}
	server.schedules.waiting(item.ID, "browser_disconnected")
	server.dispatchSchedules(t.Context())
	loaded, _ = server.schedules.get(item.ID)
	if loaded.CurrentRunID != "" {
		t.Fatal("disconnected browser schedule was dispatched")
	}
	resp = request("DELETE", "/v1/schedules/"+item.ID, nil)
	resp.Body.Close()
	if _, ok := server.schedules.get(item.ID); ok {
		t.Fatal("delete failed")
	}
	if err := server.store.SaveOutput("saved-job", Output{Mode: "text", Text: "saved answer"}); err != nil {
		t.Fatal(err)
	}
	resp = request("GET", "/v1/jobs/saved-job", nil)
	resultRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(resultRaw, []byte("saved answer")) {
		t.Fatalf("result lookup failed: %s %s", resp.Status, resultRaw)
	}
}

func TestOutcomeBreakdownUsesRequestedSelection(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "one", Route: "default", Provider: "browser", Model: "GPT-5.6 Sol", Reasoning: "Sehr hoch"}
	store.RecordCompleted(job, Output{Mode: "text", Provider: "browser", Model: "browser", SelectedModel: "GPT-5.5", SelectedReasoning: "Hoch", Error: "browser_timeout"})
	metrics := store.Metrics()
	if metrics.JobsFailed != 1 || metrics.ByAttemptedProvider["browser"] != 1 || metrics.ByAttemptedModel["GPT-5.6 Sol"] != 1 || metrics.ModelFailures["GPT-5.6 Sol"] != 1 || metrics.ReasoningFailures["Sehr hoch"] != 1 || metrics.SelectionFailures["browser / GPT-5.6 Sol / Sehr hoch"] != 1 {
		t.Fatalf("incorrect breakdown: %+v", metrics)
	}
}

func TestDueScheduleDispatchesOnceAndPersistsResult(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected provider path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"done"}`))
	}))
	defer provider.Close()
	cfg := config.Config{Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()}, Routes: map[string]config.Route{"default": {Provider: "ollama", TimeoutSeconds: 5, Model: "test-model"}}, Providers: config.Providers{Ollama: config.OllamaProvider{URL: provider.URL, Timeout: 5}}}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := Schedule{ID: "due", Name: "due", Job: Job{Prompt: "hello", Route: "default", Output: OutputSpec{Mode: "text"}}, Timing: ScheduleTiming{Type: "interval", IntervalSeconds: 3600}, Enabled: true, NextRun: now.Add(-time.Second), CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	if _, err := server.schedules.add(item); err != nil {
		t.Fatal(err)
	}
	server.dispatchSchedules(t.Context())
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		loaded, _ := server.schedules.get("due")
		if loaded.Runs == 1 {
			if loaded.LastOutcome != "completed" || !loaded.NextRun.After(now) || server.store.Metrics().JobsTotal != 1 {
				t.Fatalf("bad completed run: %+v", loaded)
			}
			server.dispatchSchedules(t.Context())
			if server.store.Metrics().JobsTotal != 1 {
				t.Fatal("schedule ran a second time")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("due schedule did not complete")
}

func TestScheduleWorkflowUsesSavedPreviousResultAndHistory(t *testing.T) {
	var prompts []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("provider request: %v", err)
			return
		}
		prompts = append(prompts, input.Prompt)
		w.Header().Set("Content-Type", "application/json")
		if len(prompts) == 1 {
			_, _ = w.Write([]byte(`{"response":"CB40-FIRST"}`))
		} else {
			_, _ = w.Write([]byte(`{"response":"CB40-SECOND"}`))
		}
	}))
	defer provider.Close()
	dir := t.TempDir()
	cfg := config.Config{Storage: config.Storage{Directory: dir, Inbox: t.TempDir()}, Routes: map[string]config.Route{"default": {Provider: "ollama", TimeoutSeconds: 5, Model: "test-model"}}, Providers: config.Providers{Ollama: config.OllamaProvider{URL: provider.URL, Timeout: 5}}}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := Schedule{ID: "workflow", Name: "workflow", Job: Job{Prompt: "first", Route: "default", Output: OutputSpec{Mode: "text"}},
		Steps:  []ScheduleStep{{Name: "use answer", Job: Job{Prompt: "follow {{previous.text}}", Route: "default", Output: OutputSpec{Mode: "text"}}}},
		Timing: ScheduleTiming{Type: "interval", IntervalSeconds: 3600}, Enabled: true, NextRun: now.Add(-time.Second), CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	if _, err := server.schedules.add(item); err != nil {
		t.Fatal(err)
	}
	server.dispatchSchedules(t.Context())
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		loaded, _ := server.schedules.get("workflow")
		if loaded.Runs == 1 {
			if loaded.LastOutcome != "completed" || len(loaded.History) != 1 || len(loaded.History[0].Steps) != 2 || len(prompts) != 2 || !strings.Contains(prompts[1], "follow previous_result.text in submitted_content") || !strings.Contains(prompts[1], `"text":"CB40-FIRST"`) || strings.Index(prompts[1], "CB40-FIRST") < strings.Index(prompts[1], "<submitted_content>") {
				t.Fatalf("incorrect workflow: schedule=%+v prompts=%q", loaded, prompts)
			}
			reopened, err := newScheduleStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := reopened.get("workflow")
			if saved.LastOutcome != "completed" || len(saved.History) != 1 || len(saved.History[0].Steps) != 2 {
				t.Fatalf("workflow history lost on restart: %+v", saved)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workflow did not finish")
}
