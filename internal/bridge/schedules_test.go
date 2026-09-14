package bridge

import (
	"bytes"
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
	input := map[string]interface{}{"name": "test schedule", "job": map[string]interface{}{"prompt": secret, "route": "default", "output": map[string]interface{}{"mode": "text"}}, "timing": map[string]interface{}{"type": "interval", "interval_seconds": 60}}
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
	if strings.Contains(string(statusRaw), secret) {
		t.Fatal("prompt leaked into routine status")
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
