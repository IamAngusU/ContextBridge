package cluster

import (
	"math"
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
