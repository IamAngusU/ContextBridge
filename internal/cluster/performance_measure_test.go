package cluster

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestDurationPercentileUsesNearestRankWithoutChangingInput(t *testing.T) {
	values := []time.Duration{40 * time.Microsecond, 10 * time.Microsecond, 30 * time.Microsecond, 20 * time.Microsecond}
	if got := durationPercentile(values, 0.50); got != 20 {
		t.Fatalf("p50 = %v us, want 20", got)
	}
	if got := durationPercentile(values, 0.95); got != 40 {
		t.Fatalf("p95 = %v us, want 40", got)
	}
	if values[0] != 40*time.Microsecond || values[1] != 10*time.Microsecond {
		t.Fatalf("percentile changed caller samples: %#v", values)
	}
}

func TestOperationMetricKeepsClockResolutionRepresentable(t *testing.T) {
	metric := operationMetric("fast", 1, []time.Duration{0}, 0, 1)
	if metric.ThroughputPerSec != 0 {
		t.Fatalf("zero wall time must remain representable in JSON, got %v", metric.ThroughputPerSec)
	}
}

func TestMeasureBridgeOnlyRejectsUnsafeBounds(t *testing.T) {
	valid := BridgeMeasurementOptions{Samples: 1, Warmup: 0, Concurrencies: []int{1}, DatabaseJobs: 1}
	cases := []BridgeMeasurementOptions{
		{Samples: 0, Warmup: valid.Warmup, Concurrencies: valid.Concurrencies, DatabaseJobs: valid.DatabaseJobs},
		{Samples: valid.Samples, Warmup: -1, Concurrencies: valid.Concurrencies, DatabaseJobs: valid.DatabaseJobs},
		{Samples: valid.Samples, Warmup: valid.Warmup, Concurrencies: []int{MaximumWorkerConcurrency + 1}, DatabaseJobs: valid.DatabaseJobs},
		{Samples: valid.Samples, Warmup: valid.Warmup, Concurrencies: []int{1, 1}, DatabaseJobs: valid.DatabaseJobs},
		{Samples: valid.Samples, Warmup: valid.Warmup, Concurrencies: valid.Concurrencies, DatabaseJobs: 0},
	}
	for index, options := range cases {
		if _, err := MeasureBridgeOnly(context.Background(), options); err == nil {
			t.Errorf("case %d accepted invalid options: %#v", index, options)
		}
	}
}

func TestMeasureBridgeOnlyProducesAuditableShape(t *testing.T) {
	report, err := MeasureBridgeOnly(context.Background(), BridgeMeasurementOptions{
		Samples: 4, Warmup: 1, Concurrencies: []int{1, 4}, DatabaseJobs: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Operations) != 4 {
		t.Fatalf("operation rows = %d, want 4: %#v", len(report.Operations), report.Operations)
	}
	for _, operation := range report.Operations {
		if operation.Samples != 4 {
			t.Errorf("%s/%d samples = %d", operation.Name, operation.Concurrency, operation.Samples)
		}
		if operation.P50Microseconds > operation.P95Microseconds || operation.P95Microseconds > operation.P99Microseconds {
			t.Errorf("unordered percentiles: %#v", operation)
		}
		if operation.ThroughputPerSec < 0 || math.IsInf(operation.ThroughputPerSec, 0) || math.IsNaN(operation.ThroughputPerSec) {
			t.Errorf("unrepresentable measured throughput: %#v", operation)
		}
	}
	if report.Database.Jobs != 10 || report.Database.AfterBytes < report.Database.BaselineBytes || report.Database.GrowthBytes < 0 {
		t.Fatalf("invalid database growth shape: %#v", report.Database)
	}
	if report.Database.GrowthPer1000JobBytes != report.Database.GrowthPerJobBytes*1000 {
		t.Fatalf("database normalization is inconsistent: %#v", report.Database)
	}
}
