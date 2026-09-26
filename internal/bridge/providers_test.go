package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestDecisionModelComesFromTrustedProviderConfig(t *testing.T) {
	decision := NormalizeDecision([]byte(`{"verdict":"allow","flags":[],"confidence":0.9,"model":"forged-model"}`), "ollama", "actual-local-model", time.Second)
	if decision.Model != "actual-local-model" {
		t.Fatalf("model metadata was not trusted: %#v", decision)
	}
}

func TestResolvedEngineEgressMustMatchAuthenticatedBoundary(t *testing.T) {
	remote := config.Engine{Type: "ollama", URL: "https://203.0.113.10:11434"}
	if err := validateResolvedEngineEgress(Job{ContextBridgeEgress: "local_only", ContextBridgeProviderClassification: "local"}, remote); err == nil {
		t.Fatal("remote Ollama endpoint escaped a local-only execution boundary")
	}
	if err := validateResolvedEngineEgress(Job{ContextBridgeProviderClassification: "remote"}, config.Engine{Type: "ollama", URL: "http://127.0.0.1:11434"}); err == nil {
		t.Fatal("local endpoint was accepted as an authenticated remote provider")
	}
	if err := validateResolvedEngineEgress(Job{ContextBridgeEgress: "remote_allowed", ContextBridgeProviderClassification: "remote"}, remote); err != nil {
		t.Fatalf("explicit remote endpoint was rejected: %v", err)
	}
	if err := validateResolvedEngineEgress(Job{}, config.Engine{Type: "ollama", URL: "http://198.51.100.2:11434"}); err == nil {
		t.Fatal("plaintext remote provider endpoint was accepted")
	}
	if err := validateResolvedEngineEgress(Job{ContextBridgeEgress: "local_only", ContextBridgeProviderClassification: "remote"}, config.Engine{Type: "adapter"}); err == nil {
		t.Fatal("URL-less adapter escaped a local-only boundary")
	}
	if err := validateResolvedEngineEgress(Job{ContextBridgeProviderClassification: "local"}, config.Engine{Type: "llama_cpp", Listen: "198.51.100.3:8080"}); err == nil {
		t.Fatal("llama.cpp listen fallback escaped endpoint classification")
	}
}

func TestOllamaHostCannotTurnLocalClassIntoRemoteEgress(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "https://203.0.113.20:11434")
	cfg := config.Config{
		Routes:    map[string]config.Route{"default": {Provider: "ollama", Model: "test"}},
		Providers: config.Providers{Ollama: config.OllamaProvider{Timeout: 1}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{
		Prompt: "must never leave this machine", Output: OutputSpec{Mode: "text"},
		ContextBridgeEgress: "local_only", ContextBridgeProviderClassification: "local",
	})
	if output.Error != "providers_unavailable" {
		t.Fatalf("remote OLLAMA_HOST was not rejected before provider execution: %#v", output)
	}
}

func TestJobCanSelectConfiguredRouteFallback(t *testing.T) {
	primaryCalls := 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls++
		http.Error(w, "primary should not be called", http.StatusInternalServerError)
	}))
	defer primary.Close()
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/generate" {
			json.NewEncoder(w).Encode(map[string]interface{}{"response": "chosen fallback"})
			return
		}
		http.NotFound(w, req)
	}))
	defer selected.Close()

	cfg := config.Config{
		Routes: map[string]config.Route{"default": {Provider: "primary", Fallback: []string{"selected"}}},
		Engines: map[string]config.Engine{
			"primary":  {Type: "ollama", URL: primary.URL, Model: "test", TimeoutSeconds: 2},
			"selected": {Type: "ollama", URL: selected.URL, Model: "test", TimeoutSeconds: 2},
		},
	}
	processor := NewProcessor(cfg, nil)
	output := processor.Process(context.Background(), Job{Provider: "selected", Prompt: "test", Output: OutputSpec{Mode: "text"}})
	if output.Error != "" || output.Text != "chosen fallback" || primaryCalls != 0 {
		t.Fatalf("provider override did not select the configured fallback: %#v, primary calls %d", output, primaryCalls)
	}

	rejected := processor.Process(context.Background(), Job{Provider: "unconfigured", Prompt: "test", Output: OutputSpec{Mode: "text"}})
	if rejected.Error != "provider_not_allowed_for_route" {
		t.Fatalf("unconfigured provider was not rejected: %#v", rejected)
	}
}

func TestOutputModelComesFromTrustedProviderConfig(t *testing.T) {
	output := NormalizeOutput([]byte(`{"mode":"text","text":"ok","model":"forged-model"}`), OutputSpec{Mode: "text"}, "ollama", "actual-local-model", time.Second)
	if output.Model != "actual-local-model" {
		t.Fatalf("model metadata was not trusted: %#v", output)
	}
}

func TestTrustedPromptAllowsExplicitArtifactCreation(t *testing.T) {
	prompt := trustedPrompt(Job{Prompt: "Create an image", Output: OutputSpec{Mode: "text", Artifacts: true}})
	if !strings.Contains(prompt, "create images or downloadable files") {
		t.Fatalf("artifact-capable adapter prompt was constrained to text: %s", prompt)
	}
}

func TestExplicitAdapterErrorIsNotAcceptedAsAnAnswer(t *testing.T) {
	for _, failure := range []string{"adapter_rate_limited", "adapter_timeout", "artifacts_missing: expected 1 file(s), received 0", "images_missing: expected 1 image(s), received 0"} {
		t.Run(failure, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{Routes: map[string]config.Route{"default": {Provider: "adapter", TimeoutSeconds: 2}}, Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 30}}}
			processor := NewProcessor(cfg, store)
			result := make(chan Output, 1)
			go func() {
				result <- processor.Process(context.Background(), Job{ID: "adapter-error-test", Provider: "adapter", Prompt: "test", Output: OutputSpec{Mode: "text"}})
			}()
			deadline := time.Now().Add(time.Second)
			var work *adapterJob
			for work == nil && time.Now().Before(deadline) {
				work = store.NextAdapterJob("", time.Minute)
				if work == nil {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if work == nil || !store.Complete(work.Job.ID, work.LeaseGeneration, Output{Mode: "text", Error: failure}) {
				t.Fatal("adapter job was not queued")
			}
			if output := <-result; output.Error != failure || output.Text != "" {
				t.Fatalf("adapter error became output: %#v", output)
			}
		})
	}
}

func TestAdapterRouteTimeoutKeepsSpecificFailure(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Routes: map[string]config.Route{"default": {Provider: "adapter", TimeoutSeconds: 1}}, Providers: config.Providers{Adapter: config.AdapterProvider{LeaseSeconds: 30}}}
	output := NewProcessor(cfg, store).Process(context.Background(), Job{ID: "adapter-timeout-test", Provider: "adapter", Prompt: "test", Output: OutputSpec{Mode: "text"}})
	if output.Error != "adapter_timeout" || output.Text != "" {
		t.Fatalf("adapter timeout was hidden or treated as an answer: %#v", output)
	}
}

func TestSelectOllamaModelUsesSmallestCompatibleModel(t *testing.T) {
	advertised := map[string][]string{
		"large-text:latest": {"completion"}, "small-text:latest": {"completion"},
		"small-vl:latest": {"completion", "vision"}, "opaque-embed:latest": {"embedding"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"models": []map[string]interface{}{
					{"name": "large-text:latest", "digest": "large-text", "size": 8_000},
					{"name": "small-text:latest", "digest": "small-text", "size": 2_000},
					{"name": "small-vl:latest", "digest": "small-vl", "size": 3_000},
					{"name": "opaque-embed:latest", "digest": "opaque-embed", "size": 4_000},
				},
			})
		case "/api/ps":
			// A loaded compatible model wins over a smaller cold model.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []map[string]interface{}{{"name": "large-text:latest"}}})
		case "/api/show":
			var input struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(request.Body).Decode(&input)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"capabilities": advertised[input.Model]})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	tests := []struct {
		name       string
		task       string
		mode       string
		needsImage bool
		want       string
	}{
		{name: "loaded text", task: "generation", mode: "text", want: "large-text:latest"},
		{name: "image", task: "generation", mode: "text", needsImage: true, want: "small-vl:latest"},
		{name: "vision task", task: "vision", mode: "text", want: "small-vl:latest"},
		{name: "embedding", task: "embedding", mode: "embedding", want: "opaque-embed:latest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectOllamaModel(context.Background(), server.URL, test.task, test.mode, test.needsImage)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("selected %q, want %q", got, test.want)
			}
		})
	}
}

func TestSelectOllamaModelNeverPromotesANameGuess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []map[string]interface{}{{"name": "obvious-llava-vision:latest", "digest": "unverified", "size": 1}}})
		case "/api/ps":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []map[string]interface{}{{"name": "obvious-llava-vision:latest"}}})
		case "/api/show":
			http.Error(w, "capabilities unavailable", http.StatusNotFound)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	if selected, err := selectOllamaModel(context.Background(), server.URL, "vision", "text", true); err == nil || selected != "" {
		t.Fatalf("unverified model-name guess became an automatic vision model: model=%q err=%v", selected, err)
	}
}

func TestAutomaticOllamaVisionJobUsesVerifiedModelAndCarriesMultipleImages(t *testing.T) {
	var generated struct {
		Model   string   `json:"model"`
		Images  []string `json:"images"`
		Options struct {
			NumCtx int `json:"num_ctx"`
		} `json:"options"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []map[string]interface{}{{"name": "opaque-vl", "digest": "opaque-vl-digest", "size": 100}}})
		case "/api/ps":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []interface{}{}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"capabilities": []string{"completion", "vision"}})
		case "/api/generate":
			if err := json.NewDecoder(request.Body).Decode(&generated); err != nil {
				t.Errorf("decode generation request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"response": "described"})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	cfg := config.Config{
		Routes:    map[string]config.Route{"default": {Provider: "ollama", Model: "auto", TimeoutSeconds: 5, Task: "vision"}},
		Providers: config.Providers{Ollama: config.OllamaProvider{URL: server.URL, Model: "auto", Images: true, Timeout: 5}},
	}
	output := NewProcessor(cfg, nil).Process(context.Background(), Job{
		Prompt: "Compare these images.", Images: []ImageInput{{MediaType: "image/png", DataBase64: "dmVyaWZpZWQ="}, {MediaType: "image/jpeg", DataBase64: "c2Vjb25k"}}, Output: OutputSpec{Mode: "text"},
	})
	if output.Error != "" || output.Text != "described" || output.Model != "opaque-vl" {
		t.Fatalf("verified automatic vision job failed: %#v", output)
	}
	if generated.Model != "opaque-vl" || len(generated.Images) != 2 || generated.Images[0] != "dmVyaWZpZWQ=" || generated.Images[1] != "c2Vjb25k" || generated.Options.NumCtx != 8192 {
		t.Fatalf("selected model or verified image was not sent: %#v", generated)
	}
}

func TestEngineImagePassportEnforcesCountBytesAndMediaType(t *testing.T) {
	engine := config.Engine{MaxInputImages: 1, MaxImageBytes: 4, MaxTotalImageBytes: 4, ImageMediaTypes: []string{"image/png"}}
	if err := validateEngineImageInputs(engine, []ImageInput{{MediaType: "image/png", DataBase64: "YQ=="}}); err != nil {
		t.Fatalf("valid engine image input was rejected: %v", err)
	}
	if err := validateEngineImageInputs(engine, []ImageInput{{MediaType: "image/png", DataBase64: "YQ=="}, {MediaType: "image/png", DataBase64: "Yg=="}}); err == nil || !strings.Contains(err.Error(), "at most 1") {
		t.Fatalf("known image-count limit was not enforced: %v", err)
	}
	if err := validateEngineImageInputs(engine, []ImageInput{{MediaType: "image/jpeg", DataBase64: "YQ=="}}); err == nil || !strings.Contains(err.Error(), "media type") {
		t.Fatalf("known media-type limit was not enforced: %v", err)
	}
}

func TestAutomaticOllamaSelectionSkipsSlowUnverifiedCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []map[string]interface{}{
				{"name": "slow-small", "digest": "slow-small", "size": 1},
				{"name": "ready-next", "digest": "ready-next", "size": 2},
			}})
		case "/api/ps":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"models": []interface{}{}})
		case "/api/show":
			var input struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(request.Body).Decode(&input)
			if input.Model == "slow-small" {
				select {
				case <-request.Context().Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"capabilities": []string{"completion"}})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	started := time.Now()
	model, err := selectOllamaModel(context.Background(), server.URL, "generation", "text", false)
	if err != nil || model != "ready-next" {
		t.Fatalf("slow candidate hid later compatible model: model=%q err=%v", model, err)
	}
	if elapsed := time.Since(started); elapsed >= 1500*time.Millisecond {
		t.Fatalf("slow candidate consumed the route timeout: %v", elapsed)
	}
}
