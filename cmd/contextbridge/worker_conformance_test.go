package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestWorkerConformanceProducesContentMinimizingEvidence(t *testing.T) {
	now := time.Now().UTC()
	node := validConformanceNode(t, now)
	server := workerConformanceServer(t, now, []cluster.Node{node})
	defer server.Close()

	report, err := runWorkerConformance(context.Background(), server.URL, "observer-token", "")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Compatible || report.Schema != workerConformanceV1 || len(report.Workers) != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), node.ID) || strings.Contains(string(encoded), node.Name) {
		t.Fatalf("report leaked raw node identity: %s", encoded)
	}
	if !strings.HasPrefix(report.Workers[0].NodeRef, "sha256:") || len(report.Workers[0].Checks) != 11 {
		t.Fatalf("worker evidence is incomplete: %#v", report.Workers[0])
	}
}

func TestWorkerConformanceFailsClosedOnMalformedEvidence(t *testing.T) {
	now := time.Now().UTC()
	node := validConformanceNode(t, now)
	node.PublicKey = "not-a-key"
	node.LastSeen = now.Add(-time.Minute)
	node.ClockOffsetMS = 10 * 60 * 1000
	node.Capabilities.MaxConcurrent = cluster.MaximumWorkerConcurrency + 1
	node.Capabilities.Running = cluster.MaximumWorkerConcurrency + 2
	node.Capabilities.Providers = []string{"ollama", "OLLAMA"}
	node.Capabilities.AutomaticTasks = map[string][]string{"unknown": {"generation"}}
	node.Capabilities.Models = []cluster.ModelCapability{{Name: "bad", Provider: "ollama", Available: false, Loaded: true}}
	node.Capabilities.MemoryFree = node.Capabilities.MemoryTotal + 1
	node.Capabilities.AdapterEndpoints = 1
	node.JobsFailed = node.JobsTotal + 1
	server := workerConformanceServer(t, now, []cluster.Node{node})
	defer server.Close()

	report, err := runWorkerConformance(context.Background(), server.URL, "observer-token", node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Compatible {
		t.Fatal("malformed worker evidence was reported compatible")
	}
	failed := 0
	for _, check := range report.Workers[0].Checks {
		if !check.Passed {
			failed++
		}
	}
	if failed < 8 {
		t.Fatalf("only %d malformed evidence classes failed: %#v", failed, report.Workers[0].Checks)
	}
}

func TestWorkerConformanceSelectorIsUnambiguous(t *testing.T) {
	now := time.Now().UTC()
	first := validConformanceNode(t, now)
	second := first
	second.ID = "second-private-node"
	if _, err := selectConformanceWorkers([]cluster.Node{first, second}, first.Name); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate display names should require exact ID: %v", err)
	}
	second.Connected = false
	selected, err := selectConformanceWorkers([]cluster.Node{first, second}, "")
	if err != nil || len(selected) != 1 || selected[0].ID != first.ID {
		t.Fatalf("default selection should include connected workers only: %#v %v", selected, err)
	}
}

func validConformanceNode(t *testing.T, now time.Time) cluster.Node {
	t.Helper()
	_, publicKey, err := cluster.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return cluster.Node{
		ID: "private-node-id", Name: "Private Workstation", PublicKey: publicKey, Connected: true, State: "online", LastSeen: now.Add(-time.Second),
		ClockOffsetMS: 20, JobsTotal: 10, JobsFailed: 1, CostUSD: .04, CostKnownJobs: 3, CostUnknownJobs: 7,
		Capabilities: cluster.Capabilities{
			ClockTime: now.Add(-time.Second), UTCOffsetSeconds: 3600, OS: "linux", Architecture: "amd64", CPU: "test cpu", CPUCores: 8,
			CPUUtilization: 25, MemoryTotal: 16 << 30, MemoryFree: 12 << 30, AgentVersion: "v0.5.86",
			Providers: []string{"ollama"}, Tasks: []string{"generation"}, Modes: []string{"text"}, Sources: []string{"local"}, Groups: []string{"default"},
			AutomaticTasks: map[string][]string{"ollama": {"generation"}}, MaxConcurrent: 4, Running: 1,
			Models: []cluster.ModelCapability{{Name: "small-model", Provider: "ollama", Tasks: []string{"generation"}, Available: true, Loaded: true, CapabilitiesVerified: true, CapabilitySource: "ollama_show"}},
			GPUs:   []cluster.GPUCapability{{Name: "Test GPU", Backend: "CUDA", MemoryTotal: 8 << 30, MemoryFree: 6 << 30, Utilization: 20, Temperature: 45}},
		},
	}
}

func workerConformanceServer(t *testing.T, now time.Time, nodes []cluster.Node) *httptest.Server {
	t.Helper()
	manifest := cluster.CurrentProtocolManifest(cluster.MaximumJobPayloadBytes)
	overview := cluster.Overview{GeneratedAt: now, NodesOnline: len(nodes), NodesTotal: len(nodes)}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer observer-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/cluster/protocol":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/v1/cluster/overview":
			_ = json.NewEncoder(w).Encode(overview)
		case "/v1/cluster/nodes":
			_ = json.NewEncoder(w).Encode(nodes)
		default:
			http.NotFound(w, request)
		}
	}))
}
