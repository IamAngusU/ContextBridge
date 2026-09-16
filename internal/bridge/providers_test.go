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
		t.Fatalf("artifact-capable browser prompt was constrained to text: %s", prompt)
	}
}

func TestExplicitBrowserErrorIsNotAcceptedAsAnAnswer(t *testing.T) {
	for _, failure := range []string{"browser_rate_limited", "browser_timeout", "artifacts_missing: expected 1 file(s), received 0", "images_missing: expected 1 image(s), received 0"} {
		t.Run(failure, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{Routes: map[string]config.Route{"default": {Provider: "browser", TimeoutSeconds: 2}}, Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 30}}}
			processor := NewProcessor(cfg, store)
			result := make(chan Output, 1)
			go func() {
				result <- processor.Process(context.Background(), Job{ID: "browser-error-test", Provider: "browser", Prompt: "test", Output: OutputSpec{Mode: "text"}})
			}()
			deadline := time.Now().Add(time.Second)
			var work *browserJob
			for work == nil && time.Now().Before(deadline) {
				work = store.NextBrowserJob("", time.Minute)
				if work == nil {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if work == nil || !store.Complete(work.Job.ID, work.LeaseGeneration, Output{Mode: "text", Error: failure}) {
				t.Fatal("browser job was not queued")
			}
			if output := <-result; output.Error != failure || output.Text != "" {
				t.Fatalf("browser error became output: %#v", output)
			}
		})
	}
}

func TestBrowserRouteTimeoutKeepsSpecificFailure(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Routes: map[string]config.Route{"default": {Provider: "browser", TimeoutSeconds: 1}}, Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 30}}}
	output := NewProcessor(cfg, store).Process(context.Background(), Job{ID: "browser-timeout-test", Provider: "browser", Prompt: "test", Output: OutputSpec{Mode: "text"}})
	if output.Error != "browser_timeout" || output.Text != "" {
		t.Fatalf("browser timeout was hidden or treated as an answer: %#v", output)
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

func TestAutomaticOllamaVisionJobUsesVerifiedModelAndCarriesImage(t *testing.T) {
	var generated struct {
		Model  string   `json:"model"`
		Images []string `json:"images"`
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
		Prompt: "Describe this image.", ImageBase64: "dmVyaWZpZWQ=", ImageMediaType: "image/png", Output: OutputSpec{Mode: "text"},
	})
	if output.Error != "" || output.Text != "described" || output.Model != "opaque-vl" {
		t.Fatalf("verified automatic vision job failed: %#v", output)
	}
	if generated.Model != "opaque-vl" || len(generated.Images) != 1 || generated.Images[0] != "dmVyaWZpZWQ=" {
		t.Fatalf("selected model or verified image was not sent: %#v", generated)
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
