package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestLocalControlURLAcceptsOnlyLoopbackListeners(t *testing.T) {
	for _, test := range []struct {
		address string
		want    string
	}{
		{"127.0.0.1:32145", "http://127.0.0.1:32145/v1/system/stop"},
		{"localhost:32145", "http://127.0.0.1:32145/v1/system/stop"},
		{"0.0.0.0:32145", "http://127.0.0.1:32145/v1/system/stop"},
		{"[::1]:32145", "http://[::1]:32145/v1/system/stop"},
		{"[::]:32145", "http://127.0.0.1:32145/v1/system/stop"},
	} {
		got, err := localControlURL(test.address)
		if err != nil || got != test.want {
			t.Errorf("localControlURL(%q) = %q, %v; want %q", test.address, got, err, test.want)
		}
	}
	for _, address := range []string{"example.com:32145", "192.0.2.10:32145", "not-an-address"} {
		if got, err := localControlURL(address); err == nil {
			t.Errorf("localControlURL(%q) unexpectedly accepted %q", address, got)
		}
	}
}

func TestRequestContextBridgeStopSendsAuthenticatedForceRequest(t *testing.T) {
	const token = "correct-local-control-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != localStopPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		var request struct {
			Force bool `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Force {
			t.Errorf("force payload = %+v, %v", request, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"stopping":true,"forced":true}`))
	}))
	defer server.Close()

	if err := requestContextBridgeStop(context.Background(), http.DefaultClient, server.URL+localStopPath, token, true); err != nil {
		t.Fatal(err)
	}
}

func TestRequestContextBridgeStopPreservesBusyFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"ContextBridge is busy; retry with --force"}`))
	}))
	defer server.Close()
	err := requestContextBridgeStop(context.Background(), http.DefaultClient, server.URL, "token", false)
	if err == nil || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("busy response error = %v", err)
	}
}

func TestStopCommandLoadsConfigAndCallsRunningLocalService(t *testing.T) {
	const token = "test-stop-command-token-long-enough"
	forceSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("wrong stop bearer token")
		}
		var request struct {
			Force bool `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		forceSeen = request.Force
		_, _ = w.Write([]byte(`{"ok":true,"stopping":true,"forced":true}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Listen = server.Listener.Addr().String()
	cfg.Server.Token = token
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := stopCommand([]string{"--config", path, "--force"}); err != nil {
		t.Fatal(err)
	}
	if !forceSeen {
		t.Fatal("stop command did not forward --force")
	}
}

func TestStopCommandRejectsUnexpectedArgumentsBeforeLoadingConfig(t *testing.T) {
	if err := stopCommand([]string{"unexpected"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("unexpected argument error = %v", err)
	}
}
