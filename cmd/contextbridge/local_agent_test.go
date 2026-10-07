package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestLocalAgentBoundary(t *testing.T) {
	for _, target := range []string{"https://example.org", "http://localhost:9000", "http://127.0.0.1@evil.test", "http://127.0.0.1:9000/path", "http://127.0.0.1?secret=1"} {
		if (config.AgentAPI{URL: target}).Validate() == nil {
			t.Errorf("accepted %s", target)
		}
	}
	if (config.AgentAPI{URL: "http://127.0.0.1:8770", DefaultPolicy: "offline"}).Validate() != nil {
		t.Fatal("loopback origin rejected")
	}
}

func TestLocalAgentClientDoesNotForwardCredentialsOrFollowRedirect(t *testing.T) {
	received := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received++; w.WriteHeader(200) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("inherited credentials")
		}
		if r.Header.Get("Idempotency-Key") != "same-request-123456" {
			t.Error("retry identity dropped")
		}
		http.Redirect(w, r, other.URL, 307)
	}))
	defer server.Close()
	_, err := (agentClient{api: config.AgentAPI{URL: server.URL}}).call(context.Background(), "POST", "/api/agent/jobs", map[string]string{"text": "PRIVATE"}, "same-request-123456")
	if err == nil || received != 0 {
		t.Fatal("redirect followed or accepted")
	}
}

func TestLocalAgentRoutingDoesNotReadPromptAsFlags(t *testing.T) {
	if agentExplicitClusterRoute([]string{"--prompt", "--provider"}) {
		t.Fatal("task text changed route")
	}
	if !agentExplicitClusterRoute([]string{"--provider=ollama", "--prompt", "x"}) {
		t.Fatal("explicit cluster route lost")
	}
}

func TestLocalAgentFreshExplicitlyDisablesReuse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var packet map[string]any
		if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if packet["reuse_verified_code"] != false || packet["policy"] != "offline" {
			t.Error("fresh flag lost or policy changed")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "0123456789abcdef0123456789abcdef", "state": "running"})
	}))
	defer server.Close()
	if err := localAgentCommand(config.AgentAPI{URL: server.URL}, []string{"--fresh", "--task", "code", "--background", "task"}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAgentForwardsExplicitDaybreakMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var packet map[string]any
		if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
			t.Fatal(err)
		}
		if packet["mode"] != "daybreak-blue" || packet["policy"] != "offline" {
			t.Errorf("mode/policy lost: %#v", packet)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "0123456789abcdef0123456789abcdef", "state": "running"})
	}))
	defer server.Close()
	api := config.AgentAPI{URL: server.URL}
	if err := localAgentCommand(api, []string{"--mode", "daybreak-blue", "--background", "task"}); err != nil {
		t.Fatal(err)
	}
	if agentExplicitClusterRoute([]string{"--mode", "--provider", "task"}) {
		t.Fatal("mode value changed route")
	}
	if err := localAgentCommand(api, []string{"--mode", "unknown", "--background", "task"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if err := localAgentCommand(api, []string{"--mode", "daybreak-blue", "--task", "diagnose", "--background", "task"}); err == nil {
		t.Fatal("daybreak diagnose accepted")
	}
}

func TestLocalAgentWorkspaceSnapshotBoundary(t *testing.T) {
	file := filepath.Join(t.TempDir(), "context.json")
	raw := []byte(`{"schema":"contextbridge.workspace-context.v1","workspace":"demo","cloud_allowed":false,"files":[]}`)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/capabilities" {
			json.NewEncoder(w).Encode(map[string]any{"code": map[string]any{"workspace_context": "explicit snapshots"}})
			return
		}
		calls++
		var packet map[string]any
		if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
			t.Fatal(err)
		}
		ctx, ok := packet["workspace_context"].(map[string]any)
		if !ok || ctx["workspace"] != "demo" || packet["policy"] != "offline" {
			t.Error("snapshot lost")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "0123456789abcdef0123456789abcdef", "state": "running"})
	}))
	defer server.Close()
	api := config.AgentAPI{URL: server.URL}
	for _, args := range [][]string{
		{"--workspace-context", file, "question"},
		{"--task", "code", "--policy", "hybrid", "--workspace-context", file, "question"},
	} {
		if err := localAgentCommand(api, args); err == nil {
			t.Fatal("invalid source submission accepted")
		}
	}
	if calls != 0 {
		t.Fatal("private context submitted before validation")
	}
	if err := localAgentCommand(api, []string{"--task", "code", "--background", "--workspace-context", file, "fix"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("expected one explicit submission")
	}
	if agentExplicitClusterRoute([]string{"--workspace-context", "--provider", "task"}) {
		t.Fatal("file argument changed route")
	}
	if err := os.WriteFile(file, []byte(`{"schema":"contextbridge.workspace-context.v1","cloud_allowed":false,"cloud_allowed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := localAgentCommand(api, []string{"--task", "code", "--policy", "hybrid", "--workspace-context", file, "fix"}); err == nil {
		t.Fatal("duplicate source permission accepted")
	}
	if calls != 1 {
		t.Fatal("ambiguous snapshot submitted")
	}
}
