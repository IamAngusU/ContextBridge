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
	if installedServiceIdle(context.Background(), cfg) {
		t.Fatal("active service should defer update")
	}
	idle = true
	if !installedServiceIdle(context.Background(), cfg) {
		t.Fatal("idle service should permit update")
	}
	cfg.Server.Listen = "0.0.0.0:32145"
	if installedServiceIdle(context.Background(), cfg) {
		t.Fatal("non-loopback health endpoint must not authorize update")
	}
}
