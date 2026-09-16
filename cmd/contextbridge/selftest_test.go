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
	got, err := parseSelftestProviders("GEM, ollama, GPT, local")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{selftestLocal, selftestChatGPT, selftestGemini}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providers = %#v, want %#v", got, want)
	}
	if _, err := parseSelftestProviders("local,unknown"); err == nil {
		t.Fatal("unsupported provider was accepted")
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
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_terminal_failure", Status: cluster.JobFailed, Error: "browser_lease_lost"})
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
		selftestTarget{Kind: selftestChatGPT, NodeID: "node-a"}, selftestJobSpec{Prompt: "fixed"})
	if err == nil || !strings.Contains(err.Error(), "browser_lease_lost") || job.Status != cluster.JobFailed {
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
			MaxConcurrent: 4,
			Providers:     []string{"ollama", "browser"},
			BrowserTabs:   2,
			AutomaticTasks: map[string][]string{
				"ollama":  {"generation"},
				"browser": {"generation"},
			},
			Models: []cluster.ModelCapability{
				{Name: "huge", Provider: "ollama", Tasks: []string{"generation"}, Available: true, Loaded: true, CapabilitiesVerified: true, Size: 20 << 30},
				{Name: "small", Provider: "ollama", Tasks: []string{"generation"}, Available: true, Loaded: true, CapabilitiesVerified: true, Size: 2 << 30},
				{Name: "tiny-cold", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true, Size: 1 << 30},
				{Name: "embed", Provider: "ollama", Tasks: []string{"embedding"}, Available: true, Loaded: true, CapabilitiesVerified: true},
			},
			BrowserSessions: []cluster.BrowserSessionCapability{
				{Profile: "chatgpt", State: "waiting"},
				{Profile: "gemini", State: "waiting"},
			},
		},
	}}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestLocal, selftestChatGPT, selftestGemini}, "")
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
			MaxConcurrent: 1, Running: 1, Providers: []string{"browser"},
			BrowserSessions: []cluster.BrowserSessionCapability{{Profile: "chatgpt", State: "waiting"}},
		},
	}, {
		ID: "node-b", Name: "GeminiOnly", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 2, Providers: []string{"browser"},
			BrowserSessions: []cluster.BrowserSessionCapability{{Profile: "gemini", State: "waiting"}},
		},
	}}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestChatGPT}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "slots are busy") {
		t.Fatalf("unexpected capacity plan: %#v", plan)
	}
	plan = buildSelftestPlanAt(now, nodes[1:], []string{selftestChatGPT}, "")
	if len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "attach an idle chatgpt") {
		t.Fatalf("unexpected profile plan: %#v", plan)
	}
}

func TestBuildSelftestPlanRejectsRateLimitedTabAndEmbeddingOnlyModel(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	nodes := []cluster.Node{{
		ID: "node-a", Name: "Worker", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 2, Providers: []string{"ollama", "browser"},
			Models:          []cluster.ModelCapability{{Name: "embed", Provider: "ollama", Tasks: []string{"embedding"}, Available: true, Loaded: true, CapabilitiesVerified: true}},
			BrowserSessions: []cluster.BrowserSessionCapability{{Profile: "gemini", State: "rate_limited"}},
		},
	}}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestLocal, selftestGemini}, "")
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
			ID: "no-browser-tab", Name: "RelayOnly", Connected: true, LastSeen: now,
			Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"browser"}, BrowserTabs: 0,
				AutomaticTasks:  map[string][]string{"browser": {"generation"}},
				BrowserSessions: []cluster.BrowserSessionCapability{{Profile: "chatgpt", State: "waiting"}},
			},
		},
	}
	plan := buildSelftestPlanAt(now, nodes, []string{selftestLocal, selftestChatGPT}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 2 {
		t.Fatalf("readiness disagreed with provider-scoped scheduler evidence: %#v", plan)
	}
}

func TestBuildSelftestPlanAcceptsExactLocalModelAndReadyBrowserProfile(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	node := cluster.Node{
		ID: "node-a", Name: "Worker", Connected: true, LastSeen: now,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 2, Providers: []string{"ollama", "browser"}, BrowserTabs: 1,
			AutomaticTasks: map[string][]string{"ollama": {"generation"}, "browser": {"generation"}},
			Models:         []cluster.ModelCapability{{Name: "text-model", Provider: "ollama", Tasks: []string{"generation"}}},
			BrowserSessions: []cluster.BrowserSessionCapability{{
				Profile: "gemini", State: "waiting",
			}},
		},
	}
	plan := buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestLocal, selftestGemini}, "text-model")
	if len(plan.Missing) != 0 || len(plan.Targets) != 2 || plan.Targets[0].NodeID != "node-a" || plan.Targets[1].NodeID != "node-a" {
		t.Fatalf("schedulable provider-scoped targets were rejected: %#v", plan)
	}
}

func TestBuildSelftestPlanUsesSchedulerFreshnessBoundary(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	node := cluster.Node{
		ID: "node-a", Name: "Worker", Connected: true,
		Capabilities: cluster.Capabilities{
			MaxConcurrent: 1,
			Providers:     []string{"browser"},
			BrowserTabs:   1,
			AutomaticTasks: map[string][]string{
				"browser": {"generation"},
			},
			BrowserSessions: []cluster.BrowserSessionCapability{{
				Profile: "chatgpt", State: "waiting",
			}},
		},
	}
	node.LastSeen = now.Add(-cluster.NodeFreshnessWindow)
	plan := buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestChatGPT}, "")
	if len(plan.Targets) != 1 || len(plan.Missing) != 0 {
		t.Fatalf("node exactly at scheduler freshness boundary was rejected: %#v", plan)
	}

	node.LastSeen = now.Add(-cluster.NodeFreshnessWindow - time.Nanosecond)
	plan = buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestChatGPT}, "")
	if len(plan.Targets) != 0 || len(plan.Missing) != 1 || !strings.Contains(plan.Missing[0], "stale or offline") {
		t.Fatalf("stale node was reported ready or had an unclear reason: %#v", plan)
	}

	node.LastSeen = now
	node.Connected = false
	plan = buildSelftestPlanAt(now, []cluster.Node{node}, []string{selftestChatGPT}, "")
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
			Providers:     []string{"ollama", "browser"},
			Models:        []cluster.ModelCapability{{Name: "generation", Provider: "ollama", Tasks: []string{"generation"}, Available: true, CapabilitiesVerified: true}},
			BrowserSessions: []cluster.BrowserSessionCapability{
				{Profile: "chatgpt", State: "waiting"},
				{Profile: "gemini", State: "waiting"},
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
			name: "browser profile is busy", kind: selftestChatGPT, want: "busy or rate-limited",
			fresh: cluster.Node{ID: "fresh-chatgpt-busy", Connected: true, LastSeen: now, Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"browser"},
				BrowserSessions: []cluster.BrowserSessionCapability{{Profile: "chatgpt", State: "rate_limited"}},
			}},
		},
		{
			name: "browser profile needs attachment", kind: selftestGemini, want: "attach an idle gemini",
			fresh: cluster.Node{ID: "fresh-browser", Connected: true, LastSeen: now, Capabilities: cluster.Capabilities{
				MaxConcurrent: 1, Providers: []string{"browser"},
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

func TestSelftestBrowserJobAlwaysUsesFreshPerJobConversation(t *testing.T) {
	target := selftestTarget{Kind: selftestChatGPT, NodeID: "node-a", NodeName: "Worker"}
	request, err := buildSelftestRequest(target, selftestJobSpec{Prompt: "fixed"}, "RUN")
	if err != nil {
		t.Fatal(err)
	}
	var job bridge.Job
	if err := json.Unmarshal(request.Payload, &job); err != nil {
		t.Fatal(err)
	}
	if request.Requirements.Provider != "browser" || job.BrowserProfile != "chatgpt" {
		t.Fatalf("wrong browser routing: request=%#v job=%#v", request, job)
	}
	if request.Requirements.BrowserProfile != "chatgpt" {
		t.Fatalf("browser profile is not a hard scheduler requirement: %#v", request.Requirements)
	}
	if !request.Requirements.BrowserFreshChat || !request.Requirements.BrowserEphemeralChat {
		t.Fatalf("self-test fresh per-job policy is not visible to the relay scheduler: %#v", request.Requirements)
	}
	if job.Metadata["contextbridge_new_chat"] != true || job.Metadata["contextbridge_new_chat_per_job"] != true {
		t.Fatalf("self-test could reuse a personal chat: %#v", job.Metadata)
	}
	if request.Requirements.SessionID != "selftest-RUN-chatgpt" || !reflect.DeepEqual(request.Requirements.PreferredNodes, []string{"node-a"}) {
		t.Fatalf("self-test did not keep its isolated session near the checked node: %#v", request.Requirements)
	}
}
