package main

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestFetchConsoleStatusUsesReadOnlyAuthenticatedRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/status" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected console request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v0.test","queued":2,"completed":3,"active_jobs":1,"adapter":{"connected":true,"active_endpoints":1,"endpoints":[{"id":7,"profile":"profile-one","state":"working","current_model":"model-a"}]},"metrics":{"jobs_total":4,"jobs_failed":1},"runtime":{"hardware":{"gpus":[{"name":"RTX","utilization_percent":12}]},"engines":{"ollama":{"state":"online","models":[{"name":"qwen3:8b","loaded":true}]},"remote":{"state":"online","type":"openai_compatible","remote":true,"models":[{"name":"api-model","available":true}]},"stopped":{"state":"stopped","models":[{"name":"not-ready","loaded":true}]}},"resource_packs":[{"schema_version":1,"id":"example.modelkit","name":"ModelKit","path":"X:\\\\ModelKit"}]}}`))
	}))
	defer server.Close()
	cfg := config.Config{Server: config.Server{Listen: strings.TrimPrefix(server.URL, "http://"), Token: "test-secret"}}
	status, err := fetchConsoleStatus(context.Background(), server.Client(), cfg)
	if err != nil || requests != 1 {
		t.Fatalf("console request failed: %v; requests=%d", err, requests)
	}
	snapshot := toServiceSnapshot(status)
	if snapshot.Version != "v0.test" || snapshot.Queued != 2 || snapshot.ActiveJobs != 1 || !snapshot.AdapterConnected || snapshot.Endpoints[0].CurrentModel != "model-a" || snapshot.Endpoints[0].State != "working" || snapshot.GPU != "RTX" ||
		len(snapshot.LocalProviders) != 1 || snapshot.LocalProviders[0] != "ollama" || len(snapshot.LocalModels) != 1 || snapshot.LocalModels[0].Name != "qwen3:8b" || !snapshot.LocalModels[0].Loaded || len(snapshot.APIProviders) != 1 || snapshot.APIProviders[0] != "remote" || len(snapshot.APIModels) != 1 || snapshot.APIModels[0].Name != "api-model" || len(snapshot.ResourcePacks) != 1 || snapshot.ResourcePacks[0].ID != "example.modelkit" {
		t.Fatalf("console snapshot lost status data: %#v", snapshot)
	}
	engine := status.Runtime.Engines["ollama"]
	engine.Models = nil
	status.Runtime.Engines["ollama"] = engine
	withoutLoadedModel := toServiceSnapshot(status)
	if len(withoutLoadedModel.LocalProviders) != 1 || len(withoutLoadedModel.LocalModels) != 0 {
		t.Fatalf("online Ollama disappeared when no model was loaded: %#v", withoutLoadedModel)
	}
}

func TestServiceSnapshotFeatureIndicatorsReflectEffectiveConfiguration(t *testing.T) {
	enabled := true
	cfg := config.Config{
		Cluster:  config.Cluster{Relay: config.ClusterRelay{Enabled: true}, Worker: config.ClusterWorker{Enabled: false}},
		Portable: config.PortableResources{Enabled: &enabled},
		Engines:  map[string]config.Engine{"ollama": {AutoStart: true}},
	}
	status := consoleStatus{}
	status.Updates.Enabled = true
	status.RAG.Enabled = false
	snapshot := toServiceSnapshot(status, cfg)
	want := map[string]bool{"RLY": true, "WRK": false, "UPD": true, "RAG": false, "PCK": true, "EAS": true}
	if len(snapshot.Features) != len(want) {
		t.Fatalf("feature count = %d; want %d", len(snapshot.Features), len(want))
	}
	for _, feature := range snapshot.Features {
		if enabled, ok := want[feature.Label]; !ok || enabled != feature.Enabled {
			t.Fatalf("feature %#v not in expected state %#v", feature, want)
		}
	}
}

func TestFetchConsoleStatusRejectsWrongToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	cfg := config.Config{Server: config.Server{Listen: strings.TrimPrefix(server.URL, "http://"), Token: "wrong"}}
	_, err := fetchConsoleStatus(context.Background(), server.Client(), cfg)
	if !errors.Is(err, errConsoleUnauthorized) {
		t.Fatalf("wrong token should not retry indefinitely: %v", err)
	}
}

func TestConsoleExitCommandsOnlyCloseTheReadOnlyView(t *testing.T) {
	for _, command := range []string{"exit", "QUIT", " q ", ":q"} {
		if !isConsoleExitCommand(command) {
			t.Fatalf("%q not recognized as an exit command", command)
		}
		select {
		case <-consoleExitRequested(strings.NewReader(command + "\n")):
		case <-time.After(time.Second):
			t.Fatalf("%q did not close the console view", command)
		}
	}
	for _, command := range []string{"", "status", "restart", "exit now"} {
		if isConsoleExitCommand(command) {
			t.Fatalf("unrelated input %q closed the console view", command)
		}
	}
}

func TestConsoleLineEditorPreservesTypedTextAndControlCommands(t *testing.T) {
	commands := make(chan string, 8)
	edits := []string{}
	readConsoleCommands(bufio.NewReader(strings.NewReader("helx\bp\rabc\b\bde\r\f")), commands, func(value string) {
		edits = append(edits, value)
	})
	got := []string{}
	for command := range commands {
		got = append(got, command)
	}
	if strings.Join(got, "|") != "help|ade|clear" {
		t.Fatalf("unexpected commands: %#v", got)
	}
	joined := strings.Join(edits, "|")
	for _, want := range []string{"helx", "hel", "help", "abc", "a", "ade"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("line editor lost %q in %#v", want, edits)
		}
	}
}

func TestConsoleLineEditorPublishesSpaceImmediately(t *testing.T) {
	commands := make(chan string, 2)
	edits := []string{}
	readConsoleCommands(bufio.NewReader(strings.NewReader("send x\r")), commands, func(value string) {
		edits = append(edits, value)
	})
	for range commands {
	}
	found := false
	for _, edit := range edits {
		if edit == "send " {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("space key was not published until a later character: %#v", edits)
	}
}

func TestAdjacentConfigPathPrefersOwnInstallation(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "contextbridge.exe")
	if adjacentConfigPath(executable) != "" {
		t.Fatal("missing adjacent config was selected")
	}
	configPath := filepath.Join(directory, "config.yml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if adjacentConfigPath(executable) != configPath {
		t.Fatal("the binary did not prefer its adjacent config")
	}
	t.Setenv("CONTEXTBRIDGE_CONFIG", configPath)
	if defaultConfigPath() != configPath {
		t.Fatal("explicit environment override lost precedence")
	}
}
