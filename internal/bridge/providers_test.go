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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{"name": "large-text:latest", "size": 8_000, "details": map[string]interface{}{"family": "qwen"}},
				{"name": "small-text:latest", "size": 2_000, "details": map[string]interface{}{"family": "qwen"}},
				{"name": "small-vl:latest", "size": 3_000, "details": map[string]interface{}{"family": "qwen-vl"}},
				{"name": "jina-embed:latest", "size": 4_000, "details": map[string]interface{}{"family": "bert"}},
			},
		})
	}))
	defer server.Close()

	tests := []struct {
		name           string
		needsImage     bool
		needsEmbedding bool
		want           string
	}{
		{name: "text", want: "small-text:latest"},
		{name: "image", needsImage: true, want: "small-vl:latest"},
		{name: "embedding", needsEmbedding: true, want: "jina-embed:latest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectOllamaModel(context.Background(), server.URL, test.needsImage, test.needsEmbedding)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("selected %q, want %q", got, test.want)
			}
		})
	}
}
