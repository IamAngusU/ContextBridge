package cluster

import "testing"

func TestMergeUsageKeepsOnlyJobAttributedResourcePeaks(t *testing.T) {
	target := Usage{ResourceScope: "job", PeakVRAMBytes: 4, PeakRAMBytes: 5, PeakGPUUtilization: 6}
	mergeUsage(&target, Usage{PeakVRAMBytes: 99, PeakRAMBytes: 99, PeakGPUUtilization: 99})
	if target.PeakVRAMBytes != 4 || target.PeakRAMBytes != 5 || target.PeakGPUUtilization != 6 {
		t.Fatalf("unattributed counters changed pipeline resource peaks: %#v", target)
	}

	mergeUsage(&target, Usage{ResourceScope: "job", PeakVRAMBytes: 7, PeakRAMBytes: 8, PeakGPUUtilization: 9})
	if target.ResourceScope != "job" || target.PeakVRAMBytes != 7 || target.PeakRAMBytes != 8 || target.PeakGPUUtilization != 9 {
		t.Fatalf("attributed counters were not merged: %#v", target)
	}
}
