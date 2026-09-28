package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestConfigManagementAPIIsAuthenticatedLocalAndRevisionSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: cfg}
	server.SetConfigPath(path)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/config", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/config", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var managed config.ManagedConfig
	if err := json.NewDecoder(response.Body).Decode(&managed); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || strings.Contains(managed.YAML, cfg.Server.Token) {
		t.Fatalf("unsafe managed response: status=%d body=%#v", response.StatusCode, managed)
	}

	request, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/config/schema", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Constraints []map[string]interface{} `json:"constraints"`
	}
	if err := json.NewDecoder(response.Body).Decode(&schema); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	foundWorkerLimit := false
	for _, field := range schema.Constraints {
		if field["path"] == "cluster.worker.max_concurrent" && field["maximum"] != nil {
			foundWorkerLimit = true
		}
	}
	if response.StatusCode != http.StatusOK || !foundWorkerLimit {
		t.Fatalf("config constraint catalog is incomplete: status=%d constraints=%d", response.StatusCode, len(schema.Constraints))
	}

	proposed := strings.Replace(managed.YAML, "style: panel", "style: classic", 1)
	body, _ := json.Marshal(map[string]string{"yaml": proposed, "base_revision": managed.Revision})
	request, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/v1/config", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("validate status = %d", response.StatusCode)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote config access status = %d", recorder.Code)
	}
}
