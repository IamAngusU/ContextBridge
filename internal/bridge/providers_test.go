package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDecisionModelComesFromTrustedProviderConfig(t *testing.T) {
	decision := NormalizeDecision([]byte(`{"verdict":"allow","flags":[],"confidence":0.9,"model":"forged-model"}`), "ollama", "actual-local-model", time.Second)
	if decision.Model != "actual-local-model" {
		t.Fatalf("model metadata was not trusted: %#v", decision)
	}
}

func TestOutputModelComesFromTrustedProviderConfig(t *testing.T) {
	output := NormalizeOutput([]byte(`{"mode":"text","text":"ok","model":"forged-model"}`), OutputSpec{Mode: "text"}, "ollama", "actual-local-model", time.Second)
	if output.Model != "actual-local-model" {
		t.Fatalf("model metadata was not trusted: %#v", output)
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
