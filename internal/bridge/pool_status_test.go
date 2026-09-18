package bridge

import (
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestSummarizePoolIncludesOnlyOnlineCapacity(t *testing.T) {
	nodes := []cluster.Node{
		{ID: "secret-a", Name: "workstation", Connected: true, Capabilities: cluster.Capabilities{CPUCores: 24, MemoryTotal: 32 << 30, MemoryFree: 12 << 30, MaxConcurrent: 4, Running: 1, GPUs: []cluster.GPUCapability{{MemoryTotal: 10 << 30, MemoryFree: 7 << 30}}}},
		{ID: "secret-b", Name: "cpu-rack", Connected: true, Capabilities: cluster.Capabilities{CPUCores: 64, MemoryTotal: 128 << 30, MemoryFree: 80 << 30, MaxConcurrent: 8, Running: 2}},
		{ID: "offline", Name: "offline", Connected: false, Capabilities: cluster.Capabilities{CPUCores: 999, MaxConcurrent: 99}},
	}
	got := summarizePool(nodes)
	if got.NodesTotal != 3 || got.NodesOnline != 2 || got.SlotsTotal != 12 || got.SlotsBusy != 3 {
		t.Fatalf("unexpected pool counts: %#v", got)
	}
	if got.CPUCores != 88 || got.GPUs != 1 || got.ZeroGPUNodes != 1 || got.VRAMTotal != 10<<30 || got.VRAMFree != 7<<30 {
		t.Fatalf("unexpected aggregate capacity: %#v", got)
	}
}
