package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"gopkg.in/yaml.v3"
)

func TestExplicitIntegrationSecretWriterIsOptInAndExact(t *testing.T) {
	var output bytes.Buffer
	if err := writeExplicitIntegrationSecret(&output, "API key   ", "secret-without-redaction"); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "API key   secret-without-redaction\n"; got != want {
		t.Fatalf("explicit secret output = %q, want %q", got, want)
	}
}

type integrationRoundTripper func(*http.Request) (*http.Response, error)

func (function integrationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestOpenAIIntegrationCheckSeparatesPreflightFromExplicitLiveInference(t *testing.T) {
	const token = "private-local-token"
	liveCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/openai/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"contextbridge:default"}]}`))
		case "/openai/v1/chat/completions":
			liveCalls++
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"CONTEXTBRIDGE-INTEGRATION-OK"}}]}`))
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	info := openAIIntegrationInfo{BaseURL: server.URL + "/openai/v1", Model: "contextbridge:default"}

	report, err := checkOpenAIIntegration(context.Background(), server.Client(), info, token, false)
	if err != nil || !report.Reachable || !report.Authenticated || !report.ModelAvailable || report.LiveRequested || liveCalls != 0 {
		t.Fatalf("non-executing check = %#v, live calls=%d, err=%v", report, liveCalls, err)
	}
	report, err = checkOpenAIIntegration(context.Background(), server.Client(), info, token, true)
	if err != nil || !report.LiveRequested || !report.LiveSucceeded || liveCalls != 1 {
		t.Fatalf("explicit live check = %#v, live calls=%d, err=%v", report, liveCalls, err)
	}
}

func TestOpenAIIntegrationCheckFailsClosedOnAuthenticationAndModelMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer expected" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"contextbridge:other"}]}`))
	}))
	t.Cleanup(server.Close)
	info := openAIIntegrationInfo{BaseURL: server.URL, Model: "contextbridge:default"}
	if _, err := checkOpenAIIntegration(context.Background(), server.Client(), info, "wrong", false); err == nil || !strings.Contains(err.Error(), "authentication/model") {
		t.Fatalf("bad token did not fail at the authentication boundary: %v", err)
	}
	if _, err := checkOpenAIIntegration(context.Background(), server.Client(), info, "expected", false); err == nil || !strings.Contains(err.Error(), "not advertised") {
		t.Fatalf("missing model did not fail closed: %v", err)
	}
}

func TestOpenAIIntegrationCheckAppliesDeadlinesToPreflightAndLive(t *testing.T) {
	seen := 0
	client := &http.Client{Transport: integrationRoundTripper(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 {
			t.Fatalf("request %s has no live deadline", request.URL.Path)
		}
		seen++
		body := `{"data":[{"id":"contextbridge:default"}]}`
		if strings.HasSuffix(request.URL.Path, "/chat/completions") {
			body = `{"choices":[{"message":{"content":"CONTEXTBRIDGE-INTEGRATION-OK"}}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	info := openAIIntegrationInfo{BaseURL: "http://127.0.0.1:32145/openai/v1", Model: "contextbridge:default"}
	report, err := checkOpenAIIntegration(context.Background(), client, info, "token", true)
	if err != nil || !report.LiveSucceeded || seen != 2 {
		t.Fatalf("bounded integration check failed: report=%#v seen=%d err=%v", report, seen, err)
	}
}

func TestOpenAIIntegrationCheckHonorsCallerDeadlineDuringHeaderAndBodyStalls(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "before headers",
			handler: func(_ http.ResponseWriter, request *http.Request) {
				<-request.Context().Done()
			},
		},
		{
			name: "after partial body",
			handler: func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[`)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-request.Context().Done()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			started := time.Now()
			_, err := checkOpenAIIntegration(ctx, server.Client(), openAIIntegrationInfo{
				BaseURL: server.URL, Model: "contextbridge:default",
			}, "token", false)
			if err == nil || time.Since(started) > 2*time.Second {
				t.Fatalf("stalled integration check was not cancelled promptly: elapsed=%s err=%v", time.Since(started), err)
			}
		})
	}
}

func TestRelayIntegrationJSONWritesSecretOnlyToPrivateFile(t *testing.T) {
	const secret = "cb_scoped_secret_never_stdout"
	var issued struct {
		ProducerLimits cluster.ProducerLimits `json:"producer_limits"`
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&issued); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"token": secret, "record": map[string]interface{}{"id": "tok_json", "role": "producer", "subject": "json-app", "created_at": "2026-09-26T00:00:00Z"}})
	}))
	defer relay.Close()
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = relay.URL
	cfg.Cluster.Relay.AdminToken = "admin-token"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(t.TempDir(), "producer.env")
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = writeEnd
	commandErr := integrateCommand([]string{"relay", "--config", configPath, "--subject", "json-app", "--allowed-tenants", "tenant-a,tenant-b", "--max-priority", "0", "--write-env", envPath, "--json"})
	_ = writeEnd.Close()
	os.Stdout = previousStdout
	stdout, _ := io.ReadAll(readEnd)
	_ = readEnd.Close()
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if len(issued.ProducerLimits.AllowedTenants) != 2 || issued.ProducerLimits.AllowedTenants[0] != "tenant-a" || issued.ProducerLimits.AllowedTenants[1] != "tenant-b" {
		t.Fatalf("relay integration lost --allowed-tenants: %#v", issued.ProducerLimits)
	}
	if issued.ProducerLimits.MaxPriority == nil || *issued.ProducerLimits.MaxPriority != 0 {
		t.Fatalf("relay integration lost explicit zero priority ceiling: %#v", issued.ProducerLimits)
	}
	var report relayIntegrationInfo
	if err := json.Unmarshal(stdout, &report); err != nil || report.TokenID != "tok_json" || strings.Contains(string(stdout), secret) {
		t.Fatalf("relay JSON metadata is invalid or leaked secret: %q report=%#v err=%v", stdout, report, err)
	}
	private, err := os.ReadFile(envPath)
	if err != nil || !strings.Contains(string(private), secret) {
		t.Fatalf("private output did not contain issued credential: %q err=%v", private, err)
	}
}

func TestRelayIntegrationCreatesScopedPrivateBundleWithoutTerminalSecret(t *testing.T) {
	const admin = "admin-token"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/v1/cluster/tokens" || request.Header.Get("Authorization") != "Bearer "+admin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"token":"cb_scoped_producer_secret","record":{"id":"tok_test","role":"producer","subject":"website-a","created_at":"2026-09-24T00:00:00Z","expires_at":"2026-10-24T00:00:00Z","revoked":false}}`))
	}))
	t.Cleanup(server.Close)
	cfg := config.Config{Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: server.URL, AdminToken: admin}}}
	path := filepath.Join(t.TempDir(), "producer.env")
	info, err := createRelayIntegrationBundle(context.Background(), cfg, path, "website-a", []string{"private"}, 720)
	if err != nil {
		t.Fatal(err)
	}
	if info.Subject != "website-a" || info.TokenID != "tok_test" || info.RelayURL != server.URL || info.OutputPath != path {
		t.Fatalf("unexpected redacted relay integration metadata: %#v", info)
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cb_scoped_producer_secret") {
		t.Fatal("redacted relay metadata exposed the producer token")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "CONTEXTBRIDGE_RELAY_URL="+server.URL) || !strings.Contains(content, "CONTEXTBRIDGE_PRODUCER_TOKEN=cb_scoped_producer_secret") {
		t.Fatalf("producer bundle is incomplete: %q", content)
	}
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := createRelayIntegrationBundle(context.Background(), cfg, path, "website-b", nil, 720); err == nil {
		t.Fatal("relay integration overwrote an existing credential file")
	}
	if requests != 1 {
		t.Fatalf("existing output path still caused credential issuance: %d requests", requests)
	}
}

func TestUIIntegrationCreatesReadOnlyObserverBundle(t *testing.T) {
	const admin = "admin-token"
	var issued struct {
		Role           string                 `json:"role"`
		Subject        string                 `json:"subject"`
		ProducerLimits cluster.ProducerLimits `json:"producer_limits"`
		ObserverLimits cluster.ObserverLimits `json:"observer_limits"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/cluster/tokens" || request.Header.Get("Authorization") != "Bearer "+admin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&issued); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"token":"cb_scoped_observer_secret","record":{"id":"tok_ui","role":"observer","subject":"custom-dashboard","created_at":"2026-09-26T00:00:00Z","expires_at":"2026-10-26T00:00:00Z","revoked":false}}`))
	}))
	t.Cleanup(server.Close)
	cfg := config.Config{Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: server.URL, AdminToken: admin}}}
	path := filepath.Join(t.TempDir(), "ui.env")
	limits := cluster.ObserverLimits{AllowedSubjects: []string{"website-api"}, AllowedTenants: []string{"production"}}
	info, err := createObserverIntegrationBundleScoped(context.Background(), cfg, path, "custom-dashboard", 720, limits)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Role != "observer" || issued.Subject != "custom-dashboard" || issued.ProducerLimits.MaxQueuedJobs != 0 || issued.ProducerLimits.MaxJobsPerHour != 0 || issued.ProducerLimits.MaxPriority != nil || len(issued.ProducerLimits.Providers) != 0 || len(issued.ProducerLimits.AllowedTenants) != 0 || issued.ProducerLimits.Egress != "" {
		t.Fatalf("UI integration did not request a plain observer identity: %#v", issued)
	}
	if len(issued.ObserverLimits.AllowedSubjects) != 1 || issued.ObserverLimits.AllowedSubjects[0] != "website-api" || len(issued.ObserverLimits.AllowedTenants) != 1 || issued.ObserverLimits.AllowedTenants[0] != "production" {
		t.Fatalf("UI integration did not send observer scope: %#v", issued.ObserverLimits)
	}
	if info.Kind != "contextbridge-relay-observer" || info.Role != "observer" || info.TokenID != "tok_ui" {
		t.Fatalf("unexpected UI integration metadata: %#v", info)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "CONTEXTBRIDGE_RELAY_URL="+server.URL) || !strings.Contains(content, "CONTEXTBRIDGE_OBSERVER_TOKEN=cb_scoped_observer_secret") {
		t.Fatalf("observer bundle is incomplete: %q", content)
	}
	if strings.Contains(content, "CONTEXTBRIDGE_PRODUCER_TOKEN") {
		t.Fatalf("read-only UI bundle was labelled as a producer credential: %q", content)
	}
}

func TestRelayIntegrationIssuesDurableProducerGovernance(t *testing.T) {
	const admin = "admin-token"
	var issued struct {
		ProducerLimits cluster.ProducerLimits `json:"producer_limits"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+admin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&issued); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"token":"cb_governed_secret","record":{"id":"tok_governed","role":"producer","subject":"bounded-app","created_at":"2026-09-24T00:00:00Z","revoked":false}}`))
	}))
	t.Cleanup(server.Close)
	cfg := config.Config{Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: server.URL, AdminToken: admin}}}
	path := filepath.Join(t.TempDir(), "bounded.env")
	maxPriority := 80
	want := cluster.ProducerLimits{MaxQueuedJobs: 3, MaxJobsPerHour: 25, MaxPriority: &maxPriority, Providers: []string{"ollama"}, AllowedTenants: []string{"tenant-a"}, Egress: "local_only", RequireE2EE: true}
	if _, err := createRelayIntegrationBundleGoverned(context.Background(), cfg, path, "bounded-app", nil, 24, want); err != nil {
		t.Fatal(err)
	}
	if issued.ProducerLimits.MaxQueuedJobs != want.MaxQueuedJobs || issued.ProducerLimits.MaxJobsPerHour != want.MaxJobsPerHour || issued.ProducerLimits.MaxPriority == nil || *issued.ProducerLimits.MaxPriority != maxPriority || issued.ProducerLimits.Egress != want.Egress || len(issued.ProducerLimits.Providers) != 1 || issued.ProducerLimits.Providers[0] != "ollama" || len(issued.ProducerLimits.AllowedTenants) != 1 || issued.ProducerLimits.AllowedTenants[0] != "tenant-a" || !issued.ProducerLimits.RequireE2EE {
		t.Fatalf("governance was not sent to the relay: %#v", issued.ProducerLimits)
	}
}

func TestOpenAIIntegrationIsRedactedAndWritesPrivateEnvWithoutOverwrite(t *testing.T) {
	cfg := config.Config{
		Server: config.Server{Listen: "127.0.0.1:32145", Token: "private-local-token"},
		Routes: map[string]config.Route{"zeta": {Provider: "ollama"}, "default": {Provider: "ollama"}},
	}
	info, err := buildOpenAIIntegration(cfg, filepath.Join(t.TempDir(), "config.yml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.BaseURL != "http://127.0.0.1:32145/openai/v1" || info.Model != "contextbridge:default" || !info.TokenConfigured || info.APIKey != "" {
		t.Fatalf("unexpected redacted integration: %#v", info)
	}
	shown, err := buildOpenAIIntegration(cfg, info.ConfigPath, true)
	if err != nil || shown.APIKey != cfg.Server.Token {
		t.Fatalf("explicit token output failed: %#v %v", shown, err)
	}

	path := filepath.Join(t.TempDir(), ".contextbridge.env")
	if err := writeOpenAIIntegrationEnv(path, info.BaseURL, cfg.Server.Token, info.Model); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, expected := range []string{"OPENAI_BASE_URL=" + info.BaseURL, "OPENAI_API_KEY=" + cfg.Server.Token, "OPENAI_MODEL=" + info.Model} {
		if !strings.Contains(content, expected) {
			t.Fatalf("integration file is missing %q: %s", expected, content)
		}
	}
	if err := writeOpenAIIntegrationEnv(path, info.BaseURL, "replacement", info.Model); err == nil {
		t.Fatal("integration writer overwrote an existing secret file")
	}
	after, _ := os.ReadFile(path)
	if string(after) != content {
		t.Fatal("failed overwrite attempt changed the existing integration file")
	}
}

func TestOpenAIIntegrationRejectsUnsafeEnvValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeOpenAIIntegrationEnv(path, "http://127.0.0.1:32145/openai/v1", "token\nINJECTED=yes", "contextbridge:default"); err == nil {
		t.Fatal("newline-bearing token was written to an env file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unsafe integration created a file: %v", err)
	}
}

func TestLiteLLMIntegrationWritesSecretFreeConfigAndPrivateEnv(t *testing.T) {
	openAI := openAIIntegrationInfo{
		Kind:            "openai-compatible",
		BaseURL:         "http://127.0.0.1:32145/openai/v1",
		Model:           "contextbridge:default",
		TokenConfigured: true,
		ConfigPath:      filepath.Join(t.TempDir(), "config.yml"),
	}
	info, err := buildLiteLLMIntegration(openAI)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModelName != "contextbridge" || info.DownstreamModel != "openai/contextbridge:default" || info.BaseURL != openAI.BaseURL {
		t.Fatalf("unexpected LiteLLM integration: %#v", info)
	}

	directory := t.TempDir()
	configPath := filepath.Join(directory, "litellm-contextbridge.yaml")
	envPath := filepath.Join(directory, ".contextbridge-litellm.env")
	const token = "private-litellm-token"
	if err := writeLiteLLMIntegrationFiles(configPath, envPath, info, token); err != nil {
		t.Fatal(err)
	}
	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configContent := string(configRaw)
	for _, expected := range []string{
		`model_name: "contextbridge"`,
		`model: "openai/contextbridge:default"`,
		"api_base: os.environ/CONTEXTBRIDGE_LITELLM_BASE_URL",
		"api_key: os.environ/CONTEXTBRIDGE_LITELLM_API_KEY",
	} {
		if !strings.Contains(configContent, expected) {
			t.Fatalf("LiteLLM config is missing %q:\n%s", expected, configContent)
		}
	}
	if strings.Contains(configContent, token) || strings.Contains(configContent, openAI.BaseURL) {
		t.Fatal("secret-free LiteLLM config contains private environment data")
	}
	var parsed struct {
		ModelList []struct {
			ModelName string `yaml:"model_name"`
			Params    struct {
				Model   string `yaml:"model"`
				BaseURL string `yaml:"api_base"`
				APIKey  string `yaml:"api_key"`
			} `yaml:"litellm_params"`
		} `yaml:"model_list"`
	}
	if err := yaml.Unmarshal(configRaw, &parsed); err != nil {
		t.Fatalf("generated LiteLLM YAML is invalid: %v", err)
	}
	if len(parsed.ModelList) != 1 || parsed.ModelList[0].ModelName != "contextbridge" || parsed.ModelList[0].Params.Model != "openai/contextbridge:default" || parsed.ModelList[0].Params.BaseURL != "os.environ/CONTEXTBRIDGE_LITELLM_BASE_URL" || parsed.ModelList[0].Params.APIKey != "os.environ/CONTEXTBRIDGE_LITELLM_API_KEY" {
		t.Fatalf("generated LiteLLM YAML has the wrong structure: %#v", parsed)
	}
	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	envContent := string(envRaw)
	for _, expected := range []string{
		"CONTEXTBRIDGE_LITELLM_BASE_URL=" + openAI.BaseURL,
		"CONTEXTBRIDGE_LITELLM_API_KEY=" + token,
	} {
		if !strings.Contains(envContent, expected) {
			t.Fatalf("private LiteLLM environment file is missing %q: %s", expected, envContent)
		}
	}
}

func TestLiteLLMIntegrationNeverOverwritesOrRemovesExistingFiles(t *testing.T) {
	info := liteLLMIntegrationInfo{
		ModelName:       "contextbridge",
		DownstreamModel: "openai/contextbridge:default",
		BaseURL:         "http://127.0.0.1:32145/openai/v1",
	}
	directory := t.TempDir()
	configPath := filepath.Join(directory, "litellm.yaml")
	envPath := filepath.Join(directory, "litellm.env")
	if err := os.WriteFile(envPath, []byte("KEEP=ME\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeLiteLLMIntegrationFiles(configPath, envPath, info, "secret"); err == nil {
		t.Fatal("LiteLLM writer overwrote an existing environment file")
	}
	if raw, err := os.ReadFile(envPath); err != nil || string(raw) != "KEEP=ME\n" {
		t.Fatalf("failed write changed or removed an existing environment file: %q %v", raw, err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("failed two-file write left a partial config: %v", err)
	}

	if err := os.WriteFile(configPath, []byte("keep: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeLiteLLMIntegrationFiles(configPath, filepath.Join(directory, "new.env"), info, "secret"); err == nil {
		t.Fatal("LiteLLM writer overwrote an existing config")
	}
	if raw, err := os.ReadFile(configPath); err != nil || string(raw) != "keep: true\n" {
		t.Fatalf("failed write changed an existing config: %q %v", raw, err)
	}
}

func TestLiteLLMIntegrationRejectsUnsafeOrAliasedOutputs(t *testing.T) {
	info := liteLLMIntegrationInfo{
		ModelName:       "contextbridge",
		DownstreamModel: "openai/contextbridge:default",
		BaseURL:         "http://127.0.0.1:32145/openai/v1",
	}
	path := filepath.Join(t.TempDir(), "same-file")
	if err := writeLiteLLMIntegrationFiles(path, path, info, "secret"); err == nil {
		t.Fatal("LiteLLM writer accepted the same path for public config and private environment")
	}
	if err := writeLiteLLMIntegrationFiles(path+".yaml", path+".env", info, "secret\nINJECTED=yes"); err == nil {
		t.Fatal("LiteLLM writer accepted a newline-bearing token")
	}
	if _, err := os.Stat(path + ".yaml"); !os.IsNotExist(err) {
		t.Fatalf("unsafe values created a config file: %v", err)
	}
}

func TestLiteLLMIntegrationCommandCreatesFilesWithoutPrintingToken(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(directory, "litellm.yaml")
	envPath := filepath.Join(directory, ".litellm.env")
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = writeEnd
	commandErr := integrateCommand([]string{"litellm", "--config", configPath, "--write-config", yamlPath, "--write-env", envPath, "--json"})
	_ = writeEnd.Close()
	os.Stdout = previousStdout
	stdout, _ := io.ReadAll(readEnd)
	_ = readEnd.Close()
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if strings.Contains(string(stdout), cfg.Server.Token) {
		t.Fatal("LiteLLM integration metadata printed the local credential")
	}
	var report liteLLMIntegrationInfo
	if err := json.Unmarshal(stdout, &report); err != nil {
		t.Fatalf("LiteLLM integration did not emit valid JSON metadata: %q: %v", stdout, err)
	}
	if report.OutputConfigPath != yamlPath || report.OutputEnvPath != envPath || report.ModelName != "contextbridge" {
		t.Fatalf("unexpected LiteLLM command metadata: %#v", report)
	}
	private, err := os.ReadFile(envPath)
	if err != nil || !strings.Contains(string(private), cfg.Server.Token) {
		t.Fatalf("private LiteLLM environment did not contain the credential: %q %v", private, err)
	}
	public, err := os.ReadFile(yamlPath)
	if err != nil || strings.Contains(string(public), cfg.Server.Token) {
		t.Fatalf("secret-free LiteLLM config is missing or leaked the credential: %q %v", public, err)
	}
}

func TestLiteLLMIntegrationCommandRequiresBothOutputPaths(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	err := integrateCommand([]string{"litellm", "--config", configPath, "--write-config", filepath.Join(t.TempDir(), "litellm.yaml")})
	if err == nil || !strings.Contains(err.Error(), "must be provided together") {
		t.Fatalf("one-file LiteLLM command was not rejected clearly: %v", err)
	}
}

func TestIntegrationPriorityCeilingIsRelayOnlyAndBounded(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"wrong target", []string{"openai", "--config", configPath, "--max-priority", "20"}, "only for relay"},
		{"below range", []string{"relay", "--config", configPath, "--max-priority", "-1"}, "between 0 and 100"},
		{"above range", []string{"relay", "--config", configPath, "--max-priority", "101"}, "between 0 and 100"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := integrateCommand(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected priority option result: %v", err)
			}
		})
	}
}

func TestLiteLLMIntegrationCommandRejectsOptionsItCannotEnforce(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	for _, option := range [][]string{
		{"--subject", "mistaken-scoped-client"},
		{"--providers", "ollama"},
		{"--show-token"},
	} {
		args := append([]string{"litellm", "--config", configPath}, option...)
		err := integrateCommand(args)
		if err == nil || !strings.Contains(err.Error(), "unsupported option") {
			t.Fatalf("LiteLLM integration silently accepted %v: %v", option, err)
		}
	}
}

func TestMCPIntegrationUsesExactExecutableAndConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yml")
	info, err := buildMCPIntegration(configPath)
	if err != nil {
		t.Fatal(err)
	}
	servers, ok := info.Config["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatalf("MCP server map is missing: %#v", info.Config)
	}
	entry, ok := servers["contextbridge"].(map[string]interface{})
	if !ok {
		t.Fatalf("MCP command is missing: %#v", servers)
	}
	command, ok := entry["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		t.Fatalf("MCP command is missing: %#v", entry)
	}
	args, ok := entry["args"].([]string)
	if !ok || len(args) != 4 || args[0] != "mcp" || args[1] != "serve" || args[2] != "--config" || args[3] != configPath {
		t.Fatalf("MCP arguments are wrong: %#v", entry["args"])
	}
}
