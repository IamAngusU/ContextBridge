package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

type mcpIntegrationInfo struct {
	Kind       string                 `json:"kind"`
	ConfigPath string                 `json:"config_path"`
	Config     map[string]interface{} `json:"config"`
}

func integrateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge integrate openai|mcp [--config path] [--json]")
	}
	target := strings.ToLower(strings.TrimSpace(args[0]))
	flags := flag.NewFlagSet("integrate "+target, flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "config path")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	showToken := flags.Bool("show-token", false, "include the local API token in terminal output")
	writeEnv := flags.String("write-env", "", "write a new mode-0600 OpenAI-compatible .env file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *jsonOutput && strings.TrimSpace(*writeEnv) != "" {
		return errors.New("--json and --write-env are separate output modes")
	}
	if *showToken && strings.TrimSpace(*writeEnv) != "" {
		return errors.New("--show-token is unnecessary with --write-env and cannot be combined with it")
	}
	absoluteConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(absoluteConfig)
	if err != nil {
		return err
	}

	switch target {
	case "openai":
		info, err := buildOpenAIIntegration(cfg, absoluteConfig, *showToken)
		if err != nil {
			return err
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
			fmt.Printf("API key   %s\n", info.APIKey)
		} else {
			fmt.Println("API key   configured · hidden")
		}
		fmt.Println()
		fmt.Println("Create a private copy-paste .env file:")
		fmt.Println("  contextbridge integrate openai --write-env .contextbridge.env")
		fmt.Println("Use --show-token only when you intentionally need the key in terminal output.")
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
	default:
		return fmt.Errorf("unsupported integration %q; use openai or mcp", target)
	}
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create integration file without overwriting existing data: %w", err)
	}
	content := fmt.Sprintf("OPENAI_BASE_URL=%s\nOPENAI_API_KEY=%s\nOPENAI_MODEL=%s\n", baseURL, token, model)
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("sync integration file: %w", err)
	}
	if err := file.Close(); err != nil {
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
