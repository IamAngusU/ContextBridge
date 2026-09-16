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

func TestProviderRequirementSelectsReadyBrowserWorker(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "local-model", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, Providers: []string{"ollama"}, MaxConcurrent: 1}},
		{ID: "web-chat", Connected: true, LastSeen: now, Capabilities: Capabilities{Tasks: []string{"generation"}, Providers: []string{"browser"}, MaxConcurrent: 1}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation", Provider: "browser"})
	if len(ranked) != 1 || ranked[0].Node.ID != "web-chat" {
		t.Fatalf("browser provider requirement was not enforced: %#v", ranked)
	}
}

func TestBrowserProfileRequirementIsAHardReadyTabFilter(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "chatgpt", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Tasks: []string{"generation"}, Providers: []string{"browser"}, MaxConcurrent: 2,
			BrowserTabs: 1, BrowserSessions: []BrowserSessionCapability{{Profile: "chatgpt", State: "waiting"}},
		}},
		{ID: "gemini-busy", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Tasks: []string{"generation"}, Providers: []string{"browser"}, MaxConcurrent: 2,
			BrowserTabs: 1, BrowserBusy: 1, BrowserSessions: []BrowserSessionCapability{{Profile: "gemini", State: "working"}},
		}},
		{ID: "gemini-ready", Connected: true, LastSeen: now, Capabilities: Capabilities{
			Tasks: []string{"generation"}, Providers: []string{"browser"}, MaxConcurrent: 2,
			BrowserTabs: 1, BrowserSessions: []BrowserSessionCapability{{Profile: "GeMiNi", State: "waiting"}},
		}},
	}
	ranked := Rank(nodes, Requirements{Task: "generation", Provider: "browser", BrowserProfile: "gemini"})
	if len(ranked) != 1 || ranked[0].Node.ID != "gemini-ready" {
		t.Fatalf("profile-specific job escaped to a wrong or busy tab: %#v", ranked)
	}
	if got := Rank(nodes[:2], Requirements{Task: "generation", Provider: "browser", BrowserProfile: "gemini"}); len(got) != 0 {
		t.Fatalf("busy requested profile was treated as ready: %#v", got)
	}
	if got := Rank(nodes, Requirements{Task: "generation", Provider: "ollama", BrowserProfile: "gemini"}); len(got) != 0 {
		t.Fatalf("browser profile crossed the provider boundary: %#v", got)
	}
}

func TestBrowserProfileAndModelMustMatchTheSameReadyTab(t *testing.T) {
	node := Node{ID: "split-browser", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser"}, MaxConcurrent: 2, BrowserTabs: 2,
		BrowserSessions: []BrowserSessionCapability{
			{TabID: 1, Profile: "chatgpt", State: "waiting", CurrentModel: "GPT-5.6 Sol", ModelChoices: []string{"GPT-5.6 Sol", "GPT-5.5"}},
			{TabID: 2, Profile: "gemini", State: "waiting", CurrentModel: "3.6 Flash", ModelChoices: []string{"3.6 Flash", "3.1 Pro"}},
		},
		// The node-wide inventory intentionally contains both labels. It must not
		// allow the scheduler to combine a profile from one tab with a model that
		// exists only on another tab.
		Models: []ModelCapability{
			{Name: "GPT-5.6 Sol", Provider: "browser", Tasks: []string{"generation"}},
			{Name: "3.1 Pro", Provider: "browser", Tasks: []string{"generation"}},
		},
	}}

	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", Model: "3.1 Pro"}); len(got) != 0 {
		t.Fatalf("profile and model were combined across tabs: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "browser", BrowserProfile: "gemini", Model: "3.1 Pro"}); len(got) != 1 {
		t.Fatalf("valid same-tab browser model choice was rejected: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", Model: "GPT-5.6 Sol"}); len(got) != 1 {
		t.Fatalf("current model from an older per-tab advertisement was rejected: %#v", got)
	}

	legacy := node
	legacy.ID = "legacy-browser"
	legacy.Capabilities.BrowserSessions = nil
	if got := Rank([]Node{legacy}, Requirements{Task: "generation", Provider: "browser", Model: "GPT-5.6 Sol"}); len(got) != 1 {
		t.Fatalf("legacy model-only browser inventory lost compatibility: %#v", got)
	}
}

func TestBrowserTabModelChoiceFoldsNBSPAndExcludesBusyTabs(t *testing.T) {
	node := Node{ID: "browser", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser"}, MaxConcurrent: 2, BrowserTabs: 2, BrowserBusy: 1,
		BrowserSessions: []BrowserSessionCapability{
			{TabID: 1, Profile: "chatgpt", State: "working", ModelChoices: []string{"GPT-5.6\u00a0Sol"}},
			{TabID: 2, Profile: "gemini", State: "waiting", ModelChoices: []string{"3.6 Flash"}},
		},
		Models: []ModelCapability{{Name: "GPT-5.6\u00a0Sol", Provider: "browser", Tasks: []string{"generation"}}},
	}}

	request := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", Model: "GPT-5.6 Sol"}
	if got := Rank([]Node{node}, request); len(got) != 0 {
		t.Fatalf("busy matching browser tab was considered schedulable: %#v", got)
	}
	node.Capabilities.BrowserSessions[0].State = "waiting"
	node.Capabilities.BrowserBusy = 0
	if got := Rank([]Node{node}, request); len(got) != 1 {
		t.Fatalf("NBSP-equivalent model label was not accepted on a ready tab: %#v", got)
	}
}

func TestBrowserSessionBoundTabsAreReservedForAffinityAndRecoverMovedConversation(t *testing.T) {
	sessions := []BrowserSessionCapability{
		{TabID: 41, Profile: "chatgpt", State: "session_bound", CurrentModel: "GPT-5.6 Sol"},
		{TabID: 42, Profile: "chatgpt", State: "waiting", CurrentModel: "GPT-5.6 Sol"},
	}
	newSession := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", Model: "GPT-5.6 Sol"}
	selected, ok := selectReadyBrowserSession(sessions, newSession)
	if !ok || selected.TabID != 42 {
		t.Fatalf("new session stole an occupied browser conversation: %#v %v", selected, ok)
	}
	affinity := newSession
	affinity.BrowserTabID = 41
	affinity.BrowserSessionRecovery = true
	selected, ok = selectReadyBrowserSession(sessions, affinity)
	if !ok || selected.TabID != 41 || selectedBrowserTabBinding(affinity, selected) != 41 {
		t.Fatalf("same-session affinity did not reuse its bound tab: %#v %v", selected, ok)
	}

	// The old tab vanished and the saved conversation was reopened elsewhere.
	// The relay must stay on the same node but leave execution unpinned so the
	// extension can prove the saved URL and report the replacement tab.
	sessions = []BrowserSessionCapability{
		{TabID: 42, Profile: "chatgpt", State: "waiting", CurrentModel: "GPT-5.6 Sol"},
		{TabID: 43, Profile: "chatgpt", State: "session_bound", CurrentModel: "GPT-5.6 Sol"},
	}
	selected, ok = selectReadyBrowserSession(sessions, affinity)
	if !ok || selectedBrowserTabBinding(affinity, selected) != 0 {
		t.Fatalf("moved-session recovery stayed pinned to a vanished tab: %#v %v", selected, ok)
	}

	workingOld := append([]BrowserSessionCapability{{TabID: 41, Profile: "chatgpt", State: "working", CurrentModel: "GPT-5.6 Sol"}}, sessions...)
	if selected, ok = selectReadyBrowserSession(workingOld, affinity); ok {
		t.Fatalf("a busy existing session silently escaped to another tab: %#v", selected)
	}
	navigatedOld := append([]BrowserSessionCapability{{TabID: 41, Profile: "gemini", State: "waiting", CurrentModel: "3.6 Flash"}}, sessions...)
	if selected, ok = selectReadyBrowserSession(navigatedOld, affinity); !ok || selectedBrowserTabBinding(affinity, selected) != 0 {
		t.Fatalf("an incompatible reused tab id blocked saved-URL recovery: %#v %v", selected, ok)
	}
}

func TestOpaqueBrowserSessionKeyOverridesStaleTabPlacement(t *testing.T) {
	requirements := Requirements{
		Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", Model: "GPT-5.6 Sol",
		BrowserTabID: 41, BrowserSessionRecovery: true, BrowserSessionKey: "cb:" + strings.Repeat("a", 64),
	}
	sessions := []BrowserSessionCapability{
		{TabID: 41, Profile: "chatgpt", State: "session_bound", SessionKeySupported: true, CurrentModel: "GPT-5.6 Sol"},
		{TabID: 42, Profile: "chatgpt", State: "waiting", SessionKeySupported: true, SessionKey: requirements.BrowserSessionKey, CurrentModel: "GPT-5.6 Sol"},
	}
	selected, ok := selectReadyBrowserSession(sessions, requirements)
	if !ok || selected.TabID != 42 || selectedBrowserTabBinding(requirements, selected) != 42 {
		t.Fatalf("live opaque session evidence did not override stale tab placement: %#v %v", selected, ok)
	}
	sessions = append(sessions, BrowserSessionCapability{
		TabID: 43, Profile: "chatgpt", State: "waiting", SessionKeySupported: true,
		SessionKey: requirements.BrowserSessionKey, CurrentModel: "GPT-5.6 Sol",
	})
	if selected, ok = selectReadyBrowserSession(sessions, requirements); ok {
		t.Fatalf("duplicate session-key claims did not fail closed: %#v", selected)
	}
}

func TestOpaqueBrowserSessionKeyEvidenceIsProfileScoped(t *testing.T) {
	key := "cb:" + strings.Repeat("a", 64)
	sessions := []BrowserSessionCapability{
		{TabID: 41, Profile: "chatgpt", State: "session_bound", SessionKeySupported: true, SessionKey: key},
		{TabID: 42, Profile: "gemini", State: "session_bound", SessionKeySupported: true, SessionKey: key},
		{TabID: 43, Profile: "custom-secure", State: "session_bound", SessionKeySupported: true, SessionKey: key},
	}
	for _, profile := range []string{"chatgpt", "gemini", "custom-secure"} {
		selected, ok := selectReadyBrowserSession(sessions, Requirements{
			Task: "generation", Provider: "browser", BrowserProfile: profile, BrowserSessionKey: key,
		})
		if !ok || selected.Profile != profile {
			t.Fatalf("opaque session evidence crossed profile %q: %#v %v", profile, selected, ok)
		}
	}
}

func TestFreshBrowserChatCanUseBoundTabOnlyAsLauncher(t *testing.T) {
	requirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", BrowserFreshChat: true}
	sessions := []BrowserSessionCapability{
		{TabID: 11, Profile: "chatgpt", State: "session_bound", SessionKeySupported: true, CanCreateFreshChat: true},
		{TabID: 12, Profile: "chatgpt", State: "session_bound", SessionKeySupported: true, CanCreateFreshChat: true},
	}
	selected, ok := selectReadyBrowserSession(sessions, requirements)
	if !ok || selected.TabID != 11 {
		t.Fatalf("fresh job could not use a bound tab as a deterministic launcher: %#v %v", selected, ok)
	}
	sessions[0].CanCreateFreshChat = false
	sessions[1].CanCreateFreshChat = false
	if selected, ok = selectReadyBrowserSession(sessions, requirements); ok {
		t.Fatalf("fresh job used a launcher without verified creation capability: %#v", selected)
	}
	if selected, ok = selectReadyBrowserSession([]BrowserSessionCapability{{
		TabID: 13, Profile: "chatgpt", State: "waiting", SessionKeySupported: true,
		DefaultFreshChat: true, CanCreateFreshChat: false,
	}}, Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt"}); ok {
		t.Fatalf("extension default fresh mode leased a waiting tab without creation capability: %#v", selected)
	}
	sessions[1].CanCreateFreshChat = true
	sessions[1].DefaultFreshChat = true
	requirements.BrowserFreshChat = false
	if selected, ok = selectReadyBrowserSession(sessions, requirements); !ok || selected.TabID != 12 {
		t.Fatalf("extension default fresh-chat mode was not routable: %#v %v", selected, ok)
	}
	requirements.BrowserTabID = 99
	if selected, ok = selectReadyBrowserSession(sessions, requirements); ok {
		t.Fatalf("a missing established session silently launched another chat: %#v", selected)
	}
}

func TestBusyBrowserTabsDoNotHideLocalModelCapacity(t *testing.T) {
	now := time.Now().UTC()
	node := Node{ID: "mixed", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser", "ollama"},
		AutomaticTasks: map[string][]string{"ollama": {"generation"}},
		Models:         []ModelCapability{{Name: "local-text", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}},
		MaxConcurrent:  4, Running: 1, BrowserTabs: 1, BrowserBusy: 1,
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "browser"}); len(got) != 0 {
		t.Fatalf("browser job routed to a worker with no free tab: %#v", got)
	}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "ollama"}); len(got) != 1 {
		t.Fatalf("busy browser tab incorrectly blocked a local-model job: %#v", got)
	}
}

func TestModelCapabilityCannotCrossProviderBoundary(t *testing.T) {
	node := Node{ID: "mixed", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser", "ollama"}, MaxConcurrent: 2,
		Models: []ModelCapability{{Name: "web-vision", Provider: "browser", Vision: true, Tasks: []string{"generation", "vision"}}},
	}}
	for _, request := range []Requirements{
		{Task: "vision", Provider: "ollama", Vision: true},
		{Task: "generation", Provider: "ollama", Model: "web-vision"},
	} {
		if got := Rank([]Node{node}, request); len(got) != 0 {
			t.Fatalf("browser-only model leaked into Ollama capability: %#v", got)
		}
	}
	if got := Rank([]Node{node}, Requirements{Task: "vision", Provider: "browser", Vision: true}); len(got) != 1 {
		t.Fatalf("valid browser vision job was excluded: %#v", got)
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

func TestBrowserProviderRequiresAnAvailableTab(t *testing.T) {
	node := Node{ID: "relay-only", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser"}, AutomaticTasks: map[string][]string{"browser": {"generation"}}, MaxConcurrent: 2,
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "browser"}); len(got) != 0 {
		t.Fatalf("browser job was routed without any attached tab: %#v", got)
	}
	node.Capabilities.BrowserTabs = 1
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "browser"}); len(got) != 1 {
		t.Fatalf("available browser tab was rejected: %#v", got)
	}
}

func TestUnscopedModelCannotSatisfyExplicitProvider(t *testing.T) {
	node := Node{ID: "legacy-mixed", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser", "ollama"}, MaxConcurrent: 2,
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
		Tasks: []string{"generation"}, Providers: []string{"browser", "ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}}, MaxConcurrent: 2,
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

func TestBrowserModelLabelsFoldUnicodeWhitespaceOnly(t *testing.T) {
	node := Node{ID: "browser", Connected: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser", "ollama"}, MaxConcurrent: 2,
		Models: []ModelCapability{
			{Name: "3.6\u00a0Flash", Provider: "browser", Tasks: []string{"generation"}},
			{Name: "local\u00a0model", Provider: "ollama", Tasks: []string{"generation"}},
		},
	}}
	if got := Rank([]Node{node}, Requirements{Task: "generation", Provider: "browser", Model: "3.6 Flash"}); len(got) != 1 {
		t.Fatalf("browser label with NBSP rejected ordinary user spaces: %#v", got)
	}
	for _, request := range []Requirements{
		{Task: "generation", Provider: "browser", Model: "3.1 Pro"},
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
