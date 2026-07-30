package updater

import "testing"

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

func TestReleaseAssetName(t *testing.T) {
	if value := releaseAssetName("windows", "amd64"); value != "contextbridge_windows_amd64.zip" {
		t.Fatal(value)
	}
	if value := releaseAssetName("linux", "arm64"); value != "contextbridge_linux_arm64.tar.gz" {
		t.Fatal(value)
	}
}
