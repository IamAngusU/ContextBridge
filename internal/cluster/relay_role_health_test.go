package cluster

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRelayRoleHealthSeparatesLiveReadyAndWritable(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:   filepath.Join(t.TempDir(), "relay.db"),
		AdminToken: "admin_role_health_012345678901234567890123",
		Version:    "v-test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	assertRelayRoleHealth(t, server.URL+"/livez", http.StatusOK, map[string]interface{}{
		"ok": true, "live": true,
	})
	assertRelayRoleHealth(t, server.URL+"/readyz", http.StatusOK, map[string]interface{}{
		"ok": true, "ready": true, "leader": true, "mode": "standalone",
	})
	assertRelayRoleHealth(t, server.URL+"/leaderz", http.StatusOK, map[string]interface{}{
		"ok": true, "leader": true, "writable": true, "mode": "standalone",
	})

	if !relay.QuiesceForStop(true) {
		t.Fatal("forced quiesce was rejected")
	}
	assertRelayRoleHealth(t, server.URL+"/livez", http.StatusOK, map[string]interface{}{
		"ok": true, "live": true,
	})
	assertRelayRoleHealth(t, server.URL+"/readyz", http.StatusServiceUnavailable, map[string]interface{}{
		"ok": false, "ready": false, "leader": true, "mode": "standalone",
	})
	assertRelayRoleHealth(t, server.URL+"/leaderz", http.StatusServiceUnavailable, map[string]interface{}{
		"ok": false, "leader": true, "writable": false, "mode": "standalone",
	})
}

func TestRelayRoleHealthFailsClosedWhenStoreIsUnavailable(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:   filepath.Join(t.TempDir(), "relay.db"),
		AdminToken: "admin_role_store_0123456789012345678901234",
		Version:    "v-test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}

	assertRelayRoleHealth(t, server.URL+"/livez", http.StatusOK, map[string]interface{}{
		"ok": true, "live": true,
	})
	assertRelayRoleHealth(t, server.URL+"/readyz", http.StatusServiceUnavailable, map[string]interface{}{
		"ok": false, "ready": false, "leader": true, "mode": "standalone",
	})
	assertRelayRoleHealth(t, server.URL+"/leaderz", http.StatusServiceUnavailable, map[string]interface{}{
		"ok": false, "leader": true, "writable": false, "mode": "standalone",
	})
}

func assertRelayRoleHealth(t *testing.T, url string, expectedStatus int, expected map[string]interface{}) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		t.Fatalf("%s returned %s, want %d", url, response.Status, expectedStatus)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for key, wanted := range expected {
		if body[key] != wanted {
			t.Fatalf("%s field %s = %#v, want %#v (body %#v)", url, key, body[key], wanted, body)
		}
	}
	for _, forbidden := range []string{"overview", "nodes", "jobs", "tokens", "database"} {
		if _, present := body[forbidden]; present {
			t.Fatalf("%s leaked %s: %#v", url, forbidden, body)
		}
	}
}
