package bridge

import (
	"testing"
)

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
