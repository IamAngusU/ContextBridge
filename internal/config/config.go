package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version         int                       `yaml:"version"`
	Server          Server                    `yaml:"server"`
	Storage         Storage                   `yaml:"storage"`
	Routes          map[string]Route          `yaml:"routes"`
	Providers       Providers                 `yaml:"providers"`
	BrowserProfiles map[string]BrowserProfile `yaml:"browser_profiles"`
}

type Server struct {
	Listen string `yaml:"listen"`
	Token  string `yaml:"token"`
}

type Storage struct {
	Directory string `yaml:"directory"`
	Inbox     string `yaml:"inbox"`
}

type Route struct {
	Provider       string   `yaml:"provider" json:"provider"`
	Fallback       []string `yaml:"fallback" json:"fallback"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	BrowserProfile string   `yaml:"browser_profile" json:"browser_profile"`
}

type Providers struct {
	Ollama  OllamaProvider  `yaml:"ollama"`
	Browser BrowserProvider `yaml:"browser"`
}

type OllamaProvider struct {
	URL     string `yaml:"url"`
	Model   string `yaml:"model"`
	Images  bool   `yaml:"images"`
	Timeout int    `yaml:"timeout_seconds"`
}

type BrowserProvider struct {
	LeaseSeconds int `yaml:"lease_seconds"`
}

type BrowserProfile struct {
	Label     string    `yaml:"label" json:"label"`
	MatchURL  string    `yaml:"match_url" json:"match_url"`
	Selectors Selectors `yaml:"selectors" json:"selectors"`
}

type Selectors struct {
	Input     []string `yaml:"input" json:"input"`
	FileInput []string `yaml:"file_input" json:"file_input"`
	Submit    []string `yaml:"submit" json:"submit"`
	Response  []string `yaml:"response" json:"response"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	expanded := expandEnvironment(string(raw))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg, filepath.Dir(path))
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Server.Token == "" || strings.Contains(c.Server.Token, "change-me") || strings.Contains(c.Server.Token, "${") {
		return errors.New("server.token must be a strong secret or environment reference")
	}
	if !strings.HasPrefix(c.Server.Listen, "127.0.0.1:") && !strings.HasPrefix(c.Server.Listen, "localhost:") {
		return errors.New("server.listen must use localhost unless the source is reviewed and TLS is placed in front")
	}
	if _, ok := c.Routes["default"]; !ok {
		return errors.New("routes.default is required")
	}
	for name, route := range c.Routes {
		for _, provider := range append([]string{route.Provider}, route.Fallback...) {
			if provider != "ollama" && provider != "browser" {
				return fmt.Errorf("route %s references unsupported provider %s", name, provider)
			}
		}
		if route.BrowserProfile != "" {
			if _, ok := c.BrowserProfiles[route.BrowserProfile]; !ok {
				return fmt.Errorf("route %s references unknown browser profile %s", name, route.BrowserProfile)
			}
		}
	}
	return nil
}

func (c Config) Route(name string) Route {
	if route, ok := c.Routes[name]; ok {
		return route
	}
	return c.Routes["default"]
}

func Default(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	}
	secret, err := newSecret()
	if err != nil {
		return err
	}
	content := strings.ReplaceAll(defaultYAML, "GENERATED_TOKEN", secret)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0600)
}

func applyDefaults(cfg *Config, base string) {
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:32145"
	}
	if cfg.Storage.Directory == "" {
		cfg.Storage.Directory = filepath.Join(base, "data")
	} else if !filepath.IsAbs(cfg.Storage.Directory) {
		cfg.Storage.Directory = filepath.Join(base, cfg.Storage.Directory)
	}
	if cfg.Storage.Inbox == "" {
		cfg.Storage.Inbox = filepath.Join(base, "inbox")
	} else if !filepath.IsAbs(cfg.Storage.Inbox) {
		cfg.Storage.Inbox = filepath.Join(base, cfg.Storage.Inbox)
	}
	if cfg.Providers.Ollama.URL == "" {
		cfg.Providers.Ollama.URL = "http://127.0.0.1:11434"
	}
	if cfg.Providers.Ollama.Model == "" {
		cfg.Providers.Ollama.Model = "gemma3:4b"
	}
	if cfg.Providers.Ollama.Timeout == 0 {
		cfg.Providers.Ollama.Timeout = 45
	}
	if cfg.Providers.Browser.LeaseSeconds == 0 {
		cfg.Providers.Browser.LeaseSeconds = 90
	}
	for name, route := range cfg.Routes {
		if route.TimeoutSeconds == 0 {
			route.TimeoutSeconds = 180
		}
		cfg.Routes[name] = route
	}
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnvironment(value string) string {
	return envPattern.ReplaceAllStringFunc(value, func(token string) string {
		name := token[2 : len(token)-1]
		if replacement, ok := os.LookupEnv(name); ok {
			return replacement
		}
		return token
	})
}

func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

const defaultYAML = `version: 1

server:
  listen: 127.0.0.1:32145
  token: GENERATED_TOKEN

storage:
  directory: ./data
  inbox: ./inbox

routes:
  default:
    provider: ollama
    fallback: [browser]
    timeout_seconds: 180
    browser_profile: chatgpt
  inkwall:
    provider: ollama
    fallback: [browser]
    timeout_seconds: 180
    browser_profile: chatgpt

providers:
  ollama:
    url: http://127.0.0.1:11434
    model: gemma3:4b
    images: true
    timeout_seconds: 45
  browser:
    lease_seconds: 90

browser_profiles:
  chatgpt:
    label: ChatGPT
    match_url: https://chatgpt.com/*
    selectors:
      input: ['#prompt-textarea', '[contenteditable="true"]']
      file_input: ['input[type="file"]']
      submit: ['button[data-testid="send-button"]', 'button[aria-label*="Send"]']
      response: ['[data-message-author-role="assistant"]']
`
