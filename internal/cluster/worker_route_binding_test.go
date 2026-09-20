package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkerResolvesExplicitProviderToOperatorPrimaryRoute(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status" || request.Header.Get("Authorization") != "Bearer local-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"routes": map[string]interface{}{
				"default":  map[string]interface{}{"provider": "ollama", "task": "generation"},
				"deepseek": map[string]interface{}{"provider": "deepseek", "task": "generation"},
				"fallback": map[string]interface{}{"provider": "ollama", "fallback": []string{"deepseek"}, "task": "generation"},
			},
		})
	}))
	defer local.Close()
	worker := &Worker{cfg: WorkerConfig{LocalURL: local.URL, LocalToken: "local-secret"}, client: local.Client()}
	route, err := worker.resolveLocalPrimaryRoute(context.Background(), Requirements{Provider: "deepseek", Task: "generation", Model: "deepseek-flash"})
	if err != nil || route != "deepseek" {
		t.Fatalf("explicit provider did not bind to its primary route: route=%q err=%v", route, err)
	}
	bound, err := bindLocalRoute([]byte(`{"route":"forged","prompt":"safe"}`), route)
	if err != nil || !strings.Contains(string(bound), `"route":"deepseek"`) || strings.Contains(string(bound), "forged") {
		t.Fatalf("authenticated route did not replace payload hint: %s err=%v", bound, err)
	}
}

func TestWorkerRouteBindingFailsClosedWithoutMatchingPrimaryRoute(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"routes": map[string]interface{}{
				"default": map[string]interface{}{"provider": "ollama", "fallback": []string{"deepseek"}, "task": "generation"},
			},
		})
	}))
	defer local.Close()
	worker := &Worker{cfg: WorkerConfig{LocalURL: local.URL}, client: local.Client()}
	if _, err := worker.resolveLocalPrimaryRoute(context.Background(), Requirements{Provider: "deepseek", Task: "generation"}); err == nil || !strings.Contains(err.Error(), "provider_not_bound_to_local_route") {
		t.Fatalf("fallback-only provider binding was accepted: %v", err)
	}
}
