package bridge

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestExternalOnlyRuntimeDoesNotProbeImplicitOllamaOrCreateModels(t *testing.T) {
	var externalRequests atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalRequests.Add(1)
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"external-model"}]}`)
	}))
	defer external.Close()

	var ollamaRequests atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ollamaRequests.Add(1)
		http.Error(w, "must not be contacted", http.StatusInternalServerError)
	}))
	defer ollama.Close()

	models := filepath.Join(t.TempDir(), "models")
	cfg := config.Config{
		Storage: config.Storage{Models: models},
		Routes:  map[string]config.Route{"default": {Provider: "external"}},
		Engines: map[string]config.Engine{
			"external": {Type: "openai_compatible", URL: external.URL + "/v1", Model: "external-model", Capabilities: []string{"text"}},
		},
	}
	cfg.Providers.Ollama.URL = ollama.URL

	manager := NewRuntimeManager(cfg, log.New(io.Discard, "", 0))
	if _, present := manager.engines["ollama"]; present {
		t.Fatal("external-only runtime inventory inserted an implicit Ollama engine")
	}
	manager.refreshExternalEngines(context.Background())
	status := manager.Snapshot(context.Background())
	engine, present := status.Engines["external"]
	if !present || engine.State != "online" || engine.LifecycleOwner != "external" {
		t.Fatalf("external runtime ownership/status = %#v", engine)
	}
	if externalRequests.Load() != 1 {
		t.Fatalf("external inventory requests = %d, want 1", externalRequests.Load())
	}
	if ollamaRequests.Load() != 0 {
		t.Fatalf("external-only configuration probed Ollama %d times", ollamaRequests.Load())
	}
	if _, err := os.Stat(models); !os.IsNotExist(err) {
		t.Fatalf("external-only discovery created or changed the managed model directory: %v", err)
	}
}

func TestRuntimeKeepsImplicitOllamaWhenARouteNeedsIt(t *testing.T) {
	for name, route := range map[string]config.Route{
		"primary":  {Provider: "ollama"},
		"fallback": {Provider: "external", Fallback: []string{"ollama"}},
	} {
		t.Run(name, func(t *testing.T) {
			manager := NewRuntimeManager(config.Config{Routes: map[string]config.Route{"default": route}}, log.New(io.Discard, "", 0))
			engine, present := manager.engines["ollama"]
			if !present || engine.Type != "ollama" || engine.LifecycleOwner != "external" {
				t.Fatalf("required implicit Ollama status = %#v, present %t", engine, present)
			}
		})
	}
}

func TestRuntimeCancellationLeavesExternalServiceRunning(t *testing.T) {
	var requests atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"external-model"}]}`)
	}))
	defer external.Close()

	cfg := config.Config{
		Runtime: config.Runtime{HardwareRefreshSeconds: 3600},
		Routes:  map[string]config.Route{"default": {Provider: "external"}},
		Engines: map[string]config.Engine{
			"external": {Type: "openai_compatible", URL: external.URL + "/v1", Model: "external-model", Capabilities: []string{"text"}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := NewRuntimeManager(cfg, log.New(io.Discard, "", 0))
	manager.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if requests.Load() == 0 {
		cancel()
		t.Fatal("external runtime was not observed before shutdown")
	}
	cancel()
	time.Sleep(20 * time.Millisecond)

	response, err := http.Get(external.URL + "/v1/models") // #nosec G107 -- test-owned loopback server.
	if err != nil {
		t.Fatalf("ContextBridge cancellation stopped an externally managed runtime: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("external runtime after ContextBridge cancellation returned %s", response.Status)
	}
}
