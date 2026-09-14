package bridge

import (
	"testing"
	"time"
)

func TestBrowserHeartbeatAllowsDelayedWorkerWakeButNotStaleOrPaused(t *testing.T) {
	store := &Store{browser: BrowserClientStatus{Connected: true, State: "waiting", LastSeen: time.Now().Add(-75 * time.Second)}}
	if !store.BrowserStatus().Connected {
		t.Fatal("a delayed but healthy MV3 alarm should not make the relay flicker offline")
	}
	store.browser.LastSeen = time.Now().Add(-95 * time.Second)
	if store.BrowserStatus().Connected {
		t.Fatal("a genuinely stale browser relay must be reported offline")
	}
	store.browser.LastSeen = time.Now()
	store.browser.Connected = false
	store.browser.State = "paused"
	if store.BrowserStatus().Connected {
		t.Fatal("a deliberate disconnect must be immediate")
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
