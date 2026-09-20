package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestDocumentedTrailingConfigWorksForResultAndSchedule(t *testing.T) {
	const token = "test_012345678901234567890123456789"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/v1/jobs/job-1":
			_, _ = writer.Write([]byte(`{"id":"job-1","status":"completed"}`))
		case "/v1/schedules/schedule-1":
			_, _ = writer.Write([]byte(`{"id":"schedule-1","status":"active"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server = config.Server{Listen: strings.TrimPrefix(server.URL, "http://"), Token: token}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := resultCommand([]string{"job-1", "--config", configPath}); err != nil {
		t.Fatalf("documented result ordering failed: %v", err)
	}
	if err := scheduleCommand([]string{"show", "schedule-1", "--config", configPath}); err != nil {
		t.Fatalf("documented schedule ordering failed: %v", err)
	}
}
