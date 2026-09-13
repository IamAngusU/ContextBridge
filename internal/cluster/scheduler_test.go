package cluster

import (
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

func TestBusyBrowserTabsDoNotHideLocalModelCapacity(t *testing.T) {
	now := time.Now().UTC()
	node := Node{ID: "mixed", Connected: true, LastSeen: now, Capabilities: Capabilities{
		Tasks: []string{"generation"}, Providers: []string{"browser", "ollama"},
		MaxConcurrent: 4, Running: 1, BrowserTabs: 1, BrowserBusy: 1,
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
