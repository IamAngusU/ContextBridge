package bridge

import (
	"math"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestSummarizePoolSaturatesUntrustedTotals(t *testing.T) {
	nodes := []cluster.Node{
		{Connected: true, Capabilities: cluster.Capabilities{Running: math.MaxInt, MaxConcurrent: math.MaxInt, CPUCores: math.MaxInt, MemoryTotal: math.MaxUint64, MemoryFree: math.MaxUint64, GPUs: []cluster.GPUCapability{{MemoryTotal: math.MaxUint64, MemoryFree: math.MaxUint64}}}},
		{Connected: true, Capabilities: cluster.Capabilities{Running: 1, MaxConcurrent: 1, CPUCores: 1, MemoryTotal: 1, MemoryFree: 1, GPUs: []cluster.GPUCapability{{MemoryTotal: 1, MemoryFree: 1}}}},
	}
	summary := summarizePool(nodes)
	if summary.SlotsBusy != math.MaxInt || summary.SlotsTotal != math.MaxInt || summary.CPUCores != math.MaxInt {
		t.Fatalf("integer pool totals wrapped: %#v", summary)
	}
	if summary.MemoryTotal != math.MaxUint64 || summary.MemoryFree != math.MaxUint64 || summary.VRAMTotal != math.MaxUint64 || summary.VRAMFree != math.MaxUint64 {
		t.Fatalf("byte pool totals wrapped: %#v", summary)
	}
}
