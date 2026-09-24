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

func TestScopedAdapterCapabilitiesFencePrincipalEndpointAndLeaseGeneration(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterClientStatus{
		State: "waiting", Ready: true, ActiveEndpoints: 1,
		Endpoints: []AdapterEndpointStatus{{ID: 9, Profile: "shared", State: "idle"}},
	}
	capA, err := store.RecordScopedAdapterHeartbeat("adapter-a", heartbeat)
	if err != nil || len(capA) != 1 {
		t.Fatalf("could not register adapter A endpoint: %#v %v", capA, err)
	}
	capB, err := store.RecordScopedAdapterHeartbeat("adapter-b", heartbeat)
	if err != nil || len(capB) != 1 {
		t.Fatalf("could not register adapter B endpoint: %#v %v", capB, err)
	}
	store.Queue(Job{ID: "scoped-fence", ContextBridgeAdapterEndpointID: 9, ContextBridgeAdapterPrincipal: "adapter-a", Output: OutputSpec{Mode: "text"}}, map[string]interface{}{"name": "shared"}, time.Minute)
	if work, err := store.NextScopedAdapterJobForEndpoint("adapter-b", "shared", 9, capA[0].EndpointCapability, time.Minute); err == nil || work != nil {
		t.Fatal("an endpoint capability crossed adapter principal identity")
	}
	if work, err := store.NextScopedAdapterJobForEndpoint("adapter-b", "shared", 9, capB[0].EndpointCapability, time.Minute); err != nil || work != nil {
		t.Fatalf("another principal claimed endpoint-pinned work with its own capability: %#v %v", work, err)
	}
	work, err := store.NextScopedAdapterJobForEndpoint("adapter-a", "shared", 9, capA[0].EndpointCapability, time.Minute)
	if err != nil || work == nil {
		t.Fatalf("valid scoped lease failed: %#v %v", work, err)
	}
	if store.RenewScoped("scoped-fence", work.LeaseGeneration, "adapter-b", work.LeaseCapability, time.Minute) {
		t.Fatal("another principal renewed the lease")
	}
	if store.RenewScoped("scoped-fence", work.LeaseGeneration, "adapter-a", "wrong", time.Minute) {
		t.Fatal("a wrong opaque capability renewed the lease")
	}
	if !store.ReleaseAdapterLeaseScoped("scoped-fence", work.LeaseGeneration, "adapter-a", work.LeaseCapability) {
		t.Fatal("valid scoped release failed")
	}
	if store.RenewScoped("scoped-fence", work.LeaseGeneration, "adapter-a", work.LeaseCapability, time.Minute) {
		t.Fatal("released lease capability remained usable")
	}
	replacement, err := store.NextScopedAdapterJobForEndpoint("adapter-a", "shared", 9, capA[0].EndpointCapability, time.Minute)
	if err != nil || replacement == nil {
		t.Fatalf("replacement lease failed: %#v %v", replacement, err)
	}
	if replacement.LeaseGeneration == work.LeaseGeneration || replacement.LeaseCapability == work.LeaseCapability {
		t.Fatal("replacement lease reused its generation or capability")
	}
	if store.RenewScoped("scoped-fence", work.LeaseGeneration, "adapter-a", work.LeaseCapability, time.Minute) {
		t.Fatal("stale generation and capability controlled the replacement lease")
	}
}

func TestScopedAdapterHeartbeatKeepsCapabilityStableDuringLongPolls(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterClientStatus{
		State: "waiting", Ready: true, ActiveEndpoints: 1,
		Endpoints: []AdapterEndpointStatus{{ID: 5, Profile: "profile", State: "idle"}},
	}
	first, err := store.RecordScopedAdapterHeartbeat("adapter", heartbeat)
	if err != nil || len(first) != 1 {
		t.Fatalf("first heartbeat failed: %#v %v", first, err)
	}
	key := adapterEndpointKey{principal: "adapter", profile: "profile", endpoint: 5}
	store.mu.Lock()
	record := store.endpointCaps[key]
	shortExpiry := time.Now().Add(30 * time.Second)
	record.expiresAt = shortExpiry
	store.endpointCaps[key] = record
	store.mu.Unlock()
	heartbeat.Endpoints[0].EndpointCapability = first[0].EndpointCapability
	second, err := store.RecordScopedAdapterHeartbeat("adapter", heartbeat)
	if err != nil || len(second) != 1 {
		t.Fatalf("renewing heartbeat failed: %#v %v", second, err)
	}
	if second[0].EndpointCapability != first[0].EndpointCapability || !second[0].ExpiresAt.After(shortExpiry) {
		t.Fatalf("healthy heartbeat rotated or failed to renew capability: first=%#v second=%#v", first[0], second[0])
	}
	if _, err := store.RecordScopedAdapterHeartbeat("adapter", AdapterClientStatus{
		State: "waiting", Ready: true, ActiveEndpoints: 1,
		Endpoints: []AdapterEndpointStatus{{ID: 5, Profile: "profile", State: "idle"}},
	}); err == nil {
		t.Fatal("an active endpoint was allowed to replace its capability without proof")
	}
}

func TestExpiredEndpointCapabilityIsNotResurrectedByHeartbeat(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterClientStatus{
		State: "waiting", Ready: true, ActiveEndpoints: 1,
		Endpoints: []AdapterEndpointStatus{{ID: 4, Profile: "profile", State: "idle"}},
	}
	first, err := store.RecordScopedAdapterHeartbeat("adapter", heartbeat)
	if err != nil || len(first) != 1 {
		t.Fatalf("first heartbeat failed: %#v %v", first, err)
	}
	key := adapterEndpointKey{principal: "adapter", profile: "profile", endpoint: 4}
	store.mu.Lock()
	record := store.endpointCaps[key]
	record.expiresAt = time.Now().Add(-time.Second)
	store.endpointCaps[key] = record
	store.mu.Unlock()
	second, err := store.RecordScopedAdapterHeartbeat("adapter", heartbeat)
	if err != nil || len(second) != 1 {
		t.Fatalf("second heartbeat failed: %#v %v", second, err)
	}
	store.mu.Lock()
	oldValid := store.validEndpointCapabilityLocked("adapter", "profile", 4, first[0].EndpointCapability, time.Now())
	newValid := store.validEndpointCapabilityLocked("adapter", "profile", 4, second[0].EndpointCapability, time.Now())
	store.mu.Unlock()
	if oldValid || !newValid {
		t.Fatalf("expired capability resurrection: old=%v new=%v", oldValid, newValid)
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
