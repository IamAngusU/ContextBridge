package cluster

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestRankUsesCapabilitiesLoadAndVRAM(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "busy", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"vision"}, Groups: []string{"media"}, MaxConcurrent: 2, Running: 1, GPUs: []GPUCapability{{MemoryTotal: 12 << 30, MemoryFree: 9 << 30}}, Models: []ModelCapability{{Name: "vision-a", Vision: true, Tasks: []string{"vision"}}}}},
		{ID: "free", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"vision"}, Groups: []string{"media"}, MaxConcurrent: 2, GPUs: []GPUCapability{{MemoryTotal: 12 << 30, MemoryFree: 10 << 30}}, Models: []ModelCapability{{Name: "vision-b", Vision: true, Tasks: []string{"vision"}}}}},
		{ID: "wrong", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"embedding"}, Groups: []string{"media"}, MaxConcurrent: 2}},
	}
	ranked := Rank(nodes, Requirements{Task: "vision", Group: "media", Vision: true, MinFreeVRAM: 8 << 30})
	if len(ranked) != 2 || ranked[0].Node.ID != "free" {
		t.Fatalf("unexpected ranking: %#v", ranked)
	}
}

func TestKnownLocalModelCapabilities(t *testing.T) {
	vision, embedding := modelFeatures("qwen2.5vl:7b", "generation")
	if !vision || embedding {
		t.Fatal("vision model classification failed")
	}
	vision, embedding = modelFeatures("jina-embeddings-v4", "generation")
	if vision || !embedding {
		t.Fatal("embedding model classification failed")
	}
}

func TestAdvertisedLocalModelCapabilitiesOverrideNameGuessing(t *testing.T) {
	vision, embedding := modelFeaturesFromCapabilities("opaque-model", "generation", []string{"text", "vision"})
	if !vision || embedding {
		t.Fatal("advertised vision capability was ignored")
	}
	vision, embedding = modelFeaturesFromCapabilities("misleading-vision-name", "generation", []string{"text"})
	if vision || embedding {
		t.Fatal("name inference overrode authoritative advertised capabilities")
	}
	vision, embedding = modelFeaturesFromCapabilities("opaque-model", "generation", []string{"embedding"})
	if vision || !embedding {
		t.Fatal("advertised embedding capability was ignored")
	}
	tasks, vision, embedding := modelTasksFromCapabilities("looks-like-text", []string{"embedding"})
	if len(tasks) != 1 || tasks[0] != "embedding" || vision || !embedding {
		t.Fatalf("embedding-only model was advertised for extra tasks: %#v", tasks)
	}
	tasks, vision, embedding = modelTasksFromCapabilities("looks-like-vision", []string{"image_generation"})
	if len(tasks) != 0 || vision || embedding {
		t.Fatalf("unsupported image generation was advertised as an executable task: %#v", tasks)
	}
}

func TestEstimatedVRAMPrefersGPUWithoutExcludingZeroGPUWorker(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "cpu", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 2, MemoryTotal: 32 << 30, MemoryFree: 28 << 30}},
		{ID: "gpu", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 2, MemoryTotal: 32 << 30, MemoryFree: 28 << 30, GPUs: []GPUCapability{{MemoryTotal: 12 << 30, MemoryFree: 10 << 30}}}},
	}
	ranked := RankWithEstimate(nodes, Requirements{Task: "generation"}, 6<<30)
	if len(ranked) != 2 {
		t.Fatalf("CPU fallback was excluded: %#v", ranked)
	}
	if ranked[0].Node.ID != "gpu" {
		t.Fatalf("GPU with measured headroom was not preferred: %#v", ranked)
	}

	ranked = Rank(nodes[:1], Requirements{Task: "generation"})
	if len(ranked) != 1 || ranked[0].Node.ID != "cpu" {
		t.Fatalf("zero-GPU worker could not execute a normal job: %#v", ranked)
	}
}

func TestProviderRequirementSelectsReadyAdapterWorker(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "local-model", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, Providers: []string{"ollama"}, MaxConcurrent: 1}},
		{ID: "web-chat", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, Providers: []string{"adapter"}, MaxConcurrent: 1}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation", Provider: "adapter"})
	if len(ranked) != 1 || ranked[0].Node.ID != "web-chat" {
		t.Fatalf("adapter provider requirement was not enforced: %#v", ranked)
	}
}

func TestAdapterProfileRequirementIsAHardReadyEndpointFilter(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "profile-one", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Tasks: []string{"generation"}, Providers: []string{"adapter"}, MaxConcurrent: 2,
			AdapterEndpoints: 1, AdapterSessions: []AdapterSessionCapability{{Profile: "profile-one", State: "waiting"}},
		}},
		{ID: "profile-two-busy", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Tasks: []string{"generation"}, Providers: []string{"adapter"}, MaxConcurrent: 2,
			AdapterEndpoints: 1, AdapterBusy: 1, AdapterSessions: []AdapterSessionCapability{{Profile: "profile-two", State: "working"}},
		}},
		{ID: "profile-two-ready", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Tasks: []string{"generation"}, Providers: []string{"adapter"}, MaxConcurrent: 2,
			AdapterEndpoints: 1, AdapterSessions: []AdapterSessionCapability{{Profile: "profile-two", State: "waiting"}},
		}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two"})
	if len(ranked) != 1 || ranked[0].Node.ID != "profile-two-ready" {
		t.Fatalf("profile-specific job escaped to a wrong or busy endpoint: %#v", ranked)
	}
	if got := Rank(nodes[:2], Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two"}); len(got) != 0 {
		t.Fatalf("busy requested profile was treated as ready: %#v", got)
	}
	if got := Rank(nodes, Requirements{Task: "generation", Provider: "ollama", AdapterProfile: "profile-two"}); len(got) != 0 {
		t.Fatalf("adapter profile crossed the provider boundary: %#v", got)
	}
}

func TestAdapterProfileAndModelMustMatchTheSameReadyEndpoint(t *testing.T) {
	node := Node{ID: "split-adapter", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter"}, MaxConcurrent: 2, AdapterEndpoints: 2,
		AdapterSessions: []AdapterSessionCapability{
			{EndpointID: 1, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A", ModelChoices: []string{"Adapter Model A", "Adapter Model B"}},
			{EndpointID: 2, Profile: "profile-two", State: "waiting", CurrentModel: "remote-model-fast", ModelChoices: []string{"remote-model-fast", "remote-model-pro"}},
		},
		// The node-wide inventory intentionally contains both labels. It must not
		// allow the scheduler to combine a profile from one endpoint with a model that
		// exists only on another endpoint.
		Models: []ModelCapability{
			{Name: "Adapter Model A", Provider: "adapter", Tasks: []string{"generation"}},
			{Name: "remote-model-pro", Provider: "adapter", Tasks: []string{"generation"}},
		},
	}}

	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Model: "remote-model-pro"}); len(got) != 0 {
		t.Fatalf("profile and model were combined across endpoints: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two", Model: "remote-model-pro"}); len(got) != 1 {
		t.Fatalf("valid same-endpoint adapter model choice was rejected: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Model: "Adapter Model A"}); len(got) != 1 {
		t.Fatalf("current model from an older per-endpoint advertisement was rejected: %#v", got)
	}

	legacy := node
	legacy.ID = "legacy-adapter"
	legacy.Capabilities.AdapterSessions = nil
	if got := Rank([]Node{legacy}, Requirements{Task: "generation", Provider: "adapter", Model: "Adapter Model A"}); len(got) != 1 {
		t.Fatalf("legacy model-only adapter inventory lost compatibility: %#v", got)
	}
}

func TestAdapterEndpointModelChoiceFoldsNBSPAndExcludesBusyEndpoints(t *testing.T) {
	node := Node{ID: "adapter", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter"}, MaxConcurrent: 2, AdapterEndpoints: 2, AdapterBusy: 1,
		AdapterSessions: []AdapterSessionCapability{
			{EndpointID: 1, Profile: "profile-one", State: "working", ModelChoices: []string{"Adapter\u00a0Model A"}},
			{EndpointID: 2, Profile: "profile-two", State: "waiting", ModelChoices: []string{"remote-model-fast"}},
		},
		Models: []ModelCapability{{Name: "Adapter\u00a0Model A", Provider: "adapter", Tasks: []string{"generation"}}},
	}}

	request := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Model: "Adapter Model A"}
	if got := Rank([]Node{node}, request); len(got) != 0 {
		t.Fatalf("busy matching adapter endpoint was considered schedulable: %#v", got)
	}
	node.Capabilities.AdapterSessions[0].State = "waiting"
	node.Capabilities.AdapterBusy = 0
	if got := Rank([]Node{node}, request); len(got) != 1 {
		t.Fatalf("NBSP-equivalent model label was not accepted on a ready endpoint: %#v", got)
	}
}

func TestAdapterReasoningChoicesMatchStableCLIAliases(t *testing.T) {
	node := Node{ID: "localized-adapter", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter"}, MaxConcurrent: 1, AdapterEndpoints: 1,
		Models: []ModelCapability{{Name: "Adapter Model A", Provider: "adapter", Tasks: []string{"generation"}}},
		AdapterSessions: []AdapterSessionCapability{{
			EndpointID: 1, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A",
			ReasoningLevels: []string{"Sofort", "Mittel", "Sehr hoch"},
		}},
	}}

	for cli, ui := range map[string]string{"instant": "Sofort", "medium": "Mittel", "xhigh": "Sehr hoch"} {
		request := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Model: "Adapter Model A", Reasoning: cli}
		if got := Rank([]Node{node}, request); len(got) != 1 {
			t.Fatalf("CLI reasoning %q did not match localized UI label %q: %#v", cli, ui, got)
		}
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Reasoning: "pro"}); len(got) != 0 {
		t.Fatalf("unsupported reasoning level was broadened by alias matching: %#v", got)
	}
}

func TestAdapterSessionBoundEndpointsAreReservedForAffinityAndRecoverMovedConversation(t *testing.T) {
	sessions := []AdapterSessionCapability{
		{EndpointID: 41, Profile: "profile-one", State: "session_bound", CurrentModel: "Adapter Model A"},
		{EndpointID: 42, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A"},
	}
	newSession := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Model: "Adapter Model A"}
	selected, ok := selectReadyAdapterSession(sessions, newSession)
	if !ok || selected.EndpointID != 42 {
		t.Fatalf("new session stole an occupied adapter conversation: %#v %v", selected, ok)
	}
	affinity := newSession
	affinity.AdapterEndpointID = 41
	affinity.AdapterSessionRecovery = true
	selected, ok = selectReadyAdapterSession(sessions, affinity)
	if !ok || selected.EndpointID != 41 || selectedAdapterEndpointBinding(affinity, selected) != 41 {
		t.Fatalf("same-session affinity did not reuse its bound endpoint: %#v %v", selected, ok)
	}

	// The old endpoint vanished and the saved conversation was reopened elsewhere.
	// The relay must stay on the same node but leave execution unpinned so the
	// adapter can prove the saved URL and report the replacement endpoint.
	sessions = []AdapterSessionCapability{
		{EndpointID: 42, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A"},
		{EndpointID: 43, Profile: "profile-one", State: "session_bound", CurrentModel: "Adapter Model A"},
	}
	selected, ok = selectReadyAdapterSession(sessions, affinity)
	if !ok || selectedAdapterEndpointBinding(affinity, selected) != 0 {
		t.Fatalf("moved-session recovery stayed pinned to a vanished endpoint: %#v %v", selected, ok)
	}

	workingOld := append([]AdapterSessionCapability{{EndpointID: 41, Profile: "profile-one", State: "working", CurrentModel: "Adapter Model A"}}, sessions...)
	if selected, ok = selectReadyAdapterSession(workingOld, affinity); ok {
		t.Fatalf("a busy existing session silently escaped to another endpoint: %#v", selected)
	}
	navigatedOld := append([]AdapterSessionCapability{{EndpointID: 41, Profile: "profile-two", State: "waiting", CurrentModel: "remote-model-fast"}}, sessions...)
	if selected, ok = selectReadyAdapterSession(navigatedOld, affinity); !ok || selectedAdapterEndpointBinding(affinity, selected) != 0 {
		t.Fatalf("an incompatible reused endpoint id blocked saved-URL recovery: %#v %v", selected, ok)
	}
}

func TestOpaqueAdapterSessionKeyOverridesStaleEndpointPlacement(t *testing.T) {
	requirements := Requirements{
		Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Model: "Adapter Model A",
		AdapterEndpointID: 41, AdapterSessionRecovery: true, AdapterSessionKey: "cb:" + strings.Repeat("a", 64),
	}
	sessions := []AdapterSessionCapability{
		{EndpointID: 41, Profile: "profile-one", State: "session_bound", SessionKeySupported: true, CurrentModel: "Adapter Model A"},
		{EndpointID: 42, Profile: "profile-one", State: "waiting", SessionKeySupported: true, SessionKey: requirements.AdapterSessionKey, CurrentModel: "Adapter Model A"},
	}
	selected, ok := selectReadyAdapterSession(sessions, requirements)
	if !ok || selected.EndpointID != 42 || selectedAdapterEndpointBinding(requirements, selected) != 42 {
		t.Fatalf("live opaque session evidence did not override stale endpoint placement: %#v %v", selected, ok)
	}
	sessions = append(sessions, AdapterSessionCapability{
		EndpointID: 43, Profile: "profile-one", State: "waiting", SessionKeySupported: true,
		SessionKey: requirements.AdapterSessionKey, CurrentModel: "Adapter Model A",
	})
	if selected, ok = selectReadyAdapterSession(sessions, requirements); ok {
		t.Fatalf("duplicate session-key claims did not fail closed: %#v", selected)
	}
}

func TestOpaqueAdapterSessionKeyEvidenceIsProfileScoped(t *testing.T) {
	key := "cb:" + strings.Repeat("a", 64)
	sessions := []AdapterSessionCapability{
		{EndpointID: 41, Profile: "profile-one", State: "session_bound", SessionKeySupported: true, SessionKey: key},
		{EndpointID: 42, Profile: "profile-two", State: "session_bound", SessionKeySupported: true, SessionKey: key},
		{EndpointID: 43, Profile: "custom-secure", State: "session_bound", SessionKeySupported: true, SessionKey: key},
	}
	for _, profile := range []string{"profile-one", "profile-two", "custom-secure"} {
		selected, ok := selectReadyAdapterSession(sessions, Requirements{
			Task: "generation", Provider: "adapter", AdapterProfile: profile, AdapterSessionKey: key,
		})
		if !ok || selected.Profile != profile {
			t.Fatalf("opaque session evidence crossed profile %q: %#v %v", profile, selected, ok)
		}
	}
}

func TestFreshAdapterChatCanUseBoundEndpointOnlyAsLauncher(t *testing.T) {
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", AdapterFreshSession: true}
	sessions := []AdapterSessionCapability{
		{EndpointID: 11, Profile: "profile-one", State: "session_bound", SessionKeySupported: true, CanCreateSession: true},
		{EndpointID: 12, Profile: "profile-one", State: "session_bound", SessionKeySupported: true, CanCreateSession: true},
	}
	selected, ok := selectReadyAdapterSession(sessions, requirements)
	if !ok || selected.EndpointID != 11 {
		t.Fatalf("fresh job could not use a bound endpoint as a deterministic launcher: %#v %v", selected, ok)
	}
	sessions[0].CanCreateSession = false
	sessions[1].CanCreateSession = false
	if selected, ok = selectReadyAdapterSession(sessions, requirements); ok {
		t.Fatalf("fresh job used a launcher without verified creation capability: %#v", selected)
	}
	if selected, ok = selectReadyAdapterSession([]AdapterSessionCapability{{
		EndpointID: 13, Profile: "profile-one", State: "waiting", SessionKeySupported: true,
		DefaultNewSession: true, CanCreateSession: false,
	}}, Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one"}); ok {
		t.Fatalf("adapter default fresh mode leased a waiting endpoint without creation capability: %#v", selected)
	}
	sessions[1].CanCreateSession = true
	sessions[1].DefaultNewSession = true
	requirements.AdapterFreshSession = false
	if selected, ok = selectReadyAdapterSession(sessions, requirements); !ok || selected.EndpointID != 12 {
		t.Fatalf("adapter default fresh-session mode was not routable: %#v %v", selected, ok)
	}
	requirements.AdapterEndpointID = 99
	if selected, ok = selectReadyAdapterSession(sessions, requirements); ok {
		t.Fatalf("a missing established session silently launched another chat: %#v", selected)
	}
}

func TestBusyAdapterEndpointsDoNotHideLocalModelCapacity(t *testing.T) {
	now := time.Now().UTC()
	node := Node{ID: "mixed", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter", "ollama"},
		AutomaticTasks: map[string][]string{"ollama": {"generation"}},
		Models:         []ModelCapability{{Name: "local-text", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}},
		MaxConcurrent:  4, Running: 1, AdapterEndpoints: 1, AdapterBusy: 1,
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter"}); len(got) != 0 {
		t.Fatalf("adapter job routed to a worker with no free endpoint: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 1 {
		t.Fatalf("busy adapter endpoint incorrectly blocked a local-model job: %#v", got)
	}
}

func TestModelCapabilityCannotCrossProviderBoundary(t *testing.T) {
	node := Node{ID: "mixed", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter", "ollama"}, MaxConcurrent: 2,
		Models: []ModelCapability{{Name: "web-vision", Provider: "adapter", Vision: true, Tasks: []string{"generation", "vision"}}},
	}}
	for _, request := range []Requirements{
		{Task: "vision", Provider: "ollama", Vision: true},
		{Task: "generation", Provider: "ollama", Model: "web-vision"},
	} {
		if got := Rank([]Node{node}, request); len(got) != 0 {
			t.Fatalf("adapter-only model leaked into Ollama capability: %#v", got)
		}
	}
	if got := Rank([]Node{node}, Requirements{Task: "vision", Provider: "adapter", Vision: true}); len(got) != 1 {
		t.Fatalf("valid adapter vision job was excluded: %#v", got)
	}
}

func TestRequestedModelMustProvideRequestedModality(t *testing.T) {
	node := Node{ID: "mixed-local", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, MaxConcurrent: 2,
		Models: []ModelCapability{
			{Name: "text-only", Provider: "ollama", Tasks: []string{"generation"}},
			{Name: "vision-model", Provider: "ollama", Vision: true, Tasks: []string{"generation", "vision"}},
		},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "text-only", Vision: true}); len(got) != 0 {
		t.Fatalf("a different installed vision model satisfied the selected text-only model: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "vision-model", Vision: true}); len(got) != 1 {
		t.Fatalf("the selected vision model was rejected: %#v", got)
	}
}

func TestModelLessRequestRequiresOneModelToSatisfyTaskAndModality(t *testing.T) {
	node := Node{ID: "split", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation", "vision"}, Providers: []string{"ollama"}, MaxConcurrent: 2,
		AutomaticTasks: map[string][]string{"ollama": {"generation"}},
		Models: []ModelCapability{
			{Name: "text-only", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true},
			{Name: "vision-only", Provider: "ollama", Vision: true, Tasks: []string{"vision"}, Available: true, CapabilitiesVerified: true},
		},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Vision: true}); len(got) != 0 {
		t.Fatalf("task and modality were incorrectly combined across two models: %#v", got)
	}
	node.Capabilities.Models[1].Tasks = []string{"generation", "vision"}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Vision: true}); len(got) != 1 {
		t.Fatalf("one genuinely compatible automatic model was rejected: %#v", got)
	}
}

func TestAutomaticModelSelectorUsesCompatibleAuthoritativeModel(t *testing.T) {
	node := Node{ID: "auto", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation", "vision"}, Providers: []string{"ollama"}, MaxConcurrent: 2,
		AutomaticTasks: map[string][]string{"ollama": {"generation"}},
		Models: []ModelCapability{
			{Name: "text-only", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true},
			{Name: "vision-model", Provider: "ollama", Vision: true, Tasks: []string{"generation", "vision"}, Available: true, CapabilitiesVerified: true},
		},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "auto", Vision: true}); len(got) != 1 {
		t.Fatalf("auto selector did not use a compatible authoritative model: %#v", got)
	}
}

func TestAutomaticOllamaSelectorRequiresAvailabilityAndVerifiedCapabilities(t *testing.T) {
	base := Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"},
		AutomaticTasks: map[string][]string{"ollama": {"generation"}}, MaxConcurrent: 1,
	}
	for _, test := range []struct {
		name  string
		model ModelCapability
	}{
		{name: "name inference", model: ModelCapability{Name: "obvious-text-name", Provider: "ollama", Tasks: []string{"generation"}, Available: true}},
		{name: "not available", model: ModelCapability{Name: "text", Provider: "ollama", Tasks: []string{"generation"}, CapabilitiesVerified: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := base
			capabilities.Models = []ModelCapability{test.model}
			node := Node{ID: test.name, Connected: true, LastSeen: time.Now().UTC(), Capabilities: capabilities}
			if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
				t.Fatalf("unproven automatic model was scheduled: %#v", got)
			}
		})
	}
}

func TestExplicitOllamaModelPreservesOperatorChoiceWithoutCapabilityEvidence(t *testing.T) {
	node := Node{ID: "legacy", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"ollama"}, MaxConcurrent: 1,
		Models: []ModelCapability{{
			Name: "legacy-model", Provider: "ollama", Tasks: []string{"embedding"}, Available: true,
			CapabilitySource: "name_inference",
		}},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "legacy-model"}); len(got) != 1 {
		t.Fatalf("explicit available model was rejected from unverified name inference: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
		t.Fatalf("automatic request trusted the same unverified model: %#v", got)
	}
	node.Capabilities.Models[0].CapabilitiesVerified = true
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "legacy-model"}); len(got) != 0 {
		t.Fatalf("verified incompatible explicit model was scheduled: %#v", got)
	}
}

func TestFeatureOnlyAutomaticOllamaRequestRequiresVerifiedAvailableModel(t *testing.T) {
	base := ModelCapability{Name: "vision", Provider: "ollama", Tasks: []string{"vision"}, Vision: true}
	for _, test := range []struct {
		name  string
		model ModelCapability
		want  int
	}{
		{name: "name inference", model: func() ModelCapability { item := base; item.Available = true; return item }()},
		{name: "unavailable", model: func() ModelCapability { item := base; item.CapabilitiesVerified = true; return item }()},
		{name: "verified available", model: func() ModelCapability {
			item := base
			item.Available = true
			item.CapabilitiesVerified = true
			return item
		}(), want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := Node{ID: test.name, Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
				Providers: []string{"ollama"}, MaxConcurrent: 1, Models: []ModelCapability{test.model},
			}}
			if got := Rank([]Node{node}, Requirements{Provider: "ollama", Vision: true}); len(got) != test.want {
				t.Fatalf("feature-only auto request ranked %d nodes, want %d: %#v", len(got), test.want, got)
			}
		})
	}
}

func TestAutomaticOllamaSelectorPrefersLoadedCompatibleNode(t *testing.T) {
	now := time.Now().UTC()
	capabilities := func(loaded bool) Capabilities {
		return Capabilities{
			Tasks: []string{"generation"}, Providers: []string{"ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, MaxConcurrent: 1,
			Models: []ModelCapability{{Name: "text", Provider: "ollama", Tasks: []string{"generation"}, Available: true, Loaded: loaded, CapabilitiesVerified: true}},
		}
	}
	ranked := Rank([]Node{
		{ID: "cold", Connected: true, LastSeen: now, Capabilities: capabilities(false)},
		{ID: "loaded", Connected: true, LastSeen: now, Capabilities: capabilities(true)},
	}, Requirements{Task: "generation", Provider: "ollama"})
	if len(ranked) != 2 || ranked[0].Node.ID != "loaded" {
		t.Fatalf("loaded compatible model was not preferred: %#v", ranked)
	}
}

func TestAdapterProviderRequiresAnAvailableEndpoint(t *testing.T) {
	node := Node{ID: "relay-only", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter"}, AutomaticTasks: map[string][]string{"adapter": {"generation"}}, MaxConcurrent: 2,
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter"}); len(got) != 0 {
		t.Fatalf("adapter job was routed without any attached endpoint: %#v", got)
	}
	node.Capabilities.AdapterEndpoints = 1
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter"}); len(got) != 1 {
		t.Fatalf("available adapter endpoint was rejected: %#v", got)
	}
}

func TestUnscopedModelCannotSatisfyExplicitProvider(t *testing.T) {
	node := Node{ID: "legacy-mixed", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter", "ollama"}, MaxConcurrent: 2,
		Models: []ModelCapability{{Name: "ambiguous", Tasks: []string{"generation"}}},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "ambiguous"}); len(got) != 0 {
		t.Fatalf("unscoped model crossed an explicit provider boundary: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Model: "ambiguous"}); len(got) != 1 {
		t.Fatalf("unscoped legacy model was rejected for an unscoped request: %#v", got)
	}
}

func TestRequestedModelMustProvideRequestedTask(t *testing.T) {
	node := Node{ID: "mixed-local", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation", "embedding"}, Providers: []string{"ollama"}, MaxConcurrent: 2,
		Models: []ModelCapability{
			{Name: "text-model", Provider: "ollama", Tasks: []string{"generation"}},
			{Name: "embed-model", Provider: "ollama", Embedding: true, Tasks: []string{"embedding"}},
		},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama", Model: "embed-model"}); len(got) != 0 {
		t.Fatalf("embedding-only selected model was ranked for generation: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "embedding", Provider: "ollama", Model: "embed-model", Embedding: true}); len(got) != 1 {
		t.Fatalf("embedding model was rejected for its supported task: %#v", got)
	}
}

func TestModelLessTaskUsesAuthoritativeProviderInventory(t *testing.T) {
	base := Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter", "ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, MaxConcurrent: 2,
	}
	node := Node{ID: "mixed", Connected: true, LastSeen: time.Now().UTC()}

	node.Capabilities = base
	node.Capabilities.Models = []ModelCapability{{Name: "embed-only", Provider: "ollama", Embedding: true, Tasks: []string{"embedding"}, Available: true, CapabilitiesVerified: true}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
		t.Fatalf("global task advertisement bypassed authoritative embedding-only inventory: %#v", got)
	}

	node.Capabilities.Models = []ModelCapability{{Name: "text-model", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 1 {
		t.Fatalf("capable provider inventory was rejected: %#v", got)
	}

	node.Capabilities.Models = nil
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 0 {
		t.Fatalf("automatic Ollama request ignored missing model evidence: %#v", got)
	}
}

func TestMultiGPUNodeUsesEligibleIdleDeviceForRanking(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "rack", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 4, GPUs: []GPUCapability{
			{Name: "rack-hot", MemoryTotal: 16 << 30, MemoryFree: 12 << 30, Utilization: 99},
			{Name: "rack-idle", MemoryTotal: 16 << 30, MemoryFree: 12 << 30, Utilization: 5},
		}}},
		{ID: "single", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 4, GPUs: []GPUCapability{
			{Name: "single", MemoryTotal: 16 << 30, MemoryFree: 12 << 30, Utilization: 40},
		}}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation", MinFreeVRAM: 8 << 30})
	if len(ranked) != 2 || ranked[0].Node.ID != "rack" {
		t.Fatalf("rack node's idle eligible GPU was not used for ranking: %#v", ranked)
	}
}

func TestHardVRAMRequirementAllowsAnyRackGPUAndRejectsZeroGPU(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "zero-gpu", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 2}},
		{ID: "rack", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 2, GPUs: []GPUCapability{
			{Name: "small", MemoryTotal: 4 << 30, MemoryFree: 3 << 30},
			{Name: "large", MemoryTotal: 24 << 30, MemoryFree: 20 << 30},
		}}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation", MinFreeVRAM: 16 << 30})
	if len(ranked) != 1 || ranked[0].Node.ID != "rack" {
		t.Fatalf("hard VRAM filtering did not inspect every GPU: %#v", ranked)
	}
}

func TestHardVRAMRequirementNeverSumsRackGPUs(t *testing.T) {
	node := Node{ID: "rack", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, MaxConcurrent: 2,
		GPUs: []GPUCapability{
			{Name: "gpu-a", MemoryTotal: 8 << 30, MemoryFree: 8 << 30},
			{Name: "gpu-b", MemoryTotal: 8 << 30, MemoryFree: 8 << 30},
		},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", MinFreeVRAM: 12 << 30}); len(got) != 0 {
		t.Fatalf("two smaller GPUs were incorrectly summed for one hard VRAM requirement: %#v", got)
	}
}

func TestAdapterModelLabelsFoldUnicodeWhitespaceOnly(t *testing.T) {
	node := Node{ID: "adapter", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"adapter", "ollama"}, MaxConcurrent: 2,
		Models: []ModelCapability{
			{Name: "remote-model\u00a0fast", Provider: "adapter", Tasks: []string{"generation"}},
			{Name: "local\u00a0model", Provider: "ollama", Tasks: []string{"generation"}},
		},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "adapter", Model: "remote-model fast"}); len(got) != 1 {
		t.Fatalf("adapter label with NBSP rejected ordinary user spaces: %#v", got)
	}
	for _, request := range []Requirements{
		{Task: "generation", Provider: "adapter", Model: "remote-model-pro"},
		{Task: "generation", Provider: "ollama", Model: "local model"},
	} {
		if got := Rank([]Node{node}, request); len(got) != 0 {
			t.Fatalf("model whitespace folding crossed a model/provider boundary: %#v", got)
		}
	}
}

func TestFirstPreferredNodeWinsEqualLoad(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "node-a", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 1}},
		{ID: "node-b", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 1}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation", PreferredNodes: []string{"node-b"}})
	if len(ranked) != 2 || ranked[0].Node.ID != "node-b" {
		t.Fatalf("preferred node did not win: %#v", ranked)
	}
}

func TestRankPrefersLowerCPUPressure(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "hot", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 2, CPUUtilization: 92, MemoryTotal: 32 << 30, MemoryFree: 24 << 30}},
		{ID: "cool", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 2, CPUUtilization: 8, MemoryTotal: 32 << 30, MemoryFree: 24 << 30}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation"})
	if len(ranked) != 2 || ranked[0].Node.ID != "cool" {
		t.Fatalf("lower CPU pressure was not preferred: %#v", ranked)
	}
}

func TestRankBoundsMalformedWorkerTelemetry(t *testing.T) {
	now := time.Now().UTC()
	base := Capabilities{Tasks: []string{"generation"}, MaxConcurrent: 2}
	score := func(capabilities Capabilities, estimatedVRAM uint64) float64 {
		ranked := RankWithEstimate([]Node{{ID: "node", Connected: true, LastSeen: now, Capabilities: capabilities}}, Requirements{Task: "generation"}, estimatedVRAM)
		if len(ranked) != 1 {
			t.Fatalf("node disappeared while testing telemetry: %#v", ranked)
		}
		if math.IsNaN(ranked[0].Score) || math.IsInf(ranked[0].Score, 0) {
			t.Fatalf("invalid telemetry produced a non-finite score: %#v", ranked[0])
		}
		return ranked[0].Score
	}

	exactlyFree := base
	exactlyFree.MemoryTotal, exactlyFree.MemoryFree = 16<<30, 16<<30
	overreportedFree := base
	overreportedFree.MemoryTotal, overreportedFree.MemoryFree = 16<<30, ^uint64(0)
	if got, want := score(overreportedFree, 0), score(exactlyFree, 0); got != want {
		t.Fatalf("RAM free above total changed rank score: got %v, want %v", got, want)
	}

	unknownTotal := base
	unknownTotal.MemoryTotal, unknownTotal.MemoryFree = 0, ^uint64(0)
	if got, want := score(unknownTotal, 0), score(base, 0); got != want {
		t.Fatalf("unknown RAM total was not neutral: got %v, want %v", got, want)
	}

	for _, item := range []struct {
		name    string
		invalid int
		bounded int
		gpu     bool
	}{
		{name: "negative CPU", invalid: -500, bounded: 0},
		{name: "CPU above 100", invalid: 500, bounded: 100},
		{name: "negative GPU", invalid: -500, bounded: 0, gpu: true},
		{name: "GPU above 100", invalid: 500, bounded: 100, gpu: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			invalid, bounded := base, base
			if item.gpu {
				invalid.GPUs = []GPUCapability{{MemoryTotal: 8 << 30, MemoryFree: 8 << 30, Utilization: item.invalid}}
				bounded.GPUs = []GPUCapability{{MemoryTotal: 8 << 30, MemoryFree: 8 << 30, Utilization: item.bounded}}
			} else {
				invalid.CPUUtilization = item.invalid
				bounded.CPUUtilization = item.bounded
			}
			if got, want := score(invalid, 0), score(bounded, 0); got != want {
				t.Fatalf("out-of-range utilization changed rank score: got %v, want %v", got, want)
			}
		})
	}

	overreportedVRAM := base
	overreportedVRAM.GPUs = []GPUCapability{{MemoryTotal: 8 << 30, MemoryFree: ^uint64(0)}}
	fullVRAM := base
	fullVRAM.GPUs = []GPUCapability{{MemoryTotal: 8 << 30, MemoryFree: 8 << 30}}
	if got, want := score(overreportedVRAM, 1<<30), score(fullVRAM, 1<<30); got != want {
		t.Fatalf("VRAM free above total created unbounded headroom: got %v, want %v", got, want)
	}
	if got := Rank([]Node{{ID: "invalid-vram", Connected: true, LastSeen: now, Capabilities: overreportedVRAM}}, Requirements{Task: "generation", MinFreeVRAM: 12 << 30}); len(got) != 0 {
		t.Fatalf("VRAM free above its known total satisfied an impossible hard requirement: %#v", got)
	}
}
