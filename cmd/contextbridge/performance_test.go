package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestRepresentativeHeartbeatMetricsExposeScopeAndArithmetic(t *testing.T) {
	metrics, err := representativeHeartbeatMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 {
		t.Fatalf("heartbeat rows = %d, want 2: %#v", len(metrics), metrics)
	}
	for _, metric := range metrics {
		if metric.PayloadBytes <= 0 || metric.IntervalMilliseconds != 5000 || metric.Scope == "" {
			t.Errorf("incomplete heartbeat metric: %#v", metric)
		}
		if metric.PayloadBytesPerMinute != int64(metric.PayloadBytes)*12 {
			t.Errorf("idle traffic arithmetic is inconsistent: %#v", metric)
		}
		if metric.PayloadBytesPerHour != int64(metric.PayloadBytes)*720 || metric.PayloadBytesPerHour != metric.PayloadBytesPerMinute*60 {
			t.Errorf("hourly idle traffic arithmetic is inconsistent: %#v", metric)
		}
	}
}

func TestEffectivePerformanceConcurrenciesNeverExceedSamples(t *testing.T) {
	tests := []struct {
		samples int
		want    []int
	}{
		{samples: 1, want: []int{1}},
		{samples: 3, want: []int{1}},
		{samples: 4, want: []int{1, 4}},
		{samples: 15, want: []int{1, 4}},
		{samples: 16, want: []int{1, 4, 16}},
		{samples: 63, want: []int{1, 4, 16}},
		{samples: 64, want: []int{1, 4, 16, 64}},
		{samples: 128, want: []int{1, 4, 16, 64}},
	}
	for _, test := range tests {
		if got := effectivePerformanceConcurrencies(test.samples); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("samples %d: concurrencies = %v, want %v", test.samples, got, test.want)
		}
	}
}

func TestMeasurePerformanceFileSizesCountsBinary(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "contextbridge-test")
	if err := os.WriteFile(binary, bytes.Repeat([]byte{'b'}, 17), 0600); err != nil {
		t.Fatal(err)
	}
	files, warnings, err := measurePerformanceFileSizes(binary)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || files.BinaryBytes != 17 {
		t.Fatalf("unexpected file measurement: %#v warnings=%#v", files, warnings)
	}
}

func TestMeasureIdleRelayReturnsResourceShapeWithoutThresholds(t *testing.T) {
	report, warning, err := measureIdleRelay(context.Background(), 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if report.DurationMilliseconds <= 0 || report.GoMemorySysBytes == 0 || report.GoHeapInUseBytes == 0 {
		t.Fatalf("idle report lacks structural fields: %#v warning=%q", report, warning)
	}
	// CPU and resident values may be unavailable on a future platform. The
	// warning is the auditable representation of that state; no speed or memory
	// ceiling is asserted in a regression test.
	if warning == "" && (report.CPUCorePercent == nil || report.CPUHostPercent == nil || report.ResidentKind == "") {
		t.Fatalf("supported OS sample lacks provenance: %#v", report)
	}
}

func TestPerformanceReportHasMachineReadableScopeAndTable(t *testing.T) {
	value := 0.5
	report := performanceReport{
		SchemaVersion: 1,
		Version:       "v0.test",
		Environment:   performanceEnvironment{GOOS: "test", GOARCH: "test", CPUs: 1},
		Operations: []cluster.BridgeOperationMetric{{
			Name: "relay_queue_submit_read_cancel", Concurrency: 1, Samples: 2, OperationsPerSample: 1,
			P50Microseconds: 1, P95Microseconds: 2, P99Microseconds: 2, ThroughputPerSec: 3,
		}},
		Resources: performanceResources{
			Files: performanceFileSizes{BinaryBytes: 1024},
			IdleRelay: performanceIdleRelay{
				DurationMilliseconds: 1000, CPUCorePercent: &value, CPUHostPercent: &value,
				GoHeapAllocBytes: 1, GoMemorySysBytes: 2,
			},
			Database: cluster.DatabaseGrowthMetric{Jobs: 1000, GrowthBytes: 4096, GrowthPer1000JobBytes: 4096},
		},
		Excludes:    []string{"model and provider inference"},
		Unavailable: []performanceUnavailableMetric{{Name: "ram_per_active_inference_job", Reason: "no inference is started"}},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"schema_version":1`)) || !bytes.Contains(raw, []byte(`"excludes"`)) || !bytes.Contains(raw, []byte(`"unavailable_metrics"`)) {
		t.Fatalf("JSON omitted audit scope: %s", raw)
	}
	var output bytes.Buffer
	printPerformanceReport(&output, report)
	for _, wanted := range []string{"Latency and throughput", "P50", "Resource footprint", "Provider/model inference", "Unavailable without inventing a workload"} {
		if !strings.Contains(output.String(), wanted) {
			t.Fatalf("table omitted %q:\n%s", wanted, output.String())
		}
	}
}
