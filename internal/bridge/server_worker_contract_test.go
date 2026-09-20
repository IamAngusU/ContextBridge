package bridge

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestWorkerApprovedTaskCannotBeReplacedByPayloadRoute(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "ollama", Task: "generation"},
			"ingest":  {Provider: "ollama", Task: "rag_ingest"},
		},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	raw, err := json.Marshal(Job{
		ID:        "worker-task-contract",
		Route:     "ingest",
		Task:      "generation",
		TenantID:  "tenant-a",
		Documents: nil,
		Output:    OutputSpec{Mode: "text"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/jobs", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ContextBridge-Expected-Task", "generation")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched worker task and local route returned HTTP %d", response.StatusCode)
	}
}
