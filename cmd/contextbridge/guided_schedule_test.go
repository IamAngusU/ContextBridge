package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestScheduleAddInteractiveSubmitsReviewedBytesOnce(t *testing.T) {
	var requests atomic.Int32
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/schedules" {
			http.NotFound(w, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer local-test-secret-123456789" {
			http.Error(w, "missing credential", http.StatusUnauthorized)
			return
		}
		received, _ = io.ReadAll(request.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"schedule-1","job":{"prompt":"server private prompt"}}`))
	}))
	defer server.Close()

	configPath, schedulePath, scheduleJSON := guidedScheduleFixture(t, server.URL)
	var output bytes.Buffer
	if err := scheduleCommandWithIO([]string{"add", "--config", configPath, "--interactive"}, strings.NewReader(schedulePath+"\ny\n"), &output, true); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("schedule requests = %d, want exactly one", requests.Load())
	}
	if !bytes.Equal(received, scheduleJSON) {
		t.Fatalf("submitted bytes changed after review: got %q want %q", received, scheduleJSON)
	}
	shown := output.String()
	for _, expected := range []string{
		"ContextBridge guided schedule creation",
		"file        " + schedulePath + "  [prompt]",
		"name        Morning summary",
		"timing      daily · timezone Europe/Berlin",
		"route       default · provider auto · model auto · reasoning auto",
		"base job + 1 follow-up step(s) · prompt/content hidden",
		"Created schedule schedule-1.",
	} {
		if !strings.Contains(shown, expected) {
			t.Fatalf("guided output missing %q: %s", expected, shown)
		}
	}
	if strings.Contains(shown, "private prompt") || strings.Contains(shown, "private follow-up") || strings.Contains(shown, "server private prompt") {
		t.Fatalf("guided output exposed schedule content: %s", shown)
	}
}

func TestScheduleAddInteractiveCancellationAndNonTTYHaveNoSideEffect(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	configPath, schedulePath, _ := guidedScheduleFixture(t, server.URL)

	var output bytes.Buffer
	if err := scheduleCommandWithIO([]string{"add", "--config", configPath, "--file", schedulePath, "--interactive"}, strings.NewReader("n\n"), &output, true); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || !strings.Contains(output.String(), "No schedule was created") {
		t.Fatalf("cancelled schedule had a side effect: requests=%d output=%q", requests.Load(), output.String())
	}

	output.Reset()
	err := scheduleCommandWithIO([]string{"add", "--config", configPath, "--file", schedulePath, "--interactive"}, strings.NewReader("y\n"), &output, false)
	if err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("non-TTY guided schedule error = %v", err)
	}
	if requests.Load() != 0 || output.Len() != 0 {
		t.Fatalf("non-TTY schedule prompted or sent a request: requests=%d output=%q", requests.Load(), output.String())
	}

	err = scheduleCommandWithIO([]string{"add", "--config", configPath, "--file", "-", "--interactive"}, strings.NewReader("y\n"), io.Discard, true)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("interactive stdin schedule error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid interactive stdin sent %d request(s)", requests.Load())
	}
}

func TestInspectGuidedScheduleRejectsUnsafeOrOversizedSummaryFields(t *testing.T) {
	if _, err := inspectGuidedSchedule([]byte(`{"name":"unsafe\u001b[2J","job":{},"timing":{"type":"daily"}}`)); err == nil {
		t.Fatal("terminal control in schedule name was accepted")
	}
	if _, err := inspectGuidedSchedule([]byte(`{"name":"test","job":{},"timing":{"type":"daily"},"steps":[{},{},{},{},{}]}`)); err == nil {
		t.Fatal("oversized follow-up set was accepted")
	}
}

func guidedScheduleFixture(t *testing.T, serverURL string) (string, string, []byte) {
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
	cfg.Server.Listen = strings.TrimPrefix(serverURL, "http://")
	cfg.Server.Token = "local-test-secret-123456789"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	scheduleJSON := []byte(`{"name":"Morning summary","job":{"route":"default","prompt":"private prompt","output":{"mode":"text"}},"timing":{"type":"daily","time":"08:00","timezone":"Europe/Berlin"},"steps":[{"name":"finish","job":{"prompt":"private follow-up","output":{"mode":"text"}}}]}`)
	schedulePath := filepath.Join(directory, "schedule.json")
	if err := os.WriteFile(schedulePath, scheduleJSON, 0600); err != nil {
		t.Fatal(err)
	}
	return configPath, schedulePath, scheduleJSON
}
