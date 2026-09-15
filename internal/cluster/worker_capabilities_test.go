package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func workerCapabilitiesForStatus(t *testing.T, status interface{}) Capabilities {
	t.Helper()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status" {
			http.NotFound(w, request)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}))
	t.Cleanup(local.Close)
	worker := &Worker{
		cfg:        WorkerConfig{LocalURL: local.URL, MaxConcurrent: 1},
		client:     local.Client(),
		hardwareAt: time.Now(), // keep the test independent of host GPU probes
	}
	return worker.capabilities(context.Background())
}

func TestWorkerAdvertisesEmbeddingOnlyRuntimeModelWithoutGeneration(t *testing.T) {
	capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
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
	if len(capabilities.Models) != 2 {
		t.Fatalf("runtime model was not advertised: %#v", capabilities.Models)
	}
	model := capabilities.Models[0]
	if len(model.Tasks) != 1 || model.Tasks[0] != "embedding" || !model.Embedding || model.Vision {
		t.Fatalf("embedding-only model gained incorrect capabilities: %#v", model)
	}
	if containsFold(model.Tasks, "generation") {
		t.Fatalf("embedding-only model was advertised for generation: %#v", model.Tasks)
	}
	imageOnly := capabilities.Models[1]
	if imageOnly.Name != "image-only" || len(imageOnly.Tasks) != 0 || imageOnly.Vision || imageOnly.Embedding {
		t.Fatalf("image-generation inventory gained an executable task: %#v", imageOnly)
	}
}

func TestWorkerRouteCannotUpgradeAuthoritativeEmbeddingModel(t *testing.T) {
	capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
		"routes": map[string]interface{}{
			"default":   map[string]interface{}{"task": "generation", "provider": "ollama", "model": "embed-only"},
			"embedding": map[string]interface{}{"task": "embedding", "provider": "ollama", "model": "embed-only"},
		},
		"runtime": map[string]interface{}{"engines": map[string]interface{}{
			"ollama": map[string]interface{}{
				"state": "online",
				"models": []map[string]interface{}{{
					"name": "embed-only", "capabilities": []string{"embedding"},
				}},
			},
		}},
	})

	if len(capabilities.Models) != 1 {
		t.Fatalf("route and runtime model were not deduplicated: %#v", capabilities.Models)
	}
	model := capabilities.Models[0]
	if len(model.Tasks) != 1 || model.Tasks[0] != "embedding" || !model.Embedding || model.Vision {
		t.Fatalf("route metadata upgraded authoritative embedding capabilities: %#v", model)
	}
	if selectedModelSupports(capabilities.Models, Requirements{Task: "generation", Provider: "ollama", Model: "embed-only"}) {
		t.Fatalf("selectedModelSupports accepted generation from stale route metadata: %#v", capabilities.Models)
	}
	if !selectedModelSupports(capabilities.Models, Requirements{Task: "embedding", Provider: "ollama", Model: "embed-only"}) {
		t.Fatalf("selectedModelSupports rejected the authoritative embedding task: %#v", capabilities.Models)
	}
	if containsFold(capabilities.Tasks, "generation") {
		t.Fatalf("stale route metadata advertised generation at the worker level: %#v", capabilities.Tasks)
	}
	node := Node{ID: "embed-node", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "embed-only"}); len(got) != 0 {
		t.Fatalf("embedding-only route model was ranked for generation: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
		t.Fatalf("embedding-only route model was ranked for model-less generation: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "embedding", Provider: "ollama", Model: "embed-only", Embedding: true}); len(got) != 1 {
		t.Fatalf("embedding-only route model was not ranked for embedding: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "embedding", Provider: "ollama", Embedding: true}); len(got) != 1 {
		t.Fatalf("embedding-only route model was not ranked for its model-less supported task: %#v", got)
	}
}

func TestWorkerAutomaticOllamaRouteRejectsEmbeddingOnlyInventory(t *testing.T) {
	for _, routeModel := range []string{"", "auto"} {
		name := "blank"
		if routeModel != "" {
			name = routeModel
		}
		t.Run(name, func(t *testing.T) {
			capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
				"routes": map[string]interface{}{
					"default": map[string]interface{}{"task": "generation", "provider": "ollama", "model": routeModel},
				},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{
					"ollama": map[string]interface{}{
						"state": "online",
						"models": []map[string]interface{}{{
							"name": "embed-only", "capabilities": []string{"embedding"},
						}},
					},
				}},
			})

			if containsFold(capabilities.Tasks, "generation") {
				t.Fatalf("automatic route advertised generation from embedding-only inventory: %#v", capabilities)
			}
			node := Node{ID: "embed-node", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
			if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
				t.Fatalf("automatic route ranked embedding-only inventory for generation: %#v", got)
			}
			if routeModel == "auto" {
				if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "auto"}); len(got) != 0 {
					t.Fatalf("synthetic auto model bypassed authoritative inventory: %#v", got)
				}
			}
		})
	}
}

func TestWorkerAutoOllamaRoutePreservesCapableAndUnknownInventorySemantics(t *testing.T) {
	for _, test := range []struct {
		name   string
		models interface{}
	}{
		{name: "capable", models: []map[string]interface{}{{"name": "text-model", "capabilities": []string{"completion"}}}},
		{name: "inventory-unavailable", models: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := map[string]interface{}{"state": "online"}
			if test.models != nil {
				engine["models"] = test.models
			}
			capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
				"routes": map[string]interface{}{
					"default": map[string]interface{}{"task": "generation", "provider": "ollama", "model": "auto"},
				},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{"ollama": engine}},
			})
			if !containsFold(capabilities.Tasks, "generation") {
				t.Fatalf("valid automatic route did not advertise generation: %#v", capabilities)
			}
			node := Node{ID: "auto-node", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
			if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 1 {
				t.Fatalf("valid automatic route was not ranked: %#v", got)
			}
			if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "auto"}); len(got) != 1 {
				t.Fatalf("explicit auto selection lost compatibility: %#v", got)
			}
		})
	}
}

func TestWorkerRouteCannotUpgradeImageGenerationModelWithVisionName(t *testing.T) {
	capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
		"routes": map[string]interface{}{
			"vision": map[string]interface{}{"task": "vision", "provider": "ollama", "model": "misleading-vision-name"},
		},
		"runtime": map[string]interface{}{"engines": map[string]interface{}{
			"ollama": map[string]interface{}{
				"state": "online",
				"models": []map[string]interface{}{{
					"name": "misleading-vision-name", "capabilities": []string{"image_generation"},
				}},
			},
		}},
	})

	if len(capabilities.Models) != 1 || capabilities.Models[0].Name != "misleading-vision-name" || len(capabilities.Models[0].Tasks) != 0 {
		t.Fatalf("unsupported image-generation model was lost or gained route tasks: %#v", capabilities.Models)
	}
	if selectedModelSupports(capabilities.Models, Requirements{Task: "vision", Provider: "ollama", Model: "misleading-vision-name"}) {
		t.Fatalf("selectedModelSupports accepted image generation as vision: %#v", capabilities.Models)
	}
	node := Node{ID: "image-node", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
	if got := Rank([]Node{node}, Requirements{Task: "vision", Provider: "ollama", Model: "misleading-vision-name", Vision: true}); len(got) != 0 {
		t.Fatalf("image-generation-only route model was ranked for vision: %#v", got)
	}
}

func TestWorkerRouteAndRuntimeModelUseOneAuthoritativeEntry(t *testing.T) {
	capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
		"routes": map[string]interface{}{
			"default": map[string]interface{}{"task": "generation", "provider": "ollama", "model": "text-model"},
		},
		"runtime": map[string]interface{}{"engines": map[string]interface{}{
			"ollama": map[string]interface{}{
				"state": "online",
				"models": []map[string]interface{}{{
					"name": "text-model", "capabilities": []string{"completion"},
					"size_bytes": int64(1234), "vram_bytes": int64(567), "loaded": true,
				}},
			},
		}},
	})

	if len(capabilities.Models) != 1 {
		t.Fatalf("route and runtime produced duplicate model entries: %#v", capabilities.Models)
	}
	model := capabilities.Models[0]
	if model.Name != "text-model" || model.Provider != "ollama" || len(model.Tasks) != 1 || model.Tasks[0] != "generation" {
		t.Fatalf("unexpected authoritative model entry: %#v", model)
	}
	if model.Size != 1234 || model.VRAM != 567 || !model.Loaded || model.Vision || model.Embedding {
		t.Fatalf("runtime evidence was not preserved on the deduplicated entry: %#v", model)
	}
	if !containsFold(capabilities.Tasks, "generation") {
		t.Fatalf("authoritative text model did not advertise its configured route task: %#v", capabilities.Tasks)
	}
	node := Node{ID: "text-node", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 1 {
		t.Fatalf("authoritative text route was not ranked for model-less generation: %#v", got)
	}
}

func TestWorkerFallbackCannotAdvertiseTaskFromIncompatibleAuthoritativeInventory(t *testing.T) {
	for _, routeModel := range []string{"", "embed-only", "missing-model"} {
		t.Run("model-"+routeModel, func(t *testing.T) {
			capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
				"routes": map[string]interface{}{
					"default": map[string]interface{}{
						"task": "generation", "provider": "browser", "fallback": []string{"ollama"}, "model": routeModel,
					},
				},
				"browser": map[string]interface{}{"connected": false, "selectors_ready": false},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{
					"ollama": map[string]interface{}{
						"state": "online",
						"models": []map[string]interface{}{{
							"name": "embed-only", "capabilities": []string{"embedding"},
						}},
					},
				}},
			})

			if containsFold(capabilities.Tasks, "generation") {
				t.Fatalf("fallback upgraded incompatible authoritative inventory: %#v", capabilities)
			}
			node := Node{ID: "fallback", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
			if got := Rank([]Node{node}, Requirements{Task: "generation"}); len(got) != 0 {
				t.Fatalf("generic generation escaped to an embedding-only fallback: %#v", got)
			}
		})
	}
}

func TestWorkerFixedRouteModelMustExistInAuthoritativeInventory(t *testing.T) {
	capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
		"routes": map[string]interface{}{
			"default": map[string]interface{}{
				"task": "generation", "provider": "ollama", "model": "missing-model",
			},
		},
		"runtime": map[string]interface{}{"engines": map[string]interface{}{
			"ollama": map[string]interface{}{
				"state": "online",
				"models": []map[string]interface{}{{
					"name": "installed-model", "capabilities": []string{"completion"},
				}},
			},
		}},
	})

	if containsFold(capabilities.Tasks, "generation") {
		t.Fatalf("missing fixed route model advertised automatic generation: %#v", capabilities)
	}
	for _, model := range capabilities.Models {
		if model.Name == "missing-model" {
			t.Fatalf("missing fixed model was synthesized over authoritative inventory: %#v", capabilities.Models)
		}
	}
	node := Node{ID: "fixed", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
		t.Fatalf("model-less job ignored fixed-route readiness: %#v", got)
	}
}

func TestWorkerPreservesUnsupportedRuntimeInventoryAsNonSchedulableEvidence(t *testing.T) {
	capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
		"routes": map[string]interface{}{
			"local": map[string]interface{}{
				"task": "generation", "provider": "ollama", "model": "image-only",
			},
			"web": map[string]interface{}{
				"task": "generation", "provider": "browser",
			},
		},
		"browser": map[string]interface{}{
			"connected": true, "selectors_ready": true, "active_tabs": 1,
		},
		"runtime": map[string]interface{}{"engines": map[string]interface{}{
			"ollama": map[string]interface{}{
				"state": "online",
				"models": []map[string]interface{}{{
					"name": "image-only", "capabilities": []string{"image_generation"},
				}},
			},
		}},
	})

	var imageOnly *ModelCapability
	for index := range capabilities.Models {
		if capabilities.Models[index].Name == "image-only" {
			imageOnly = &capabilities.Models[index]
			break
		}
	}
	if imageOnly == nil || len(imageOnly.Tasks) != 0 || imageOnly.Vision || imageOnly.Embedding {
		t.Fatalf("unsupported inventory was lost or gained executable tasks: %#v", capabilities.Models)
	}
	node := Node{ID: "mixed", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
		t.Fatalf("browser task leaked into image-generation-only Ollama inventory: %#v", got)
	}
}
