package bridge

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The browser heartbeat is intentionally decoded with unknown fields rejected.
// Keep the extension's bounded DOM diagnostics and the Go wire type in lockstep
// so a newly added diagnostic cannot turn a successful Connect into HTTP 400.
func TestExtensionDOMHeartbeatKeysExistInServerSchema(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate schema contract test")
	}
	sourcePath := filepath.Join(filepath.Dir(filename), "..", "..", "extension", "src", "background.js")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ReplaceAll(string(raw), "\r\n", "\n")
	start := strings.Index(source, "  return {\n    captured_at: new Date().toISOString(),")
	if start < 0 {
		t.Fatal("inspectPageDOM return object was not found")
	}
	end := strings.Index(source[start:], "\n  };\n}\n\nasync function discoverPageCapabilities")
	if end < 0 {
		t.Fatal("inspectPageDOM return object end was not found")
	}
	objectSource := source[start : start+end]

	serverKeys := map[string]bool{}
	typeOfSnapshot := reflect.TypeOf(BrowserDOMSnapshot{})
	for index := 0; index < typeOfSnapshot.NumField(); index++ {
		name := strings.Split(typeOfSnapshot.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			serverKeys[name] = true
		}
	}
	extensionKey := regexp.MustCompile(`(?m)^    ([a-z][a-z0-9_]*):`).FindAllStringSubmatch(objectSource, -1)
	if len(extensionKey) == 0 {
		t.Fatal("inspectPageDOM did not expose any heartbeat keys")
	}
	for _, match := range extensionKey {
		if !serverKeys[match[1]] {
			t.Errorf("extension DOM heartbeat key %q is absent from BrowserDOMSnapshot", match[1])
		}
	}
}
