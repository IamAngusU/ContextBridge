//go:build !windows

package updater

import (
	"context"
	"encoding/json"
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
