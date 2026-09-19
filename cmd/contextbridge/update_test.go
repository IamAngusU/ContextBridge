package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestScheduledUpdaterRequiresIdleService(t *testing.T) {
	idle := false
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"ok":true,"idle":%t}`, idle)
	}))
	defer service.Close()
	address := strings.TrimPrefix(service.URL, "http://")
	cfg := config.Config{Server: config.Server{Listen: address}}
	if installedServiceIdle(context.Background(), cfg, false) {
		t.Fatal("active service should defer update")
	}
	idle = true
	if !installedServiceIdle(context.Background(), cfg, false) {
		t.Fatal("idle service should permit update")
	}
	cfg.Server.Listen = "192.0.2.1:32145"
	if installedServiceIdle(context.Background(), cfg, false) {
		t.Fatal("non-loopback health endpoint must not authorize update")
	}
	cfg.Cluster.Relay.Enabled = true
	cfg.Cluster.Relay.Listen = address
	if !installedServiceIdle(context.Background(), cfg, true) {
		t.Fatal("privileged relay updater should use relay health without requiring a local bridge")
	}
}

func TestManagedServiceNameIsConstrained(t *testing.T) {
	for _, name := range []string{"contextbridge-relay.service", "contextbridge@worker.service"} {
		if !validManagedService(name) {
			t.Fatalf("valid service rejected: %s", name)
		}
	}
	for _, name := range []string{"ssh.service", "contextbridge-relay.service;reboot", "contextbridge/relay.service"} {
		if validManagedService(name) {
			t.Fatalf("unsafe service accepted: %s", name)
		}
	}
}

func TestUpdateAppliedMessageDistinguishesActivationState(t *testing.T) {
	if message := updateAppliedMessage(true, "linux"); !strings.Contains(message, "--managed-service") {
		t.Fatalf("Linux restart guidance missing managed-service flag: %q", message)
	}
	if message := updateAppliedMessage(true, "windows"); !strings.Contains(message, "Windows update helper") {
		t.Fatalf("Windows restart handoff was not explained: %q", message)
	}
	if message := updateAppliedMessage(false, "linux"); !strings.Contains(message, "running the new version") {
		t.Fatalf("completed managed restart was not reported as active: %q", message)
	}
}
