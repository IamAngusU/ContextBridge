package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkerAdvertisesEmbeddingOnlyRuntimeModelWithoutGeneration(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status" {
			http.NotFound(w, request)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"runtime": map[string]interface{}{"engines": map[string]interface{}{
				"ollama": map[string]interface{}{
					"state": "online",
					"models": []map[string]interface{}{{
						"name": "embed-only", "capabilities": []string{"embedding"},
					}, {
						"name": "image-only", "capabilities": []string{"image_generation"},
					}},
				},
			}},
		})
	}))
	defer local.Close()

	worker := &Worker{
		cfg:        WorkerConfig{LocalURL: local.URL, MaxConcurrent: 1},
		client:     local.Client(),
		hardwareAt: time.Now(), // keep the test independent of host GPU probes
	}
	capabilities := worker.capabilities(context.Background())
	if len(capabilities.Models) != 1 {
		t.Fatalf("runtime model was not advertised: %#v", capabilities.Models)
	}
	model := capabilities.Models[0]
	if len(model.Tasks) != 1 || model.Tasks[0] != "embedding" || !model.Embedding || model.Vision {
		t.Fatalf("embedding-only model gained incorrect capabilities: %#v", model)
	}
	if containsFold(model.Tasks, "generation") {
		t.Fatalf("embedding-only model was advertised for generation: %#v", model.Tasks)
	}
}
