package cluster

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func workerCapabilitiesForStatus(t *testing.T, status interface{}) Capabilities {
	t.Helper()
	markRuntimeModelEvidence(status)
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

// Hand-written status fixtures that include a capabilities array model the
// current local service, which labels provider evidence explicitly. Individual
// tests can set either field to false to exercise fail-closed legacy/inferred
// inventory behavior.
func markRuntimeModelEvidence(status interface{}) {
	root, ok := status.(map[string]interface{})
	if !ok {
		return
	}
	runtimeStatus, _ := root["runtime"].(map[string]interface{})
	engines, _ := runtimeStatus["engines"].(map[string]interface{})
	for _, rawEngine := range engines {
		engine, _ := rawEngine.(map[string]interface{})
		models, _ := engine["models"].([]map[string]interface{})
		for _, model := range models {
			if _, set := model["available"]; !set {
				model["available"] = true
			}
			if _, set := model["capabilities_verified"]; !set {
				model["capabilities_verified"] = true
			}
			if _, set := model["capability_source"]; !set {
				model["capability_source"] = "ollama_show"
			}
		}
	}
}

func TestWorkerAdvertisesBoundedPerEndpointAdapterChoices(t *testing.T) {
	models := make([]string, 0, MaximumAdapterModelChoices+5)
	reasoning := make([]string, 0, MaximumAdapterReasoningLevels+5)
	for index := 0; index < MaximumAdapterModelChoices+5; index++ {
		models = append(models, fmt.Sprintf("%02d%s", index, strings.Repeat("m", MaximumAdapterChoiceBytes+20)))
	}
	for index := 0; index < MaximumAdapterReasoningLevels+5; index++ {
		reasoning = append(reasoning, fmt.Sprintf("%02d%s", index, strings.Repeat("r", MaximumAdapterChoiceBytes+20)))
	}
	capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
		"adapter": map[string]interface{}{
			"connected": true, "ready": true, "active_endpoints": 1,
			"endpoints": []map[string]interface{}{{
				"id": 7, "profile": "profile-one", "state": "waiting",
				"session_key": "cb:" + strings.Repeat("a", 64), "session_key_supported": true,
				"can_create_session": true, "default_new_session": true,
				"current_model": "Adapter Model A", "current_reasoning": "High",
				"models": models, "reasoning_levels": reasoning,
			}},
		},
	})
	if len(capabilities.AdapterSessions) != 1 {
		t.Fatalf("adapter session was not advertised: %#v", capabilities.AdapterSessions)
	}
	session := capabilities.AdapterSessions[0]
	if session.SessionKey != "cb:"+strings.Repeat("a", 64) || !session.SessionKeySupported || !session.CanCreateSession || !session.DefaultNewSession {
		t.Fatalf("adapter session routing evidence was not propagated: %#v", session)
	}
	if len(session.ModelChoices) != MaximumAdapterModelChoices || len(session.ReasoningLevels) != MaximumAdapterReasoningLevels {
		t.Fatalf("per-endpoint choices were not bounded: models=%d reasoning=%d", len(session.ModelChoices), len(session.ReasoningLevels))
	}
	for _, values := range [][]string{session.ModelChoices, session.ReasoningLevels} {
		for _, value := range values {
			if len(value) > MaximumAdapterChoiceBytes {
				t.Fatalf("oversized adapter choice escaped the worker: %d bytes", len(value))
			}
		}
	}
}

func TestRelayScopesOpaqueAdapterSessionTelemetry(t *testing.T) {
	capabilities := Capabilities{AdapterSessions: []AdapterSessionCapability{
		{EndpointID: 1, Profile: "profile-one", SessionKey: "cb:" + strings.Repeat("a", 64), SessionKeySupported: true, CanCreateSession: true, DefaultNewSession: true},
		{EndpointID: 2, Profile: "profile-one", SessionKey: "raw-session-name", SessionKeySupported: true},
		{EndpointID: 3, Profile: "custom", CanCreateSession: true, DefaultNewSession: true},
		{EndpointID: 4, Profile: "", CanCreateSession: true, DefaultNewSession: true},
	}}
	scopeNodeCapabilities(&capabilities, TokenRecord{})
	if capabilities.AdapterSessions[0].SessionKey == "" {
		t.Fatal("valid opaque adapter session key was discarded")
	}
	if capabilities.AdapterSessions[1].SessionKey != "" {
		t.Fatal("non-opaque adapter session value escaped relay validation")
	}
	if !capabilities.AdapterSessions[2].CanCreateSession || !capabilities.AdapterSessions[2].DefaultNewSession {
		t.Fatal("valid out-of-tree adapter profile lost fresh-session capability")
	}
	if capabilities.AdapterSessions[3].CanCreateSession || capabilities.AdapterSessions[3].DefaultNewSession {
		t.Fatal("unidentified adapter endpoint advertised fresh-session creation")
	}
}

func TestRelayBoundsUntrustedHardwareAndInventoryTelemetry(t *testing.T) {
	models := make([]ModelCapability, MaximumModelCapabilities+10)
	for index := range models {
		models[index] = ModelCapability{Name: strings.Repeat("m", 200), Size: -1, VRAM: -1, Tasks: []string{"generation"}}
	}
	gpus := make([]GPUCapability, MaximumGPUCapabilities+10)
	for index := range gpus {
		gpus[index] = GPUCapability{Name: strings.Repeat("g", 200), MemoryTotal: ^uint64(0), MemoryFree: ^uint64(0), Utilization: 1000, Temperature: 10000}
	}
	capabilities := Capabilities{
		CPUCores: -1, CPUUtilization: 1000, MemoryTotal: ^uint64(0), MemoryFree: ^uint64(0),
		GPUs: gpus, Models: models, MaxConcurrent: MaximumWorkerConcurrency + 1, Running: int(^uint(0) >> 1),
		AdapterEndpoints: int(^uint(0) >> 1), AdapterBusy: int(^uint(0) >> 1), QueueDepth: int(^uint(0) >> 1),
		AutomaticTasks: map[string][]string{strings.Repeat("p", 200): {strings.Repeat("t", 200)}},
	}
	scopeNodeCapabilities(&capabilities, TokenRecord{})
	if capabilities.CPUCores != 0 || capabilities.CPUUtilization != 100 || capabilities.MemoryTotal != MaximumNodeHardwareBytes || capabilities.MemoryFree != MaximumNodeHardwareBytes {
		t.Fatalf("hardware bounds were not applied: %#v", capabilities)
	}
	if len(capabilities.GPUs) != MaximumGPUCapabilities || len(capabilities.Models) != MaximumModelCapabilities {
		t.Fatalf("inventories were not bounded: GPUs=%d models=%d", len(capabilities.GPUs), len(capabilities.Models))
	}
	if capabilities.GPUs[0].Utilization != 100 || capabilities.GPUs[0].MemoryTotal != MaximumNodeHardwareBytes || capabilities.Models[0].Size != 0 || capabilities.Models[0].VRAM != 0 {
		t.Fatalf("inventory values were not normalized: gpu=%#v model=%#v", capabilities.GPUs[0], capabilities.Models[0])
	}
	if capabilities.MaxConcurrent != MaximumWorkerConcurrency || capabilities.Running != MaximumWorkerConcurrency || capabilities.AdapterEndpoints != MaximumAdapterSessions || capabilities.AdapterBusy != MaximumAdapterSessions || capabilities.QueueDepth != 1_000_000 {
		t.Fatalf("load counters were not bounded: %#v", capabilities)
	}
}

func TestAdapterRoutingSelectsOneEndpointWithAllRequestedCapabilities(t *testing.T) {
	requirements := Requirements{
		Task: "generation", Provider: "adapter", AdapterProfile: "profile-one",
		Model: "Adapter Model A", Reasoning: "Sehr hoch",
	}
	sessions := []AdapterSessionCapability{
		{EndpointID: 11, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model B", CurrentReasoning: "Sehr hoch", ModelChoices: []string{"Adapter Model B"}},
		{EndpointID: 12, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A", CurrentReasoning: "Mittel", ReasoningLevels: []string{"Mittel"}},
		{EndpointID: 13, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A", CurrentReasoning: "Sehr hoch"},
	}
	selected, ok := selectReadyAdapterSession(sessions, requirements)
	if !ok || selected.EndpointID != 13 {
		t.Fatalf("routing combined capabilities from different endpoints: %#v %v", selected, ok)
	}
	requirements.AdapterEndpointID = 12
	if _, ok := selectReadyAdapterSession(sessions, requirements); ok {
		t.Fatal("relay-selected endpoint was accepted without its requested reasoning level")
	}
	requirements.AdapterEndpointID = 13
	if selected, ok := selectReadyAdapterSession(sessions, requirements); !ok || selected.EndpointID != 13 {
		t.Fatalf("exact qualifying endpoint was not retained: %#v %v", selected, ok)
	}
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

func TestWorkerAutoOllamaRouteRequiresVerifiedAvailableInventory(t *testing.T) {
	for _, test := range []struct {
		name        string
		models      interface{}
		expectReady bool
	}{
		{name: "capable", models: []map[string]interface{}{{"name": "text-model", "capabilities": []string{"completion"}}}, expectReady: true},
		{name: "inventory-unavailable", models: nil},
		{name: "name-inferred", models: []map[string]interface{}{{"name": "obvious-text-model", "capabilities": []string{"text"}, "available": true, "capabilities_verified": false, "capability_source": "name_inference"}}},
		{name: "not-available", models: []map[string]interface{}{{"name": "text-model", "capabilities": []string{"completion"}, "available": false}}},
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
			if containsFold(capabilities.Tasks, "generation") != test.expectReady {
				t.Fatalf("automatic route readiness=%v, want %v: %#v", containsFold(capabilities.Tasks, "generation"), test.expectReady, capabilities)
			}
			node := Node{ID: "auto-node", Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
			want := 0
			if test.expectReady {
				want = 1
			}
			if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != want {
				t.Fatalf("automatic route ranked %d nodes, want %d: %#v", len(got), want, got)
			}
			if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "auto"}); len(got) != want {
				t.Fatalf("explicit auto ranked %d nodes, want %d: %#v", len(got), want, got)
			}
		})
	}
}

func TestWorkerKeepsExplicitAvailableOllamaModelWithoutCapabilityEvidence(t *testing.T) {
	status := func(routeModel string) map[string]interface{} {
		return map[string]interface{}{
			"routes": map[string]interface{}{
				"default": map[string]interface{}{"task": "generation", "provider": "ollama", "model": routeModel},
			},
			"runtime": map[string]interface{}{"engines": map[string]interface{}{
				"ollama": map[string]interface{}{
					"state": "online",
					"models": []map[string]interface{}{{
						"name": "legacy-model", "capabilities": []string{"generation"}, "available": true,
						"capabilities_verified": false, "capability_source": "name_inference",
					}},
				},
			}},
		}
	}

	fixed := workerCapabilitiesForStatus(t, status("legacy-model"))
	if !containsFold(fixed.Tasks, "generation") || !providerTaskSupported(fixed.AutomaticTasks, "ollama", "generation") {
		t.Fatalf("explicit available legacy model was rejected: %#v", fixed)
	}
	fixedNode := Node{ID: "fixed", Connected: true, LastSeen: time.Now().UTC(), Capabilities: fixed}
	if got := Rank([]Node{fixedNode}, Requirements{Task: "generation", Provider: "ollama", Model: "legacy-model"}); len(got) != 1 {
		t.Fatalf("explicit available legacy model was not schedulable: %#v", got)
	}

	automatic := workerCapabilitiesForStatus(t, status("auto"))
	if containsFold(automatic.Tasks, "generation") || providerTaskSupported(automatic.AutomaticTasks, "ollama", "generation") {
		t.Fatalf("automatic route trusted unverified name evidence: %#v", automatic)
	}
	automaticNode := Node{ID: "auto", Connected: true, LastSeen: time.Now().UTC(), Capabilities: automatic}
	if got := Rank([]Node{automaticNode}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
		t.Fatalf("automatic route scheduled unverified name evidence: %#v", got)
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
	if model.Size != 1234 || model.VRAM != 567 || !model.Available || !model.Loaded || !model.CapabilitiesVerified || model.CapabilitySource != "ollama_show" || model.Vision || model.Embedding {
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

func TestVerifiedDuplicateModelEvidenceReplacesNameInference(t *testing.T) {
	unverified := ModelCapability{
		Name: "opaque", Provider: "ollama", Tasks: []string{"generation", "vision"}, Vision: true,
		Available: true, Loaded: true, CapabilitySource: "name_inference",
	}
	verified := ModelCapability{
		Name: "opaque", Provider: "ollama", Tasks: []string{"embedding"}, Embedding: true,
		Available: true, CapabilitiesVerified: true, CapabilitySource: "ollama_show",
	}
	for _, merged := range []ModelCapability{
		mergeModelCapability(unverified, verified),
		mergeModelCapability(verified, unverified),
	} {
		if !merged.CapabilitiesVerified || merged.CapabilitySource != "ollama_show" || !merged.Available || !merged.Loaded || merged.Vision || !merged.Embedding || len(merged.Tasks) != 1 || merged.Tasks[0] != "embedding" {
			t.Fatalf("name inference contaminated verified model evidence: %#v", merged)
		}
		node := Node{ID: "duplicate", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
			Providers: []string{"ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, MaxConcurrent: 1,
			Models: []ModelCapability{merged},
		}}
		if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Vision: true}); len(got) != 0 {
			t.Fatalf("contaminated duplicate was auto-routed for vision: %#v", got)
		}
	}
}

func TestWorkerFallbackCannotAdvertiseTaskFromIncompatibleAuthoritativeInventory(t *testing.T) {
	for _, routeModel := range []string{"", "embed-only", "missing-model"} {
		t.Run("model-"+routeModel, func(t *testing.T) {
			capabilities := workerCapabilitiesForStatus(t, map[string]interface{}{
				"routes": map[string]interface{}{
					"default": map[string]interface{}{
						"task": "generation", "provider": "adapter", "fallback": []string{"ollama"}, "model": routeModel,
					},
				},
				"adapter": map[string]interface{}{"connected": false, "ready": false},
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
				"task": "generation", "provider": "adapter",
			},
		},
		"adapter": map[string]interface{}{
			"connected": true, "ready": true, "active_endpoints": 1,
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
		t.Fatalf("adapter task leaked into image-generation-only Ollama inventory: %#v", got)
	}
}
