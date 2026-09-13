package updater

import (
	"context"
	"os"
	"testing"
	"time"
)

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
	if !settings.DefaultEnabled() || settings.Channel != "stable" || settings.CheckIntervalHours != 24 {
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
	manager, err := New(Settings{}, t.TempDir(), "v0.5.4")
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
	manager, err := New(Settings{}, t.TempDir(), "v0.5.4")
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
