package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestDoctorProviderReadiness(t *testing.T) {
	var status doctorStatus
	status.Runtime.Engines = map[string]struct {
		State    string `json:"state"`
		Warning  string `json:"warning"`
		Affinity string `json:"affinity"`
	}{
		"ollama": {State: "online"},
	}
	if !doctorProviderReady("ollama", status) || doctorProviderReady("adapter", status) {
		t.Fatal("provider readiness did not respect engine and adapter state")
	}
	status.Adapter.Connected = true
	status.Adapter.Ready = true
	if !doctorProviderReady("adapter", status) {
		t.Fatal("ready adapter provider was rejected")
	}
}

func TestDoctorEndToEndWithReadyAdapterRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/health":
			w.Write([]byte(`{"ok":true}`))
		case "/v1/status":
			if req.Header.Get("Authorization") != "Bearer test_012345678901234567890123456789" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"ok":true,"adapter":{"connected":true,"ready":true},"runtime":{"engines":{}}}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "config.yml")
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: strings.TrimPrefix(server.URL, "http://"), Token: "test_012345678901234567890123456789"},
		Routes:  map[string]config.Route{"default": {Provider: "adapter"}},
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	report := runDoctor(path)
	if !report.OK {
		t.Fatalf("ready setup failed doctor: %#v", report.Checks)
	}
}
