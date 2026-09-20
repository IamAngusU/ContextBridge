package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestOllamaStatusKeepsLoadedEvidenceWhenShowIsSlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "test"})
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []map[string]interface{}{{
				"name": "loaded-model", "digest": "slow-loaded", "size": 100,
			}}})
		case "/api/ps":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []map[string]interface{}{{
				"name": "loaded-model", "size": 100, "size_vram": 80, "expires_at": "later",
			}}})
		case "/api/show":
			select {
			case <-request.Context().Done():
				return
			case <-time.After(2 * time.Second):
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"capabilities": []string{"completion"}})
			}
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	started := time.Now()
	status := ollamaStatus(t.Context(), "ollama", config.Engine{Type: "ollama", URL: server.URL})
	if elapsed := time.Since(started); elapsed >= 1500*time.Millisecond {
		t.Fatalf("slow show blocked bounded status refresh: %v", elapsed)
	}
	if status.State != "online" || len(status.Models) != 1 {
		t.Fatalf("runtime inventory missing: %#v", status)
	}
	model := status.Models[0]
	if !model.Available || !model.Loaded || model.VRAM != 80 || model.Affinity != "GPU and CPU" || status.Affinity != "GPU and CPU" {
		t.Fatalf("loaded evidence was lost behind slow capability probe: %#v", status)
	}
	if model.CapabilitiesVerified || model.CapabilitySource != "name_inference" {
		t.Fatalf("slow capability response became verified evidence: %#v", model)
	}
}
