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
