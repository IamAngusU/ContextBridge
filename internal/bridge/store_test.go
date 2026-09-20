package bridge

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdapterHeartbeatAllowsDelayedWorkerWakeButNotStaleOrPaused(t *testing.T) {
	store := &Store{adapter: AdapterClientStatus{Connected: true, State: "waiting", LastSeen: time.Now().Add(-75 * time.Second)}}
	if !store.AdapterStatus().Connected {
		t.Fatal("a delayed but healthy MV3 alarm should not make the relay flicker offline")
	}
	store.adapter.LastSeen = time.Now().Add(-95 * time.Second)
	if store.AdapterStatus().Connected {
		t.Fatal("a genuinely stale adapter relay must be reported offline")
	}
	store.adapter.LastSeen = time.Now()
	store.adapter.Connected = false
	store.adapter.State = "paused"
	if store.AdapterStatus().Connected {
		t.Fatal("a deliberate disconnect must be immediate")
	}
}

func TestMetricsSaturateAndBoundDimensions(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.metrics.JobsTotal = math.MaxUint64
	store.metrics.LatencyTotalMS = math.MaxUint64
	store.metrics.ProviderLatency["ollama"] = math.MaxUint64
	for index := 0; index < maximumMetricDimensions+100; index++ {
		store.RecordCompleted(
			Job{ID: fmt.Sprintf("metric-%d", index), Route: fmt.Sprintf("route-%d", index), Task: "generation"},
			Output{Mode: "text", Provider: "ollama", Model: fmt.Sprintf("model-%d", index), LatencyMS: 1},
		)
	}
	metrics := store.Metrics()
	if metrics.JobsTotal != math.MaxUint64 || metrics.LatencyTotalMS != math.MaxUint64 || metrics.ProviderLatency["ollama"] != math.MaxUint64 {
		t.Fatalf("metric counter wrapped: %#v", metrics)
	}
	if len(metrics.ByRoute) > maximumMetricDimensions || len(metrics.ByModel) > maximumMetricDimensions || metrics.ByRoute["other"] == 0 {
		t.Fatalf("metric dimensions were not bounded: routes=%d models=%d other=%d", len(metrics.ByRoute), len(metrics.ByModel), metrics.ByRoute["other"])
	}
}

func TestOversizedMetricsFileIsIgnored(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "metrics.json"), make([]byte, maximumMetricsFileBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if metrics := store.Metrics(); metrics.JobsTotal != 0 || len(metrics.ByRoute) != 0 {
		t.Fatalf("oversized metrics state was loaded: %#v", metrics)
	}
}

func TestMetricsPersistProviderModelAndFlags(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	store.RecordCompleted(
		Job{ID: "metrics-test", Route: "inkwall", Task: "moderation"},
		Output{Mode: "decision", Provider: "ollama", Model: "qwen", LatencyMS: 125, Decision: &Decision{Verdict: "review", Flags: []string{"advertising"}}},
	)

	reloaded, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	metrics := reloaded.Metrics()
	if metrics.JobsTotal != 1 || metrics.ByProvider["ollama"] != 1 || metrics.ByModel["qwen"] != 1 || metrics.ByFlag["advertising"] != 1 {
		t.Fatalf("unexpected persisted metrics: %#v", metrics)
	}
	if metrics.ProviderLatency["ollama"] != 125 || metrics.ProviderSamples["ollama"] != 1 || metrics.ProviderFailures["ollama"] != 0 {
		t.Fatalf("unexpected provider metrics: %#v", metrics)
	}
}

func TestCloneOutputPreservesEmptyFlagsArray(t *testing.T) {
	output := cloneOutput(Output{Mode: "decision", Decision: &Decision{Verdict: "allow", Flags: []string{}}})
	if output.Decision == nil || output.Decision.Flags == nil || len(output.Decision.Flags) != 0 {
		t.Fatalf("empty flags must remain an empty JSON array: %#v", output.Decision)
	}
}
