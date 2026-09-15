package modelregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestDiscoverOllamaAndLocalModels(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", filepath.Join(t.TempDir(), "ollama-models"))
	t.Setenv("OLLAMA_HOST", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"llava:7b","size":4096,"details":{"parameter_size":"7B","quantization_level":"Q4_K_M","family":"llava"}}]}`))
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[{"name":"llava:7b","size_vram":2048}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	path := filepath.Join(directory, "qwen2.5-vl-7b-q4_k_m.gguf")
	if err := os.WriteFile(path, make([]byte, 100), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Storage: config.Storage{Models: directory}, Providers: config.Providers{Ollama: config.OllamaProvider{URL: server.URL}}}
	models, err := Discover(context.Background(), cfg, []string{directory})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("expected Ollama and local model, got %#v", models)
	}
	var local, ollama *DiscoveryEntry
	for index := range models {
		switch models[index].Provider {
		case "local-file":
			local = &models[index]
		case "ollama":
			ollama = &models[index]
		}
	}
	if local == nil || local.Quantization != "Q4_K_M" || local.Parameters != "7B" || len(local.Capabilities) != 2 {
		t.Fatalf("local metadata was not inferred: %#v", local)
	}
	if ollama == nil || !ollama.Loaded || ollama.VRAM != 2048 || len(ollama.Capabilities) != 2 {
		t.Fatalf("Ollama runtime metadata was not discovered: %#v", ollama)
	}
}

func TestDiscoverRejectsMissingPath(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", filepath.Join(t.TempDir(), "ollama-models"))
	t.Setenv("OLLAMA_HOST", "")
	_, err := Discover(context.Background(), config.Config{}, []string{filepath.Join(t.TempDir(), "missing")})
	if err == nil {
		t.Fatal("expected a missing model path to fail clearly")
	}
}

func TestDiscoverOllamaTrustsAdvertisedCapabilitiesForOpaqueName(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", filepath.Join(t.TempDir(), "ollama-models"))
	t.Setenv("OLLAMA_HOST", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"opaque:latest","capabilities":["completion","vision"]}]}`))
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Config{Storage: config.Storage{Models: t.TempDir()}, Providers: config.Providers{Ollama: config.OllamaProvider{URL: server.URL}}}
	models, err := Discover(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || strings.Join(models[0].Capabilities, ",") != "text,vision" {
		t.Fatalf("advertised Ollama capabilities were not preserved: %#v", models)
	}
}

func TestDiscoverInstalledOllamaManifestWhileDaemonIsOffline(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OLLAMA_MODELS", root)
	t.Setenv("OLLAMA_HOST", "")
	manifest := filepath.Join(root, "manifests", "registry.ollama.ai", "library", "qwen2.5vl", "7b")
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"config":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"layers":[{"mediaType":"application/vnd.ollama.image.model","size":6000}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blobs", "sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte(`{"model_format":"gguf","model_family":"qwen25vl","model_type":"7B","file_type":"Q4_K_M"}`), 0600); err != nil {
		t.Fatal(err)
	}
	models, err := Discover(context.Background(), config.Config{Storage: config.Storage{Models: filepath.Join(root, "managed")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "qwen2.5vl:7b" || !models[0].Installed || models[0].Ready || models[0].Parameters != "7B" || len(models[0].Capabilities) != 2 {
		t.Fatalf("offline manifest not classified correctly: %#v", models)
	}
}
