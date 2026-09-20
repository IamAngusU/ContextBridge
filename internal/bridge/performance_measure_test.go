package bridge

import (
	"context"
	"math"
	"testing"
)

func TestMeasureArtifactVerificationProducesAuditableShape(t *testing.T) {
	metrics, err := MeasureArtifactVerification(context.Background(), 4, 1, []int{1, 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 {
		t.Fatalf("metric rows = %d, want 2", len(metrics))
	}
	for _, metric := range metrics {
		if metric.Name != "artifact_verification_64kib" || metric.Samples != 4 {
			t.Fatalf("unexpected metric: %#v", metric)
		}
		if metric.P50Microseconds > metric.P95Microseconds || metric.P95Microseconds > metric.P99Microseconds {
			t.Errorf("unordered percentiles: %#v", metric)
		}
		if metric.ThroughputPerSec < 0 || math.IsInf(metric.ThroughputPerSec, 0) || math.IsNaN(metric.ThroughputPerSec) {
			t.Errorf("unrepresentable throughput: %#v", metric)
		}
	}
}

func TestMeasureArtifactVerificationRejectsInvalidConcurrency(t *testing.T) {
	if _, err := MeasureArtifactVerification(context.Background(), 1, 0, []int{0}); err == nil {
		t.Fatal("accepted invalid concurrency")
	}
}
