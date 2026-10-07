package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestLocalToolsStrictArgumentsAndTransport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected credentials")
		}
		if r.Method == "POST" {
			var value map[string]any
			if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
				t.Error(err)
			}
			if value["name"] != "network.listeners" || r.URL.Path != "/api/agent/tools/call" {
				t.Error("wrong dispatch")
			}
		} else if r.URL.Path != "/api/agent/tools" {
			t.Error("wrong catalog path")
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(file, []byte("server:\n  token: unit-test-local-token-not-a-secret\nroutes:\n  default:\n    provider: ollama\nagent_api:\n  url: "+server.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list"}, {"--args", `{"port":8770}`, "call", "network.listeners"}, {"--set", "port=8770", "call", "network.listeners"}} {
		if err := localToolsCommand(append([]string{"--config", file}, args...)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"port":1,"port":2}`, `null`, `[]`, `{}`} {
		args := []string{"--config", file, "--args", raw, "call"}
		if raw != `{}` {
			args = append(args, "network.listeners")
		}
		if err := localToolsCommand(args); err == nil {
			t.Fatal("bad request accepted")
		}
	}
	for _, args := range [][]string{{"--set", "port=1", "--set", "Port=2", "call", "network.listeners"}, {"--args", `{"port":1}`, "--set", "port=2", "call", "network.listeners"}} {
		if err := localToolsCommand(append([]string{"--config", file}, args...)); err == nil {
			t.Fatal("conflicting fields accepted")
		}
	}
	if calls != 3 {
		t.Fatal("invalid input reached service")
	}
}

func TestLocalDiagnoseAndRulesAreExplicit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var value map[string]any
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			t.Error(err)
		}
		if value["task"] == "code" && value["rules"].([]any)[0] != "engineering-defaults" {
			t.Error("rules lost")
		}
		if value["task"] == "diagnose" && value["diagnostic_workspace"] != "demo" {
			t.Error("scope lost")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "0123456789abcdef0123456789abcdef", "state": "running"})
	}))
	defer server.Close()
	api := config.AgentAPI{URL: server.URL}
	for _, args := range [][]string{
		{"--task", "diagnose", "--policy", "offline", "--diagnostic-workspace", "demo", "--background", "inspect"},
		{"--task", "code", "--rules", "engineering-defaults", "--background", "page"},
	} {
		if err := localAgentCommand(api, args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"--task", "diagnose", "--policy", "hybrid", "inspect"},
		{"--rules", "off", "solve"},
		{"--diagnostic-workspace", "demo", "solve"},
	} {
		if err := localAgentCommand(api, args); err == nil {
			t.Fatal("invalid task boundary accepted")
		}
	}
	if calls != 2 {
		t.Fatal("invalid request reached service")
	}
	if agentExplicitClusterRoute([]string{"--rules", "--provider", "task"}) {
		t.Fatal("rules value changed route")
	}
}
