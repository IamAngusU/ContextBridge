package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

type openAIIntegrationInfo struct {
	Kind            string `json:"kind"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	TokenConfigured bool   `json:"token_configured"`
	APIKey          string `json:"api_key,omitempty"`
	ConfigPath      string `json:"config_path"`
}

type liteLLMIntegrationInfo struct {
	Kind             string `json:"kind"`
	ModelName        string `json:"model_name"`
	DownstreamModel  string `json:"downstream_model"`
	BaseURL          string `json:"base_url"`
	TokenConfigured  bool   `json:"token_configured"`
	ConfigPath       string `json:"contextbridge_config_path"`
	OutputConfigPath string `json:"output_config_path,omitempty"`
	OutputEnvPath    string `json:"output_env_path,omitempty"`
}

type mcpIntegrationInfo struct {
	Kind       string                 `json:"kind"`
	ConfigPath string                 `json:"config_path"`
	Config     map[string]interface{} `json:"config"`
}

type openAIIntegrationCheck struct {
	Kind           string `json:"kind"`
	Reachable      bool   `json:"reachable"`
	Authenticated  bool   `json:"authenticated"`
	ModelAvailable bool   `json:"model_available"`
	LiveRequested  bool   `json:"live_requested"`
	LiveSucceeded  bool   `json:"live_succeeded,omitempty"`
	ElapsedMS      int64  `json:"elapsed_ms"`
}

type relayIntegrationInfo struct {
	Kind       string    `json:"kind"`
	Role       string    `json:"role"`
	RelayURL   string    `json:"relay_url"`
	Subject    string    `json:"subject"`
	TokenID    string    `json:"token_id"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	OutputPath string    `json:"output_path"`
}

const (
	maximumIntegrationResponseBytes = 1 << 20
	integrationPreflightTimeout     = 10 * time.Second
	integrationLiveTimeout          = 90 * time.Second
)

func integrateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge integrate openai|litellm|mcp|relay|ui [--config path] [--json]")
	}
	target := strings.ToLower(strings.TrimSpace(args[0]))
	flags := flag.NewFlagSet("integrate "+target, flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "config path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	showToken := flags.Bool("show-token", false, "include the local API token in terminal output")
	writeEnv := flags.String("write-env", "", "write a new mode-0600 OpenAI-compatible .env file")
	writeConfig := flags.String("write-config", "", "write a new secret-free integration configuration file")
	check := flags.Bool("check", false, "verify reachability, authentication, and the configured model without inference")
	live := flags.Bool("live", false, "also send one explicit bounded live inference smoke request")
	subject := flags.String("subject", "", "remote application identity (relay integration)")
	groups := flags.String("groups", "", "comma-separated scheduling groups (relay integration)")
	lifetimeHours := flags.Int("lifetime-hours", 720, "producer credential lifetime in hours; 0 never expires")
	maxQueuedJobs := flags.Int("max-queued-jobs", 0, "producer queued-job limit; 0 uses the relay default")
	maxJobsPerHour := flags.Int("max-jobs-per-hour", 0, "durable producer admission limit; 0 disables it")
	maxPriority := flags.Int("max-priority", 100, "maximum producer-selected job priority, 0 to 100; omission preserves 100")
	providers := flags.String("providers", "", "comma-separated provider allowlist")
	allowedTenants := flags.String("allowed-tenants", "", "comma-separated tenant_id allowlist bound to a relay or read-only UI credential")
	allowedSubjects := flags.String("allowed-subjects", "", "comma-separated owner_subject allowlist bound to a read-only UI credential")
	egress := flags.String("egress", "", "producer egress ceiling: local_only or empty")
	requireE2EE := flags.Bool("require-e2ee", false, "reject every cleartext job submitted with the relay producer credential")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	maxPriorityLimit := explicitIntFlagValue(flags, "max-priority", *maxPriority)
	if target == "openai" && *jsonOutput && strings.TrimSpace(*writeEnv) != "" {
		return errors.New("--json and --write-env are separate output modes")
	}
	if target != "litellm" && strings.TrimSpace(*writeConfig) != "" {
		return errors.New("--write-config is available only for the litellm integration")
	}
	if *showToken && strings.TrimSpace(*writeEnv) != "" {
		return errors.New("--show-token is unnecessary with --write-env and cannot be combined with it")
	}
	if (*check || *live) && (strings.TrimSpace(*writeEnv) != "" || *showToken) {
		return errors.New("--check/--live cannot be combined with secret output modes")
	}
	absoluteConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(absoluteConfig)
	if err != nil {
		return err
	}
	if *requireE2EE && target != "relay" {
		return errors.New("--require-e2ee is available only for relay producer integration")
	}
	if maxPriorityLimit != nil && target != "relay" {
		return errors.New("--max-priority is available only for relay producer integration")
	}
	if maxPriorityLimit != nil && (*maxPriorityLimit < 0 || *maxPriorityLimit > 100) {
		return errors.New("--max-priority must be between 0 and 100")
	}

	switch target {
	case "openai":
		info, err := buildOpenAIIntegration(cfg, absoluteConfig, *showToken)
		if err != nil {
			return err
		}
		if *check || *live {
			if *live {
				fmt.Fprintln(os.Stderr, "Live integration smoke explicitly requested; the configured route may use compute, network egress, or paid API credit.")
			}
			report, err := checkOpenAIIntegration(context.Background(), http.DefaultClient, info, cfg.Server.Token, *live)
			if err != nil {
				return err
			}
			if *jsonOutput {
				return writeIntegrationJSON(report)
			}
			fmt.Println("OpenAI-compatible integration check")
			fmt.Printf("Reachable       %t\n", report.Reachable)
			fmt.Printf("Authenticated   %t\n", report.Authenticated)
			fmt.Printf("Model available %t\n", report.ModelAvailable)
			if report.LiveRequested {
				fmt.Printf("Live smoke      %t\n", report.LiveSucceeded)
			}
			fmt.Printf("Elapsed         %d ms\n", report.ElapsedMS)
			return nil
		}
		if strings.TrimSpace(*writeEnv) != "" {
			path, err := filepath.Abs(*writeEnv)
			if err != nil {
				return err
			}
			if err := writeOpenAIIntegrationEnv(path, info.BaseURL, cfg.Server.Token, info.Model); err != nil {
				return err
			}
			fmt.Printf("Created private integration file %s\n", path)
			fmt.Println("Keep it out of version control and load it only into the application that should use ContextBridge.")
			return nil
		}
		if *jsonOutput {
			return writeIntegrationJSON(info)
		}
		fmt.Println("OpenAI-compatible ContextBridge connection")
		fmt.Printf("Base URL  %s\n", info.BaseURL)
		fmt.Printf("Model     %s\n", info.Model)
		if info.APIKey != "" {
			if err := writeExplicitIntegrationSecret(os.Stdout, "API key   ", info.APIKey); err != nil {
				return err
			}
		} else {
			fmt.Println("API key   configured · hidden")
		}
		fmt.Println()
		fmt.Println("Create a private copy-paste .env file:")
		fmt.Println("  contextbridge integrate openai --write-env .contextbridge.env")
		fmt.Println("Use --show-token only when you intentionally need the key in terminal output.")
		return nil
	case "litellm":
		if err := rejectUnexpectedIntegrationFlags(flags, map[string]bool{
			"config": true, "json": true, "write-config": true, "write-env": true,
		}); err != nil {
			return err
		}
		if *showToken || *check || *live {
			return errors.New("--show-token, --check, and --live are not available for LiteLLM integration; use 'integrate openai --check' to verify the downstream ContextBridge endpoint")
		}
		configTarget := strings.TrimSpace(*writeConfig)
		envTarget := strings.TrimSpace(*writeEnv)
		if (configTarget == "") != (envTarget == "") {
			return errors.New("--write-config and --write-env must be provided together so the LiteLLM config stays secret-free")
		}
		openAIInfo, err := buildOpenAIIntegration(cfg, absoluteConfig, false)
		if err != nil {
			return err
		}
		info, err := buildLiteLLMIntegration(openAIInfo)
		if err != nil {
			return err
		}
		if configTarget != "" {
			configOutput, err := filepath.Abs(configTarget)
			if err != nil {
				return err
			}
			envOutput, err := filepath.Abs(envTarget)
			if err != nil {
				return err
			}
			if err := writeLiteLLMIntegrationFiles(configOutput, envOutput, info, cfg.Server.Token); err != nil {
				return err
			}
			info.OutputConfigPath = configOutput
			info.OutputEnvPath = envOutput
			if *jsonOutput {
				return writeIntegrationJSON(info)
			}
			fmt.Printf("Created secret-free LiteLLM config %s\n", configOutput)
			fmt.Printf("Created private LiteLLM environment file %s\n", envOutput)
			fmt.Println("Load the environment file only into LiteLLM; ContextBridge remains usable directly without LiteLLM.")
			return nil
		}
		if *jsonOutput {
			return writeIntegrationJSON(info)
		}
		fmt.Println("Optional LiteLLM gateway in front of ContextBridge")
		fmt.Printf("LiteLLM model    %s\n", info.ModelName)
		fmt.Printf("CB model         %s\n", info.DownstreamModel)
		fmt.Printf("CB base URL      %s\n", info.BaseURL)
		if info.TokenConfigured {
			fmt.Println("Token            configured · hidden")
		} else {
			fmt.Println("Token            not configured")
		}
		fmt.Println()
		fmt.Println("Create a secret-free LiteLLM config and a separate private environment file:")
		fmt.Println("  contextbridge integrate litellm --write-config ./litellm-contextbridge.yaml --write-env ./.contextbridge-litellm.env")
		fmt.Println("LiteLLM is optional; applications may continue to connect to ContextBridge directly.")
		return nil
	case "mcp":
		if *showToken || strings.TrimSpace(*writeEnv) != "" {
			return errors.New("--show-token and --write-env are available only for the openai integration")
		}
		info, err := buildMCPIntegration(absoluteConfig)
		if err != nil {
			return err
		}
		if !*jsonOutput {
			fmt.Println("Add this MCP server entry to your MCP client:")
		}
		return writeIntegrationJSON(info.Config)
	case "relay":
		if *showToken || *check || *live {
			return errors.New("--show-token, --check, and --live are not available for relay integration")
		}
		if strings.TrimSpace(*subject) == "" {
			return errors.New("--subject is required for a relay application credential")
		}
		if strings.TrimSpace(*allowedSubjects) != "" {
			return errors.New("--allowed-subjects applies only to a read-only UI credential")
		}
		if strings.TrimSpace(*writeEnv) == "" {
			return errors.New("--write-env is required so the producer token never enters terminal output")
		}
		if *lifetimeHours < 0 || *lifetimeHours > 10*365*24 {
			return errors.New("--lifetime-hours must be between 0 and 87600")
		}
		path, err := filepath.Abs(*writeEnv)
		if err != nil {
			return err
		}
		limits := cluster.ProducerLimits{MaxQueuedJobs: *maxQueuedJobs, MaxJobsPerHour: *maxJobsPerHour, MaxPriority: maxPriorityLimit, Providers: splitIntegrationList(*providers), AllowedTenants: splitIntegrationList(*allowedTenants), Egress: strings.TrimSpace(*egress), RequireE2EE: *requireE2EE}
		info, err := createRelayIntegrationBundleGoverned(context.Background(), cfg, path, *subject, splitIntegrationList(*groups), *lifetimeHours, limits)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeIntegrationJSON(info)
		}
		fmt.Printf("Created private producer integration file %s\n", info.OutputPath)
		fmt.Printf("Relay application %s · token %s", info.Subject, info.TokenID)
		if !info.ExpiresAt.IsZero() {
			fmt.Printf(" · expires %s", info.ExpiresAt.Format(time.RFC3339))
		}
		fmt.Println()
		fmt.Println("Transfer the file through a secure channel and load it only into the intended server-side application.")
		return nil
	case "ui":
		if *showToken || *check || *live {
			return errors.New("--show-token, --check, and --live are not available for UI integration")
		}
		if strings.TrimSpace(*subject) == "" {
			return errors.New("--subject is required for a read-only UI credential")
		}
		if strings.TrimSpace(*writeEnv) == "" {
			return errors.New("--write-env is required so the observer token never enters terminal output")
		}
		if *lifetimeHours < 0 || *lifetimeHours > 10*365*24 {
			return errors.New("--lifetime-hours must be between 0 and 87600")
		}
		if strings.TrimSpace(*groups) != "" || *maxQueuedJobs != 0 || *maxJobsPerHour != 0 || maxPriorityLimit != nil || strings.TrimSpace(*providers) != "" || strings.TrimSpace(*egress) != "" || *requireE2EE {
			return errors.New("producer groups, admission limits, providers, egress, and E2EE requirements do not apply to a read-only UI credential")
		}
		path, err := filepath.Abs(*writeEnv)
		if err != nil {
			return err
		}
		observerLimits := cluster.ObserverLimits{AllowedSubjects: splitIntegrationList(*allowedSubjects), AllowedTenants: splitIntegrationList(*allowedTenants)}
		info, err := createObserverIntegrationBundleScoped(context.Background(), cfg, path, *subject, *lifetimeHours, observerLimits)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeIntegrationJSON(info)
		}
		fmt.Printf("Created private read-only UI integration file %s\n", info.OutputPath)
		fmt.Printf("Relay observer %s · token %s", info.Subject, info.TokenID)
		if !info.ExpiresAt.IsZero() {
			fmt.Printf(" · expires %s", info.ExpiresAt.Format(time.RFC3339))
		}
		fmt.Println()
		fmt.Println("Keep the token in a trusted backend, desktop secret store, or private environment; never ship it in browser JavaScript.")
		return nil
	default:
		return fmt.Errorf("unsupported integration %q; use openai, litellm, mcp, relay, or ui", target)
	}
}

// writeExplicitIntegrationSecret is deliberately not a logging operation.
// Its only caller is the explicit --show-token path, which is documented as
// placing the configured secret on stdout for the invoking user.
func writeExplicitIntegrationSecret(writer io.Writer, prefix, secret string) error {
	_, err := io.WriteString(writer, prefix+secret+"\n")
	return err
}

func createRelayIntegrationBundle(ctx context.Context, cfg config.Config, path, subject string, groups []string, lifetimeHours int) (relayIntegrationInfo, error) {
	return createRelayIntegrationBundleGoverned(ctx, cfg, path, subject, groups, lifetimeHours, cluster.ProducerLimits{})
}

func createRelayIntegrationBundleGoverned(ctx context.Context, cfg config.Config, path, subject string, groups []string, lifetimeHours int, limits cluster.ProducerLimits) (relayIntegrationInfo, error) {
	return createScopedRelayIntegrationBundle(ctx, cfg, path, "producer", "contextbridge-relay-producer", "CONTEXTBRIDGE_PRODUCER_TOKEN", subject, groups, lifetimeHours, limits, cluster.ObserverLimits{})
}

func createObserverIntegrationBundle(ctx context.Context, cfg config.Config, path, subject string, lifetimeHours int) (relayIntegrationInfo, error) {
	return createObserverIntegrationBundleScoped(ctx, cfg, path, subject, lifetimeHours, cluster.ObserverLimits{})
}

func createObserverIntegrationBundleScoped(ctx context.Context, cfg config.Config, path, subject string, lifetimeHours int, limits cluster.ObserverLimits) (relayIntegrationInfo, error) {
	return createScopedRelayIntegrationBundle(ctx, cfg, path, "observer", "contextbridge-relay-observer", "CONTEXTBRIDGE_OBSERVER_TOKEN", subject, nil, lifetimeHours, cluster.ProducerLimits{}, limits)
}

func createScopedRelayIntegrationBundle(ctx context.Context, cfg config.Config, path, role, kind, environmentKey, subject string, groups []string, lifetimeHours int, producerLimits cluster.ProducerLimits, observerLimits cluster.ObserverLimits) (relayIntegrationInfo, error) {
	if role != "producer" && role != "observer" {
		return relayIntegrationInfo{}, errors.New("integration credential role must be producer or observer")
	}
	if environmentKey != "CONTEXTBRIDGE_PRODUCER_TOKEN" && environmentKey != "CONTEXTBRIDGE_OBSERVER_TOKEN" {
		return relayIntegrationInfo{}, errors.New("integration credential environment key is not supported")
	}
	// #nosec G703 -- path is the operator-selected absolute --write-env target; O_EXCL prevents replacing existing data.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return relayIntegrationInfo{}, fmt.Errorf("reserve integration file without overwriting existing data: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			// #nosec G703 -- cleanup removes only the exact O_EXCL file created above after an incomplete credential write.
			_ = os.Remove(path)
		}
	}()
	var output struct {
		Token  string              `json:"token"`
		Record cluster.TokenRecord `json:"record"`
	}
	relayURL := clusterBaseURL(cfg)
	if err := clusterPOST(ctx, relayURL+"/v1/cluster/tokens", cfg.Cluster.Relay.AdminToken, map[string]interface{}{
		"role": role, "subject": subject, "groups": groups, "lifetime_hours": lifetimeHours, "producer_limits": producerLimits, "observer_limits": observerLimits,
	}, &output); err != nil {
		return relayIntegrationInfo{}, fmt.Errorf("issue scoped %s credential: %w", role, err)
	}
	for _, value := range []string{relayURL, output.Token} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return relayIntegrationInfo{}, errors.New("relay returned an empty or unsafe integration value; the credential may need operator revocation")
		}
	}
	content := fmt.Sprintf("CONTEXTBRIDGE_RELAY_URL=%s\n%s=%s\n", relayURL, environmentKey, output.Token)
	if _, err := file.WriteString(content); err != nil {
		return relayIntegrationInfo{}, fmt.Errorf("write %s integration file; the issued credential may need operator revocation: %w", role, err)
	}
	if err := file.Sync(); err != nil {
		return relayIntegrationInfo{}, fmt.Errorf("sync %s integration file; the issued credential may need operator revocation: %w", role, err)
	}
	if err := file.Close(); err != nil {
		return relayIntegrationInfo{}, fmt.Errorf("close %s integration file; the issued credential may need operator revocation: %w", role, err)
	}
	remove = false
	return relayIntegrationInfo{Kind: kind, Role: role, RelayURL: relayURL, Subject: output.Record.Subject, TokenID: output.Record.ID, ExpiresAt: output.Record.ExpiresAt, OutputPath: path}, nil
}

func splitIntegrationList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func rejectUnexpectedIntegrationFlags(flags *flag.FlagSet, allowed map[string]bool) error {
	unexpected := make([]string, 0)
	flags.Visit(func(option *flag.Flag) {
		if !allowed[option.Name] {
			unexpected = append(unexpected, "--"+option.Name)
		}
	})
	if len(unexpected) == 0 {
		return nil
	}
	sort.Strings(unexpected)
	return fmt.Errorf("unsupported option(s) for %s: %s", flags.Name(), strings.Join(unexpected, ", "))
}

func checkOpenAIIntegration(ctx context.Context, client *http.Client, info openAIIntegrationInfo, token string, live bool) (openAIIntegrationCheck, error) {
	started := time.Now()
	report := openAIIntegrationCheck{Kind: "openai-compatible-check", LiveRequested: live}
	if client == nil {
		client = http.DefaultClient
	}
	preflightCtx, cancelPreflight := context.WithTimeout(ctx, integrationPreflightTimeout)
	defer cancelPreflight()
	request, err := http.NewRequestWithContext(preflightCtx, http.MethodGet, strings.TrimRight(info.BaseURL, "/")+"/models", nil)
	if err != nil {
		return report, fmt.Errorf("integration endpoint: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return report, fmt.Errorf("local service unreachable: %w", err)
	}
	report.Reachable = true
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := decodeIntegrationResponse(response, &models); err != nil {
		return report, fmt.Errorf("local service authentication/model check: %w", err)
	}
	cancelPreflight()
	report.Authenticated = true
	for _, model := range models.Data {
		if model.ID == info.Model {
			report.ModelAvailable = true
			break
		}
	}
	if !report.ModelAvailable {
		return report, fmt.Errorf("configured model %q is not advertised by the local service", info.Model)
	}
	if live {
		body, err := json.Marshal(map[string]interface{}{
			"model":      info.Model,
			"messages":   []map[string]string{{"role": "user", "content": "Reply exactly with CONTEXTBRIDGE-INTEGRATION-OK and nothing else."}},
			"max_tokens": 64,
		})
		if err != nil {
			return report, err
		}
		liveCtx, cancelLive := context.WithTimeout(ctx, integrationLiveTimeout)
		defer cancelLive()
		request, err := http.NewRequestWithContext(liveCtx, http.MethodPost, strings.TrimRight(info.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return report, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return report, fmt.Errorf("live integration smoke: %w", err)
		}
		var completion struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := decodeIntegrationResponse(response, &completion); err != nil {
			return report, fmt.Errorf("live integration smoke: %w", err)
		}
		report.LiveSucceeded = len(completion.Choices) == 1 && strings.TrimSpace(completion.Choices[0].Message.Content) == "CONTEXTBRIDGE-INTEGRATION-OK"
		if !report.LiveSucceeded {
			return report, errors.New("live integration smoke returned a non-matching bounded response")
		}
	}
	report.ElapsedMS = time.Since(started).Milliseconds()
	return report, nil
}

func decodeIntegrationResponse(response *http.Response, output interface{}) error {
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumIntegrationResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maximumIntegrationResponseBytes {
		return errors.New("response exceeds the integration-check limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	return nil
}

func buildOpenAIIntegration(cfg config.Config, configPath string, showToken bool) (openAIIntegrationInfo, error) {
	routes := make([]string, 0, len(cfg.Routes))
	for name := range cfg.Routes {
		routes = append(routes, name)
	}
	if len(routes) == 0 {
		return openAIIntegrationInfo{}, errors.New("no configured route is available for the OpenAI-compatible model ID")
	}
	sort.Strings(routes)
	route := routes[0]
	if _, ok := cfg.Routes["default"]; ok {
		route = "default"
	}
	info := openAIIntegrationInfo{
		Kind:            "openai-compatible",
		BaseURL:         strings.TrimRight(baseURL(cfg), "/") + "/openai/v1",
		Model:           "contextbridge:" + route,
		TokenConfigured: strings.TrimSpace(cfg.Server.Token) != "",
		ConfigPath:      configPath,
	}
	if showToken {
		info.APIKey = cfg.Server.Token
	}
	return info, nil
}

func buildLiteLLMIntegration(openAI openAIIntegrationInfo) (liteLLMIntegrationInfo, error) {
	for _, value := range []string{openAI.BaseURL, openAI.Model, openAI.ConfigPath} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return liteLLMIntegrationInfo{}, errors.New("ContextBridge integration value is empty or unsafe for LiteLLM configuration")
		}
	}
	return liteLLMIntegrationInfo{
		Kind:            "litellm-optional-gateway",
		ModelName:       "contextbridge",
		DownstreamModel: "openai/" + openAI.Model,
		BaseURL:         openAI.BaseURL,
		TokenConfigured: openAI.TokenConfigured,
		ConfigPath:      openAI.ConfigPath,
	}, nil
}

func renderLiteLLMIntegrationConfig(info liteLLMIntegrationInfo) (string, error) {
	for _, value := range []string{info.ModelName, info.DownstreamModel} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return "", errors.New("LiteLLM integration value is empty or unsafe")
		}
	}
	return fmt.Sprintf("model_list:\n  - model_name: %s\n    litellm_params:\n      model: %s\n      api_base: os.environ/CONTEXTBRIDGE_LITELLM_BASE_URL\n      api_key: os.environ/CONTEXTBRIDGE_LITELLM_API_KEY\n", strconv.Quote(info.ModelName), strconv.Quote(info.DownstreamModel)), nil
}

func writeLiteLLMIntegrationFiles(configPath, envPath string, info liteLLMIntegrationInfo, token string) error {
	if samePath(configPath, envPath) {
		return errors.New("LiteLLM config and private environment paths must be different")
	}
	for _, value := range []string{info.BaseURL, token} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("LiteLLM environment value is empty or unsafe")
		}
	}
	configContent, err := renderLiteLLMIntegrationConfig(info)
	if err != nil {
		return err
	}
	envContent := fmt.Sprintf("CONTEXTBRIDGE_LITELLM_BASE_URL=%s\nCONTEXTBRIDGE_LITELLM_API_KEY=%s\n", info.BaseURL, token)

	// #nosec G703 -- both paths are explicit operator-selected outputs; O_EXCL prevents replacing existing data.
	configFile, err := os.OpenFile(configPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create LiteLLM config without overwriting existing data: %w", err)
	}
	committed := false
	var envFile *os.File
	envCreated := false
	defer func() {
		_ = configFile.Close()
		if envFile != nil {
			_ = envFile.Close()
		}
		if !committed {
			_ = os.Remove(configPath) // #nosec G703 -- exact O_EXCL file created by this invocation.
			if envCreated {
				_ = os.Remove(envPath) // #nosec G703 -- exact O_EXCL file created by this invocation.
			}
		}
	}()
	// #nosec G703 -- both paths are explicit operator-selected outputs; O_EXCL prevents replacing existing data.
	envFile, err = os.OpenFile(envPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create private LiteLLM environment file without overwriting existing data: %w", err)
	}
	envCreated = true
	if _, err := configFile.WriteString(configContent); err != nil {
		return fmt.Errorf("write LiteLLM config: %w", err)
	}
	if err := configFile.Sync(); err != nil {
		return fmt.Errorf("sync LiteLLM config: %w", err)
	}
	if _, err := envFile.WriteString(envContent); err != nil {
		return fmt.Errorf("write private LiteLLM environment file: %w", err)
	}
	if err := envFile.Sync(); err != nil {
		return fmt.Errorf("sync private LiteLLM environment file: %w", err)
	}
	if err := configFile.Close(); err != nil {
		return fmt.Errorf("close LiteLLM config: %w", err)
	}
	if err := envFile.Close(); err != nil {
		return fmt.Errorf("close private LiteLLM environment file: %w", err)
	}
	committed = true
	return nil
}

func buildMCPIntegration(configPath string) (mcpIntegrationInfo, error) {
	executable, err := os.Executable()
	if err != nil {
		return mcpIntegrationInfo{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return mcpIntegrationInfo{}, err
	}
	entry := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"contextbridge": map[string]interface{}{
				"command": executable,
				"args":    []string{"mcp", "serve", "--config", configPath},
			},
		},
	}
	return mcpIntegrationInfo{Kind: "mcp-stdio", ConfigPath: configPath, Config: entry}, nil
}

func writeOpenAIIntegrationEnv(path, baseURL, token, model string) error {
	for _, value := range []string{baseURL, token, model} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("integration value is empty or unsafe for a .env file")
		}
	}
	// #nosec G703 -- path is the operator-selected absolute --write-env target; O_EXCL prevents replacing existing data.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create integration file without overwriting existing data: %w", err)
	}
	content := fmt.Sprintf("OPENAI_BASE_URL=%s\nOPENAI_API_KEY=%s\nOPENAI_MODEL=%s\n", baseURL, token, model)
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		// #nosec G703 -- cleanup removes only the exact O_EXCL file created above after an incomplete write.
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		// #nosec G703 -- cleanup removes only the exact O_EXCL file created above after a failed sync.
		_ = os.Remove(path)
		return fmt.Errorf("sync integration file: %w", err)
	}
	if err := file.Close(); err != nil {
		// #nosec G703 -- cleanup removes only the exact O_EXCL file created above after a failed close.
		_ = os.Remove(path)
		return err
	}
	return nil
}

func writeIntegrationJSON(value interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
