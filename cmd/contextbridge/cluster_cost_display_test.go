package main

import (
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestFormatClusterCostNeverPresentsUnknownAsZero(t *testing.T) {
	unknown := formatClusterCost(cluster.Usage{CostStatus: cluster.CostUnknown, CostUnknownJobs: 4})
	if unknown != "[cost unknown · 4 jobs]" || strings.Contains(unknown, "$0") {
		t.Fatalf("unknown cost was misrepresented: %q", unknown)
	}

	known := formatClusterCost(cluster.Usage{
		CostStatus:       cluster.CostUpperBound,
		CostKnownJobs:    2,
		EstimatedCostUSD: 0.123,
		ReservedCostUSD:  0.25,
	})
	for _, wanted := range []string{"$0.123000", "upper bound", "2 jobs", "reservation basis $0.250000"} {
		if !strings.Contains(known, wanted) {
			t.Fatalf("known cost missing %q: %q", wanted, known)
		}
	}
}
