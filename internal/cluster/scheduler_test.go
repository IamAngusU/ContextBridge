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
