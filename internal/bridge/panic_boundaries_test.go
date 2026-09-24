package bridge

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func panicBoundaryTestServer(t *testing.T) *Server {
	t.Helper()
	directory := t.TempDir()
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: directory, Inbox: filepath.Join(directory, "inbox")},
		Routes:  map[string]config.Route{"default": {Provider: "ollama"}},
		Engines: map[string]config.Engine{"ollama": {Type: "ollama", URL: "http://127.0.0.1:11434"}},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestSchedulePanicBoundaryPersistsAmbiguousFailure(t *testing.T) {
	server := panicBoundaryTestServer(t)
	now := time.Now().UTC()
	item := Schedule{
		ID: "schedule-panic", Name: "panic", CurrentRunID: "run-panic", CreatedAt: now, UpdatedAt: now,
		History: []ScheduleRun{{ID: "run-panic", StartedAt: now, Outcome: "running"}},
	}
	if _, err := server.schedules.add(item); err != nil {
		t.Fatal(err)
	}
	const secret = "SCHEDULE-PANIC-VALUE-MUST-NOT-PERSIST"
	func() {
		defer server.recoverSchedulePanic(item.ID, item.CurrentRunID)
		panic(secret)
	}()
	stored, ok := server.schedules.get(item.ID)
	if !ok || stored.LastOutcome != "failed" || stored.CurrentRunID != "" || !strings.Contains(stored.LastError, "execution state is ambiguous") || strings.Contains(stored.LastError, secret) {
		t.Fatalf("panic did not become a bounded terminal schedule: %#v", stored)
	}
}

func TestInboxPanicBoundaryStoresTerminalResult(t *testing.T) {
	server := panicBoundaryTestServer(t)
	processing := filepath.Join(server.cfg.Storage.Inbox, "panic.processing.json")
	if err := os.WriteFile(processing, []byte(`{"prompt":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	const secret = "INBOX-PANIC-VALUE-MUST-NOT-PERSIST"
	func() {
		defer server.recoverInboxPanic(processing)
		panic(secret)
	}()
	if _, err := os.Stat(processing); !os.IsNotExist(err) {
		t.Fatalf("panic-marked processing file remains: %v", err)
	}
	resultPath := filepath.Join(server.cfg.Storage.Inbox, "panic.result.json")
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "execution state is ambiguous") || strings.Contains(string(raw), secret) {
		t.Fatalf("unsafe panic result: %s", raw)
	}
}
