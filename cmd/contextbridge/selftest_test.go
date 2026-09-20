package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const (
	selftestAdapterA = "studio"
	selftestAdapterB = "reviewer"
)

func TestSelftestTimingSummaryShowsMeasuredScopes(t *testing.T) {
	job := cluster.Job{Usage: cluster.Usage{QueueMS: 125, ComputeMS: 4321}}
	got := selftestTimingSummary(job, 5*time.Second)
	for _, want := range []string{"wall 5.0s", "queue 0.1s", "compute 4.3s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("timing summary %q is missing %q", got, want)
		}
	}
	if got := selftestTimingSummary(cluster.Job{}, 50*time.Millisecond); got != "wall 0.1s" {
		t.Fatalf("zero usage should not invent provider metrics: %q", got)
	}
}

func TestParseSelftestProvidersCanonicalizesAliases(t *testing.T) {
	got, err := parseSelftestProviders("reviewer, ollama, adapter:studio, local")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{selftestLocal, selftestAdapterB, selftestAdapterA}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providers = %#v, want %#v", got, want)
	}
	if _, err := parseSelftestProviders("local,bad profile"); err == nil {
		t.Fatal("unsafe adapter profile was accepted")
	}
}

func TestSelectSelftestImageProfileIsExplicitOrUnambiguous(t *testing.T) {
	providers := []string{selftestLocal, selftestAdapterA, selftestAdapterB}
	if _, err := selectSelftestImageProfile(providers, "", true); err == nil {
		t.Fatal("ambiguous image adapter was accepted")
	}
	if got, err := selectSelftestImageProfile(providers, "adapter:studio", true); err != nil || got != selftestAdapterA {
		t.Fatalf("explicit image adapter = %q, %v", got, err)
	}
	if _, err := selectSelftestImageProfile(providers, "missing", true); err == nil {
		t.Fatal("unrequested image adapter was accepted")
	}
}

func TestCancelSelftestJobUsesScopedDelete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete || req.URL.Path != "/v1/cluster/jobs/job_123" {
			t.Fatalf("unexpected cancellation request: %s %s", req.Method, req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer producer-token" {
			t.Fatalf("missing scoped token: %q", req.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"cancelled"}`))
	}))
	defer server.Close()
	if err := cancelSelftestJob(server.URL, "producer-token", "job_123"); err != nil {
		t.Fatal(err)
	}
}

func TestRunSelftestJobDoesNotCancelTerminalFailure(t *testing.T) {
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_terminal_failure", Status: cluster.JobQueued})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_terminal_failure", Status: cluster.JobFailed, Error: "adapter_lease_lost"})
		case http.MethodDelete:
			deletes++
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_terminal_failure", Status: cluster.JobCancelled})
		default:
			t.Fatalf("unexpected self-test request: %s %s", req.Method, req.URL.Path)
		}
	}))
	defer server.Close()
	cfg := config.Config{Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: server.URL}}}
	_, job, err := runSelftestJob(context.Background(), cfg, "producer-token",
		selftestTarget{Kind: selftestAdapterA, NodeID: "node-a"}, selftestJobSpec{Prompt: "fixed"})
	if err == nil || !strings.Contains(err.Error(), "adapter_lease_lost") || job.Status != cluster.JobFailed {
		t.Fatalf("terminal worker failure was not surfaced: job=%#v err=%v", job, err)
	}
	if deletes != 0 {
		t.Fatalf("terminal self-test failure triggered %d cancellation request(s)", deletes)
	}
}

func TestBuildSelftestPlanSelectsReadyTargetsAndSmallestLoadedModel(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	nodes := []cluster.Node{{
		ID: "node-a", Name: "Workstation", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent:    4,
			Providers:        []string{"ollama", "adapter"},
			AdapterEndpoints: 2,
			AutomaticTasks: map[string][]string{
				"ollama":  {"generation"},
				"adapter": {"generation"},
			},
			Models: []cluster.ModelCapability{
				{Name: "huge", Provider: "ollama", Tasks: []string{"generation"}, Available: true, Loaded: true, CapabilitiesVerified: true, Size: 20 << 30},
				{Name: "small", Provider: "ollama", Tasks: []string{"generation"}, Available: true, Loaded: true, CapabilitiesVerified: true, Size: 2 << 30},
				{Name: "tiny-cold", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true, Size: 1 << 30},
				{Name: "embed", Provider: "ollama", Tasks: []string{"embedding"}, Available: true, Loaded: true, CapabilitiesVerified: true},
			},
			AdapterSessions: []cluster.AdapterSessionCapability{
				{Profile: "studio", State: "waiting"},
				{Profile: "reviewer", State: "waiting"},
			},
		},
	}}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestLocal, selftestAdapterA, selftestAdapterB}, "")
	if len(plan.Missing) != 0 || len(plan.Targets) != 3 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if plan.Targets[0].Model != "small" || !plan.Targets[0].ModelLoaded {
		t.Fatalf("local target = %#v", plan.Targets[0])
	}
	if plan.Targets[0].Running != 0 || plan.Targets[0].Capacity != 4 {
		t.Fatalf("local capacity metrics = %#v", plan.Targets[0])
	}
}

func TestBuildSelftestPlanRequiresVerifiedAutomaticOllamaEvidence(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	node := cluster.Node{ID: "node-a", Name: "Legacy", Connected: true, LastSeen: now, Capabilities: cluster.Capabilities{
		MaxConcurrent: 1, Providers: []string{"ollama"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}},
		Models: []cluster.ModelCapability{{
			Name: "legacy", Provider: "ollama", Tasks: []string{"generation"}, Available: true,
			CapabilitySource: "name_inference",
		}},
	}}
	plan := buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestLocal}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "no generation model") {
		t.Fatalf("automatic self-test trusted unverified model evidence: %#v", plan)
	}
	plan = buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestLocal}, "legacy")
	if len(plan.Targets) != 1 || len(plan.Missing) != 0 {
		t.Fatalf("explicit available legacy model was not honored: %#v", plan)
	}
}

func TestBuildSelftestPlanWaitsForExactProfileAndCapacity(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	nodes := []cluster.Node{{
		ID: "node-a", Name: "Busy", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 1, Running: 1, Providers: []string{"adapter"},
			AdapterSessions: []cluster.AdapterSessionCapability{{Profile: "studio", State: "waiting"}},
		},
	}, {
		ID: "node-b", Name: "Profile TwoOnly", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 2, Providers: []string{"adapter"},
			AdapterSessions: []cluster.AdapterSessionCapability{{Profile: "reviewer", State: "waiting"}},
		},
	}}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestAdapterA}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "slots are busy") {
		t.Fatalf("unexpected capacity plan: %#v", plan)
	}
	plan = buildSelftestPlanAt(now, nodes[1:], []string{selftestAdapterA}, "")
	if len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "make an idle studio") {
		t.Fatalf("unexpected profile plan: %#v", plan)
	}
}

func TestBuildSelftestPlanRejectsRateLimitedEndpointAndEmbeddingOnlyModel(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	nodes := []cluster.Node{{
		ID: "node-a", Name: "Worker", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 2, Providers: []string{"ollama", "adapter"},
			Models:          []cluster.ModelCapability{{Name: "embed", Provider: "ollama", Tasks: []string{"embedding"}, Available: true, Loaded: true, CapabilitiesVerified: true}},
			AdapterSessions: []cluster.AdapterSessionCapability{{Profile: "reviewer", State: "rate_limited"}},
		},
	}}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestLocal, selftestAdapterB}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 2 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if !strings.Contains(plan.Missing[0], "no generation model") || !strings.Contains(plan.Missing[1], "rate-limited") {
		t.Fatalf("unclear wait reasons: %#v", plan.Missing)
	}
}

func TestBuildSelftestPlanUsesProviderScopedSchedulerEvidence(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	nodes := []cluster.Node{
		{
			ID: "unscoped-local", Name: "Legacy", Connected: true, LastSeen: now,
			Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"ollama"},
				Models: []cluster.ModelCapability{{Name: "ambiguous", Tasks: []string{"generation"}}},
			},
		},
		{
			ID: "no-adapter-endpoint", Name: "RelayOnly", Connected: true, LastSeen: now,
			Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"adapter"}, AdapterEndpoints: 0,
				AutomaticTasks:  map[string][]string{"adapter": {"generation"}},
				AdapterSessions: []cluster.AdapterSessionCapability{{Profile: "studio", State: "waiting"}},
			},
		},
	}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestLocal, selftestAdapterA}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 2 {
		t.Fatalf("readiness disagreed with provider-scoped scheduler evidence: %#v", plan)
	}
}

func TestBuildSelftestPlanAcceptsExactLocalModelAndReadyAdapterProfile(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	node := cluster.Node{
		ID: "node-a", Name: "Worker", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 2, Providers: []string{"ollama", "adapter"}, AdapterEndpoints: 1,
			AutomaticTasks: map[string][]string{"ollama": {"generation"}, "adapter": {"generation"}},
			Models:         []cluster.ModelCapability{{Name: "text-model", Provider: "ollama", Tasks: []string{"generation"}}},
			AdapterSessions: []cluster.AdapterSessionCapability{{
				Profile: "reviewer", State: "waiting",
			}},
		},
	}
	plan := buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestLocal, selftestAdapterB}, "text-model")
	if len(plan.Missing) != 0 || len(plan.Targets) != 2 || plan.Targets[0].NodeID != "node-a" || plan.Targets[1].NodeID != "node-a" {
		t.Fatalf("schedulable provider-scoped targets were rejected: %#v", plan)
	}
}

func TestBuildSelftestPlanUsesSchedulerFreshnessBoundary(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	node := cluster.Node{
		ID: "node-a", Name: "Worker", Connected: true,
		Capabilities: cluster.Capabilities{
			MaxConcurrent:    1,
			Providers:        []string{"adapter"},
			AdapterEndpoints: 1,
			AutomaticTasks: map[string][]string{
				"adapter": {"generation"},
			},
			AdapterSessions: []cluster.AdapterSessionCapability{{
				Profile: "studio", State: "waiting",
			}},
		},
	}
	node.LastSeen = now.Add(-cluster.NodeFreshnessWindow)
	plan := buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestAdapterA}, "")
	if len(plan.Targets) != 1 || len(plan.Missing) != 0 {
		t.Fatalf("node exactly at scheduler freshness boundary was rejected: %#v", plan)
	}

	node.LastSeen = now.Add(-cluster.NodeFreshnessWindow - time.Nanosecond)
	plan = buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestAdapterA}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "stale or offline") {
		t.Fatalf("stale node was reported ready or had an unclear reason: %#v", plan)
	}

	node.LastSeen = now
	node.Connected = false
	plan = buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestAdapterA}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "stale or offline") {
		t.Fatalf("disconnected node was reported ready or had an unclear reason: %#v", plan)
	}
}

func TestBuildSelftestPlanPrefersFreshBlockingReasonOverStaleSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	stale := cluster.Node{
		ID: "stale", Name: "Stale", Connected: true, LastSeen: now.Add(-cluster.NodeFreshnessWindow - time.Nanosecond),
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 2,
			Providers:     []string{"ollama", "adapter"},
			Models:        []cluster.ModelCapability{{Name: "generation", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}},
			AdapterSessions: []cluster.AdapterSessionCapability{
				{Profile: "studio", State: "waiting"},
				{Profile: "reviewer", State: "waiting"},
			},
		},
	}
	tests := []struct {
		name  string
		kind  string
		fresh cluster.Node
		want  string
	}{
		{
			name: "compatible local capacity is busy", kind: selftestLocal, want: "slots are busy",
			fresh: cluster.Node{ID: "fresh-local-busy", Connected: true, LastSeen: now, Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Running: 1, Providers: []string{"ollama"},
				Models: []cluster.ModelCapability{{Name: "generation", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}},
			}},
		},
		{
			name: "local provider lacks generation model", kind: selftestLocal, want: "no generation model",
			fresh: cluster.Node{ID: "fresh-local-embed", Connected: true, LastSeen: now, Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"ollama"},
				Models: []cluster.ModelCapability{{Name: "embed", Provider: "ollama", Tasks: []string{"embedding"}, Available: true, CapabilitiesVerified: true}},
			}},
		},
		{
			name: "adapter profile is busy", kind: selftestAdapterA, want: "busy or rate-limited",
			fresh: cluster.Node{ID: "fresh-profile-one-busy", Connected: true, LastSeen: now, Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"adapter"},
				AdapterSessions: []cluster.AdapterSessionCapability{{Profile: "studio", State: "rate_limited"}},
			}},
		},
		{
			name: "adapter profile needs attachment", kind: selftestAdapterB, want: "make an idle reviewer",
			fresh: cluster.Node{ID: "fresh-adapter", Connected: true, LastSeen: now, Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"adapter"},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := buildSelftestPlanAt(now, []cluster.Node{stale, test.fresh}, []string{test.kind}, "")
			if len(plan.Targets) != 0 || len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], test.want) {
				t.Fatalf("fresh blocking reason was hidden by stale snapshot: %#v", plan)
			}
		})
	}
}

func TestSelftestAdapterJobAlwaysUsesFreshEphemeralSession(t *testing.T) {
	target := selftestTarget{Kind: selftestAdapterA, NodeID: "node-a", NodeName: "Worker"}
	request, err := buildSelftestRequest(target, selftestJobSpec{Prompt: "fixed"}, "RUN")
	if err != nil {
		t.Fatal(err)
	}
	var job bridge.Job
	if err := json.Unmarshal(request.Payload, &job); err != nil {
		t.Fatal(err)
	}
	if request.Requirements.Provider != "adapter" || job.AdapterProfile != selftestAdapterA {
		t.Fatalf("wrong adapter routing: request=%#v job=%#v", request, job)
	}
	if request.Requirements.AdapterProfile != selftestAdapterA {
		t.Fatalf("adapter profile is not a hard scheduler requirement: %#v", request.Requirements)
	}
	if !request.Requirements.AdapterFreshSession || !request.Requirements.AdapterEphemeralSession {
		t.Fatalf("self-test fresh per-job policy is not visible to the relay scheduler: %#v", request.Requirements)
	}
	if job.Metadata["contextbridge_new_session"] != true || job.Metadata["contextbridge_new_session_per_job"] != true {
		t.Fatalf("self-test could reuse a personal chat: %#v", job.Metadata)
	}
	if request.Requirements.SessionID != "selftest-RUN-"+selftestAdapterA || !reflect.DeepEqual(request.Requirements.PreferredNodes, []string{"node-a"}) {
		t.Fatalf("self-test did not keep its isolated session near the checked node: %#v", request.Requirements)
	}
}
