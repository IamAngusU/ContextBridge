package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestOpenAICompatibleProviderUsesAuthAndTrustedModel(t *testing.T) {
	var receivedModel, receivedPrompt string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer provider-secret" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, request)
			return
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		receivedModel = payload.Model
		if len(payload.Messages) > 0 && len(payload.Messages[0].Content) > 0 {
			receivedPrompt = payload.Messages[0].Content[0].Text
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": "REMOTE-OK"}}},
			"usage":   map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	defer provider.Close()
	cfg := config.Config{
		Routes:  map[string]config.Route{"default": {Provider: "remote", Model: "trusted-model"}},
		Engines: map[string]config.Engine{"remote": {Type: "openai_compatible", URL: provider.URL + "/v1", Model: "trusted-model", APIKey: "provider-secret", Capabilities: []string{"text"}, TimeoutSeconds: 5}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "reply exactly", Text: "untrusted", Output: OutputSpec{Mode: "text"}})
	if output.Error != "" || output.Text != "REMOTE-OK" || output.Provider != "remote" || output.Model != "trusted-model" || output.TotalTokens != 5 {
		t.Fatalf("unexpected output: %#v", output)
	}
	if receivedModel != "trusted-model" || !strings.Contains(receivedPrompt, "Trusted task instructions:\nreply exactly") || !strings.Contains(receivedPrompt, "<submitted_content>\nuntrusted") {
		t.Fatalf("provider request lost trust/model boundaries: model=%q prompt=%q", receivedModel, receivedPrompt)
	}
}

func TestOpenAICompatibleVisionRequiresExplicitCapability(t *testing.T) {
	cfg := config.Config{
		Routes:  map[string]config.Route{"default": {Provider: "remote"}},
		Engines: map[string]config.Engine{"remote": {Type: "openai_compatible", URL: "http://127.0.0.1:1/v1", Model: "text", Capabilities: []string{"text"}, TimeoutSeconds: 1}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "inspect", ImageBase64: "YQ==", ImageMediaType: "image/png", Output: OutputSpec{Mode: "text"}})
	if output.Error != "providers_unavailable" {
		t.Fatalf("unconfigured vision escaped provider boundary: %#v", output)
	}
}

func TestOpenAICompatibleStatusDoesNotClaimRemoteModelIsLoaded(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer provider-secret" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]string{{"id": "remote-model"}}})
	}))
	defer provider.Close()
	status := openAICompatibleStatus(context.Background(), "remote", config.Engine{
		Type: "openai_compatible", URL: provider.URL + "/v1", Model: "remote-model", APIKey: "provider-secret", Remote: true, Capabilities: []string{"text"},
	})
	if status.State != "online" || !status.Remote || status.Affinity != "remote API" || len(status.Models) != 1 || !status.Models[0].Available || status.Models[0].Loaded || !status.Models[0].CapabilitiesVerified {
		t.Fatalf("remote API status was misclassified: %#v", status)
	}
}

func TestOpenAICompatibleProviderRejectsUnconfiguredModelOverride(t *testing.T) {
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer provider.Close()
	cfg := config.Config{
		Routes:  map[string]config.Route{"default": {Provider: "remote"}},
		Engines: map[string]config.Engine{"remote": {Type: "openai_compatible", URL: provider.URL + "/v1", Model: "reviewed-model", Capabilities: []string{"text"}, TimeoutSeconds: 5}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "reply", Model: "unreviewed-model", Output: OutputSpec{Mode: "text"}})
	if output.Error != "providers_unavailable" || requests != 0 {
		t.Fatalf("model override crossed the configured egress boundary: output=%#v requests=%d", output, requests)
	}
}

func TestOpenAICompatibleStatusRequiresConfiguredModelInInventory(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}})
	}))
	defer provider.Close()
	status := openAICompatibleStatus(context.Background(), "remote", config.Engine{
		Type: "openai_compatible", URL: provider.URL, Model: "reviewed-model", Capabilities: []string{"text"},
	})
	if status.State != "online" || len(status.Models) != 1 || status.Models[0].Available || !strings.Contains(status.Warning, "absent") {
		t.Fatalf("empty model inventory was treated as proof of availability: %#v", status)
	}
}
