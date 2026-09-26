//go:build !windows

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixPendingRollbackRestoresPreviousExecutable(t *testing.T) {
	directory := t.TempDir()
	current := filepath.Join(directory, "contextbridge")
	failure := filepath.Join(directory, "update-failed.json")
	if err := os.WriteFile(current, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current+".previous", []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePendingUpdate(current, "v0.5.5", failure); err != nil {
		t.Fatal(err)
	}
	if err := rollbackPendingAt(current, "v0.5.5"); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(current)
	if err != nil || string(value) != "old" {
		t.Fatalf("previous version not restored: %q, %v", value, err)
	}
	if pendingUpdateFor(current, "v0.5.5") {
		t.Fatal("rollback marker was not cleared")
	}
	marker, err := os.ReadFile(failure)
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(marker, &recorded) != nil || recorded.Version != "v0.5.5" {
		t.Fatalf("failed version not quarantined: %s", marker)
	}
}

func TestUnixPendingHealthConfirmsUpdate(t *testing.T) {
	directory := t.TempDir()
	current := filepath.Join(directory, "contextbridge")
	if err := writePendingUpdate(current, "v0.5.5", filepath.Join(directory, "failed.json")); err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"version":"v0.5.5"}`))
	}))
	defer service.Close()
	manager := &Manager{executable: current, currentVersion: "v0.5.5", healthURL: service.URL}
	if err := manager.ConfirmStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pendingUpdateFor(current, "v0.5.5") {
		t.Fatal("healthy release stayed pending")
	}
}

func TestManagerRestartAndRollbackUseCapturedCanonicalExecutable(t *testing.T) {
	directory := t.TempDir()
	current := filepath.Join(directory, "contextbridge")
	failure := filepath.Join(directory, "failed.json")
	if err := os.WriteFile(current, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current+".previous", []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePendingUpdate(current, "v1.0.0", failure); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{executable: current}
	originalExec := restartExec
	defer func() { restartExec = originalExec }()
	called := ""
	restartExec = func(path string, _ []string, _ []string) error {
		called = path
		return errors.New("synthetic exec failure")
	}
	if err := manager.RestartCurrentProcess(); err == nil {
		t.Fatal("synthetic restart unexpectedly succeeded")
	}
	if called != current {
		t.Fatalf("restart used %q, want captured canonical path %q", called, current)
	}
	// restartCurrentProcessAt already restored the previous binary after the
	// synthetic exec failure. Re-arm the exact candidate state to verify the
	// manager rollback path independently.
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(current+".failed", current); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current+".previous", []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePendingUpdate(current, "v1.0.0", failure); err != nil {
		t.Fatal(err)
	}
	if err := manager.RollbackFailedStart("v1.0.0"); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(current)
	if err != nil || string(value) != "old" {
		t.Fatalf("manager rollback did not restore canonical binary: %q, %v", value, err)
	}
}
