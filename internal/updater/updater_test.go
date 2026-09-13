package updater

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAcquireLockRecoversExitedOwner(t *testing.T) {
	manager, err := New(Settings{}, t.TempDir(), "v0.5.9")
	if err != nil {
		t.Fatal(err)
	}
	path := manager.dataDir + string(os.PathSeparator) + "update.lock"
	if err := os.WriteFile(path, []byte("2147483000\n2026-09-13T12:00:00Z\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-4 * time.Minute)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := manager.acquireLock(ctx)
	if err != nil {
		t.Fatalf("dead update owner should not block: %v", err)
	}
	unlock()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock should be released: %v", err)
	}
}

func TestAcquireLockKeepsLiveOwner(t *testing.T) {
	manager, err := New(Settings{}, t.TempDir(), "v0.5.9")
	if err != nil {
		t.Fatal(err)
	}
	path := manager.dataDir + string(os.PathSeparator) + "update.lock"
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := manager.acquireLock(ctx); err == nil {
		t.Fatal("live owner lock was stolen")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live lock disappeared: %v", err)
	}
}

func TestLocalStatusDoesNotWaitForUpdateMutex(t *testing.T) {
	manager, err := New(Settings{}, t.TempDir(), "v0.5.9")
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	done := make(chan struct{})
	go func() { _ = manager.LocalStatus(); close(done) }()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("status blocked by an ongoing update")
	}
}

func TestNewerVersion(t *testing.T) {
	tests := []struct {
		current, candidate string
		newer              bool
	}{
		{"v0.1.1", "v0.2.0", true},
		{"v1.2.3", "v1.2.3", false},
		{"v2.0.0", "v1.9.9", false},
		{"dev", "v1.0.0", false},
	}
	for _, test := range tests {
		if actual := newerVersion(test.current, test.candidate); actual != test.newer {
			t.Fatalf("newerVersion(%q, %q) = %v", test.current, test.candidate, actual)
		}
	}
}

func TestSettingsDefaultAndOverride(t *testing.T) {
	settings := Settings{}
	settings.ApplyDefaults()
	if settings.DefaultEnabled() || settings.Channel != "stable" || settings.CheckIntervalHours != 24 {
		t.Fatalf("unexpected defaults: %#v", settings)
	}
	manager, err := New(settings, t.TempDir(), "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatal("expected persisted override to disable updates")
	}
	status, err = manager.SetEnabled(true)
	if err != nil || !status.Enabled {
		t.Fatalf("local opt-in did not enable updates: %+v, %v", status, err)
	}
}

func TestAutomaticUpdateIsInertByDefault(t *testing.T) {
	manager, err := New(Settings{Repository: "no-such-owner/no-such-repository"}, t.TempDir(), "v0.5.20")
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Auto(context.Background())
	if err != nil || result.Status.Enabled || !result.Status.LastChecked.IsZero() {
		t.Fatalf("disabled automatic update should not check the network: %+v, %v", result, err)
	}
}

func TestConfigDisabledCannotBeOverriddenByState(t *testing.T) {
	disabled := false
	manager, err := New(Settings{Enabled: &disabled}, t.TempDir(), "v0.5.4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if manager.LocalStatus().Enabled {
		t.Fatal("updates.enabled: false must always disable automatic updates")
	}
}

func TestReleaseAssetName(t *testing.T) {
	if value := releaseAssetName("windows", "amd64"); value != "contextbridge_windows_amd64.zip" {
		t.Fatal(value)
	}
	if value := releaseAssetName("linux", "arm64"); value != "contextbridge_linux_arm64.tar.gz" {
		t.Fatal(value)
	}
}

func TestAutomaticUpdateDefersActiveJobs(t *testing.T) {
	enabled := true
	manager, err := New(Settings{Enabled: &enabled}, t.TempDir(), "v0.5.4")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveState(State{LastChecked: time.Now().UTC(), LastAvailable: "v0.5.5"}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	manager.SetIdleCheck(func(context.Context) bool { checks++; return false })
	result, err := manager.Auto(context.Background())
	if err != nil || result.Applied || !result.Status.UpdateAvailable || checks != 1 {
		t.Fatalf("busy update should be deferred: result=%+v err=%v checks=%d", result, err, checks)
	}
}

func TestFailedVersionIsNotRetriedAutomatically(t *testing.T) {
	enabled := true
	manager, err := New(Settings{Enabled: &enabled}, t.TempDir(), "v0.5.4")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveState(State{LastChecked: time.Now().UTC(), LastAvailable: "v0.5.5", LastInstalled: "v0.5.5"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.failurePath(), []byte(`{"version":"v0.5.5","error":"startup failed; previous version restored"}`), 0600); err != nil {
		t.Fatal(err)
	}
	manager.SetIdleCheck(func(context.Context) bool { return true })
	result, err := manager.Auto(context.Background())
	if err != nil || result.Applied || result.Status.LastInstalled != "v0.5.4" || result.Status.LastError == "" {
		t.Fatalf("failed release must be quarantined: result=%+v err=%v", result, err)
	}
}

func TestHealthyUpgradeSupersedesOlderRollbackMarker(t *testing.T) {
	manager, err := New(Settings{}, t.TempDir(), "v0.5.21")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveState(State{LastInstalled: "v0.5.20", LastError: "old failed install"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.failurePath(), []byte(`{"version":"v0.5.20","error":"old failed install"}`), 0600); err != nil {
		t.Fatal(err)
	}
	status := manager.LocalStatus()
	if status.BlockedVersion != "" || status.LastError != "" || status.LastInstalled != "v0.5.21" {
		t.Fatalf("healthy newer release still shows old failure: %+v", status)
	}
	if _, err := os.Stat(manager.failurePath()); err != nil {
		t.Fatalf("historical rollback marker was removed: %v", err)
	}
}

func TestAutomaticRetryUsesBoundedBackoff(t *testing.T) {
	state := State{}
	for attempt := 0; attempt < 12; attempt++ {
		scheduleRetry(&state)
		if state.Failures < 1 || state.Failures > 8 {
			t.Fatalf("invalid failure count: %d", state.Failures)
		}
		remaining := time.Until(state.RetryAfter)
		if remaining < 14*time.Minute || remaining > 24*time.Hour {
			t.Fatalf("retry delay out of bounds: %s", remaining)
		}
	}
}
