package cluster

import (
	"math"
	"testing"
)

func TestPriceUsageKeepsUnknownCostDistinctFromZero(t *testing.T) {
	usage := priceUsage(Usage{InputTokens: 10, OutputTokens: 5}, Pricing{})
	if usage.CostStatus != CostUnknown || usage.CostUnknownJobs != 1 || usage.CostKnownJobs != 0 || usage.EstimatedCostUSD != 0 {
		t.Fatalf("unpriced usage was not marked unknown: %#v", usage)
	}
}

func TestPriceUsagePreservesWorkerUpperBoundEvidence(t *testing.T) {
	usage := priceUsage(Usage{
		InputTokens: 10, OutputTokens: 5, CostStatus: CostUpperBound,
		CostSource: "provider peak table", ReservedCostUSD: 0.002, EstimatedCostUSD: 0.001,
	}, Pricing{Mode: "estimated", Source: "relay fallback", InputPerMillionUSD: 99})
	if usage.CostStatus != CostUpperBound || usage.CostKnownJobs != 1 || usage.CostUnknownJobs != 0 || usage.CostSource != "provider peak table" || usage.EstimatedCostUSD != 0.001 {
		t.Fatalf("worker cost evidence was overwritten: %#v", usage)
	}
}

func TestPriceUsageUsesExplicitRelayEstimate(t *testing.T) {
	usage := priceUsage(Usage{InputTokens: 1_000_000, OutputTokens: 500_000, ComputeMS: 3_600_000}, Pricing{
		Mode: "estimated", Source: "operator rates", ComputePerHourUSD: 1, InputPerMillionUSD: 2, OutputPerMillionUSD: 4,
	})
	if usage.CostStatus != CostEstimated || usage.CostSource != "operator rates" || usage.CostKnownJobs != 1 || usage.EstimatedCostUSD != 5 {
		t.Fatalf("explicit relay pricing was not applied: %#v", usage)
	}
}

func TestMergeUsageMarksMixedCostEvidencePartial(t *testing.T) {
	total := Usage{}
	mergeUsage(&total, Usage{CostStatus: CostUpperBound, CostKnownJobs: 1, EstimatedCostUSD: 0.1, CostSource: "one"})
	mergeUsage(&total, Usage{CostStatus: CostUnknown, CostUnknownJobs: 1})
	if total.CostStatus != CostPartial || total.CostKnownJobs != 1 || total.CostUnknownJobs != 1 || total.EstimatedCostUSD != 0.1 {
		t.Fatalf("mixed cost evidence looked complete: %#v", total)
	}
}

func TestExtractUsageCarriesBoundedCostEvidenceFromLocalBridge(t *testing.T) {
	usage := extractUsage([]byte(`{"output":{"input_tokens":12,"output_tokens":4,"cost_status":"upper_bound","cost_source":"reviewed table","reserved_cost_usd":0.002,"estimated_cost_usd":0.001}}`))
	if usage.CostStatus != CostUpperBound || usage.CostSource != "reviewed table" || math.Abs(usage.ReservedCostUSD-0.002) > 1e-12 || math.Abs(usage.EstimatedCostUSD-0.001) > 1e-12 {
		t.Fatalf("local bridge cost evidence was lost: %#v", usage)
	}
}
