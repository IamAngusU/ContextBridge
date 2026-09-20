package bridge

import (
	"context"
	"encoding/json"
	"math"
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
		Engines: map[string]config.Engine{"remote": {Type: "openai_compatible", URL: provider.URL + "/v1", Model: "trusted-model", APIKeyFile: "secret.key", ResolvedAPIKey: "provider-secret", Capabilities: []string{"text"}, TimeoutSeconds: 5}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "reply exactly", Text: "untrusted", Output: OutputSpec{Mode: "text"}})
	if output.Error != "" || output.Text != "REMOTE-OK" || output.Provider != "remote" || output.Model != "trusted-model" || output.TotalTokens != 5 {
		t.Fatalf("unexpected output: %#v", output)
	}
	if receivedModel != "trusted-model" || !strings.Contains(receivedPrompt, "Trusted task instructions:\nreply exactly") || !strings.Contains(receivedPrompt, "<submitted_content>\nuntrusted") {
		t.Fatalf("provider request lost trust/model boundaries: model=%q prompt=%q", receivedModel, receivedPrompt)
	}
}

func TestOpenAICompatibleProviderGuardsBalanceAndReportsUpperBoundCost(t *testing.T) {
	balanceCalls, completionCalls := 0, 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer provider-secret" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/user/balance":
			balanceCalls++
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"is_available":  true,
				"balance_infos": []map[string]string{{"currency": "USD", "total_balance": "9.76"}},
			})
		case "/v1/chat/completions":
			completionCalls++
			var payload struct {
				MaxTokens       int    `json:"max_tokens"`
				ReasoningEffort string `json:"reasoning_effort"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.MaxTokens != 64 || payload.ReasoningEffort != "low" {
				t.Fatalf("bounded provider settings were not sent: %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"choices": []map[string]interface{}{{"message": map[string]string{"content": "BUDGET-OK"}}},
				"usage": map[string]int{
					"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110,
					"prompt_cache_hit_tokens": 40, "prompt_cache_miss_tokens": 60,
				},
			})
		default:
			http.NotFound(w, request)
		}
	}))
	defer provider.Close()
	cfg := config.Config{
		Routes: map[string]config.Route{"default": {Provider: "deepseek"}},
		Engines: map[string]config.Engine{"deepseek": {
			Type: "openai_compatible", URL: provider.URL + "/v1", Model: "deepseek-flash", ResolvedAPIKey: "provider-secret", Capabilities: []string{"text"}, TimeoutSeconds: 5,
			MaxOutputTokens: 64, ReasoningEffort: "low", BalancePath: "/user/balance", MinimumBalanceUSD: 5,
			Costing: config.EngineCosting{Mode: "upper_bound", Source: "official peak table", InputPerMillionUSD: 0.30, CachedInputPerMillionUSD: 0.006, OutputPerMillionUSD: 1.20},
		}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "reply exactly", MaxCostUSD: 1, Output: OutputSpec{Mode: "text"}})
	if output.Error != "" || output.Text != "BUDGET-OK" || balanceCalls != 1 || completionCalls != 1 {
		t.Fatalf("guarded provider request failed: output=%#v balance=%d completion=%d", output, balanceCalls, completionCalls)
	}
	want := 60.0/1_000_000*0.30 + 40.0/1_000_000*0.006 + 10.0/1_000_000*1.20
	if output.CostStatus != "upper_bound" || output.CostSource != "official peak table" || math.Abs(output.EstimatedCostUSD-want) > 1e-12 || output.ReservedCostUSD <= output.EstimatedCostUSD {
		t.Fatalf("cost evidence is not explicit and conservative: %#v want=%g", output, want)
	}
}

func TestOpenAICompatibleProviderStopsBeforeSpendingBelowBalanceFloor(t *testing.T) {
	completionCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/user/balance" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"is_available":  true,
				"balance_infos": []map[string]string{{"currency": "USD", "total_balance": "5.00001"}},
			})
			return
		}
		completionCalls++
		http.Error(w, "must not spend", http.StatusInternalServerError)
	}))
	defer provider.Close()
	cfg := config.Config{
		Routes: map[string]config.Route{"default": {Provider: "deepseek"}},
		Engines: map[string]config.Engine{"deepseek": {
			Type: "openai_compatible", URL: provider.URL + "/v1", Model: "deepseek-flash", ResolvedAPIKey: "provider-secret", Capabilities: []string{"text"}, TimeoutSeconds: 5,
			MaxOutputTokens: 64, BalancePath: "/user/balance", MinimumBalanceUSD: 5,
			Costing: config.EngineCosting{Mode: "upper_bound", Source: "ceiling", InputPerMillionUSD: 0.30, OutputPerMillionUSD: 1.20},
		}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "do not spend", Output: OutputSpec{Mode: "text"}})
	if output.Error != "providers_unavailable" || completionCalls != 0 {
		t.Fatalf("balance floor did not stop before provider generation: output=%#v calls=%d", output, completionCalls)
	}
}

func TestProviderBalanceRejectsNonFiniteValues(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"is_available":  true,
			"balance_infos": []map[string]string{{"currency": "USD", "total_balance": "Infinity"}},
		})
	}))
	defer provider.Close()
	_, _, err := providerBalanceUSD(context.Background(), config.Engine{URL: provider.URL + "/v1", BalancePath: "/balance"})
	if err == nil || !strings.Contains(err.Error(), "invalid USD balance") {
		t.Fatalf("non-finite provider balance returned %v", err)
	}
}

func TestProviderHTTPClientNeverFollowsRedirects(t *testing.T) {
	var targetCalls int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	request, err := http.NewRequest(http.MethodPost, redirect.URL, strings.NewReader(`{"prompt":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	response, err := providerHTTPClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || targetCalls != 0 {
		t.Fatalf("provider redirect crossed the configured egress boundary: status=%d target_calls=%d", response.StatusCode, targetCalls)
	}
}

func TestOpenAICompatibleProviderEnforcesAuthenticatedJobBudgetBeforeRequest(t *testing.T) {
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls++
		http.Error(w, "must not spend", http.StatusInternalServerError)
	}))
	defer provider.Close()
	cfg := config.Config{
		Routes: map[string]config.Route{"default": {Provider: "deepseek"}},
		Engines: map[string]config.Engine{"deepseek": {
			Type: "openai_compatible", URL: provider.URL + "/v1", Model: "deepseek-flash", ResolvedAPIKey: "secret", Capabilities: []string{"text"}, TimeoutSeconds: 5,
			MaxOutputTokens: 64, Costing: config.EngineCosting{Mode: "upper_bound", Source: "reviewed", InputPerMillionUSD: 0.30, OutputPerMillionUSD: 1.20},
		}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "bounded", MaxCostUSD: 0.000001, Output: OutputSpec{Mode: "text"}})
	if !strings.HasPrefix(output.Error, "cost_budget_exceeded:") || providerCalls != 0 {
		t.Fatalf("job budget did not stop before provider request: %#v calls=%d", output, providerCalls)
	}
}

func TestOpenAICompatibleProviderRejectsUnknownCostWhenBudgetRequested(t *testing.T) {
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls++
		http.Error(w, "must not spend", http.StatusInternalServerError)
	}))
	defer provider.Close()
	cfg := config.Config{
		Routes:  map[string]config.Route{"default": {Provider: "remote"}},
		Engines: map[string]config.Engine{"remote": {Type: "openai_compatible", URL: provider.URL + "/v1", Model: "remote", ResolvedAPIKey: "secret", Capabilities: []string{"text"}, TimeoutSeconds: 5}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{Prompt: "unknown cost", MaxCostUSD: 1, Output: OutputSpec{Mode: "text"}})
	if !strings.HasPrefix(output.Error, "cost_budget_unverifiable:") || providerCalls != 0 {
		t.Fatalf("unknown price was treated as zero: %#v calls=%d", output, providerCalls)
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
