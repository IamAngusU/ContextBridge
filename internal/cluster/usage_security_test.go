package cluster

import (
	"math"
	"testing"
	"time"
)

func TestNonNegativeDurationMillisecondsDoesNotWrap(t *testing.T) {
	if got := nonNegativeDurationMilliseconds(-time.Second); got != 0 {
		t.Fatalf("negative duration became %d ms", got)
	}
	if got := nonNegativeDurationMilliseconds(time.Duration(math.MaxInt64)); got != uint64(math.MaxInt64/int64(time.Millisecond)) {
		t.Fatalf("maximum duration conversion = %d", got)
	}
}

func TestExtractUsageReadsOnlyTypedFirstPartyOutputAccounting(t *testing.T) {
	usage := extractUsage([]byte(`{"output":{"input_tokens":42,"output_tokens":7,"total_tokens":49,"estimated_cost_usd":0.125,"cost_status":"actual","cost_source":"provider invoice","json":{"input_tokens":999999,"estimated_cost_usd":999999,"cost_status":"actual","cost_source":"model claim"}},"hardware":{"input_tokens":888888}}`))
	if usage.InputTokens != 42 || usage.OutputTokens != 7 || usage.TotalTokens != 49 {
		t.Fatalf("typed output accounting was not preserved: %#v", usage)
	}
	if usage.CostStatus != CostActual || usage.CostSource != "provider invoice" || usage.EstimatedCostUSD != 0.125 {
		t.Fatalf("model-produced lookalikes affected cost evidence: %#v", usage)
	}
}

func TestExtractUsageRejectsInvalidTypedAccounting(t *testing.T) {
	for _, raw := range []string{
		`{"output":{"input_tokens":-1,"estimated_cost_usd":2,"cost_status":"actual"}}`,
		`{"output":{"input_tokens":1e40,"estimated_cost_usd":2,"cost_status":"actual"}}`,
		`{"output":{"input_tokens":1000000000001,"estimated_cost_usd":2,"cost_status":"actual"}}`,
	} {
		usage := extractUsage([]byte(raw))
		if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.TotalTokens != 0 || usage.CostStatus != CostUnknown || usage.EstimatedCostUSD != 0 {
			t.Fatalf("invalid accounting did not fail closed for %s: %#v", raw, usage)
		}
	}
}

func TestPriceUsageContainsMaliciousWorkerAccounting(t *testing.T) {
	usage := priceUsage(Usage{
		InputTokens: math.MaxUint64, OutputTokens: math.MaxUint64, TotalTokens: math.MaxUint64,
		ComputeMS: math.MaxUint64, CostStatus: CostActual, EstimatedCostUSD: math.NaN(),
		ResourceScope: "node", PeakRAMBytes: math.MaxUint64, PeakVRAMBytes: math.MaxUint64, PeakGPUUtilization: -50,
	}, Pricing{})
	if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.TotalTokens != 0 || usage.ComputeMS != 0 {
		t.Fatalf("oversized accounting survived normalization: %#v", usage)
	}
	if usage.CostStatus != CostUnknown || math.IsNaN(usage.EstimatedCostUSD) || usage.CostUnknownJobs != 1 {
		t.Fatalf("invalid cost evidence did not fail closed: %#v", usage)
	}
	if usage.ResourceScope != "" || usage.PeakRAMBytes != 0 || usage.PeakVRAMBytes != 0 || usage.PeakGPUUtilization != 0 {
		t.Fatalf("unattributed resource values survived normalization: %#v", usage)
	}
}

func TestUsageAggregationSaturates(t *testing.T) {
	target := Usage{InputTokens: math.MaxUint64, EstimatedCostUSD: 999_999_999}
	mergeUsage(&target, Usage{InputTokens: 1, EstimatedCostUSD: 10})
	if target.InputTokens != math.MaxUint64 || target.EstimatedCostUSD != 1_000_000_000 {
		t.Fatalf("usage aggregation wrapped or exceeded its bound: %#v", target)
	}
}
