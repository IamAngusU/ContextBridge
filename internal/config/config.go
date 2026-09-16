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

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/updater"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Version         int                       `yaml:"version"`
	Server          Server                    `yaml:"server"`
	Storage         Storage                   `yaml:"storage"`
	Runtime         Runtime                   `yaml:"runtime"`
	Terminal        Terminal                  `yaml:"terminal"`
	Updates         updater.Settings          `yaml:"updates" json:"updates"`
	Routes          map[string]Route          `yaml:"routes"`
	Providers       Providers                 `yaml:"providers"`
	Engines         map[string]Engine         `yaml:"engines"`
	Models          map[string]Model          `yaml:"models"`
	RAG             RAG                       `yaml:"rag"`
	Tunnel          Tunnel                    `yaml:"tunnel"`
	BrowserProfiles map[string]BrowserProfile `yaml:"browser_profiles"`
	Cluster         Cluster                   `yaml:"cluster"`
}

type Server struct {
	Listen string `yaml:"listen"`
	Token  string `yaml:"token"`
}

type Storage struct {
	Directory string `yaml:"directory"`
	Inbox     string `yaml:"inbox"`
	Models    string `yaml:"models"`
}

type Runtime struct {
	HardwareRefreshSeconds int `yaml:"hardware_refresh_seconds"`
}

type Terminal struct {
	Style string `yaml:"style"`
}

type Route struct {
	Provider       string   `yaml:"provider" json:"provider"`
	Fallback       []string `yaml:"fallback" json:"fallback"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	BrowserProfile string   `yaml:"browser_profile" json:"browser_profile"`
	Task           string   `yaml:"task,omitempty" json:"task,omitempty"`
	Model          string   `yaml:"model,omitempty" json:"model,omitempty"`
}

type Engine struct {
	Type           string   `yaml:"type" json:"type"`
	URL            string   `yaml:"url,omitempty" json:"url,omitempty"`
	Model          string   `yaml:"model,omitempty" json:"model,omitempty"`
	Executable     string   `yaml:"executable,omitempty" json:"executable,omitempty"`
	Listen         string   `yaml:"listen,omitempty" json:"listen,omitempty"`
	AutoStart      bool     `yaml:"auto_start,omitempty" json:"auto_start,omitempty"`
	GPU            string   `yaml:"gpu,omitempty" json:"gpu,omitempty"`
	Mode           string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Pooling        string   `yaml:"pooling,omitempty" json:"pooling,omitempty"`
	TimeoutSeconds int      `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	Args           []string `yaml:"args,omitempty" json:"args,omitempty"`
}

type Model struct {
	Repository    string `yaml:"repository" json:"repository"`
	File          string `yaml:"file" json:"file"`
	ProjectorFile string `yaml:"projector_file,omitempty" json:"projector_file,omitempty"`
	SHA256        string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
	Kind          string `yaml:"kind,omitempty" json:"kind,omitempty"`
	QueryPrefix   string `yaml:"query_prefix,omitempty" json:"query_prefix,omitempty"`
	PassagePrefix string `yaml:"passage_prefix,omitempty" json:"passage_prefix,omitempty"`
	Dimensions    int    `yaml:"dimensions,omitempty" json:"dimensions,omitempty"`
}

type Tunnel struct {
	Mode       string `yaml:"mode,omitempty" json:"mode,omitempty"`
	Target     string `yaml:"target,omitempty" json:"target,omitempty"`
	LocalPort  int    `yaml:"local_port,omitempty" json:"local_port,omitempty"`
	RemotePort int    `yaml:"remote_port,omitempty" json:"remote_port,omitempty"`
}

type RAG struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	Backend        string `yaml:"backend" json:"backend"`
	Directory      string `yaml:"directory" json:"directory"`
	EmbeddingRoute string `yaml:"embedding_route" json:"embedding_route"`
	MaxDocuments   int    `yaml:"max_documents" json:"max_documents"`
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

type Cluster struct {
	Relay       ClusterRelay                `yaml:"relay" json:"relay"`
	Worker      ClusterWorker               `yaml:"worker" json:"worker"`
	ClientToken string                      `yaml:"client_token,omitempty" json:"-"`
	Policies    ClusterPolicies             `yaml:"policies" json:"policies"`
	Pricing     cluster.Pricing             `yaml:"pricing" json:"pricing"`
	Pipelines   map[string]cluster.Pipeline `yaml:"pipelines" json:"pipelines"`
}

type ClusterRelay struct {
	Enabled                 bool     `yaml:"enabled" json:"enabled"`
	Listen                  string   `yaml:"listen" json:"listen"`
	PublicURL               string   `yaml:"public_url" json:"public_url"`
	Database                string   `yaml:"database" json:"database"`
	AdminToken              string   `yaml:"admin_token" json:"-"`
	AllowedOrigins          []string `yaml:"allowed_origins" json:"allowed_origins"`
	MaxQueue                int      `yaml:"max_queue" json:"max_queue"`
	MaxJobBytes             int64    `yaml:"max_job_bytes" json:"max_job_bytes"`
	PairingTTLSeconds       int      `yaml:"pairing_ttl_seconds" json:"pairing_ttl_seconds"`
	RetentionDays           int      `yaml:"retention_days" json:"retention_days"`
	MaxTerminalJobs         int      `yaml:"max_terminal_jobs" json:"max_terminal_jobs"`
	MaxEvents               int      `yaml:"max_events" json:"max_events"`
	MaxTerminalPipelineRuns int      `yaml:"max_terminal_pipeline_runs" json:"max_terminal_pipeline_runs"`
	MaxSessionPlacements    int      `yaml:"max_session_placements" json:"max_session_placements"`
	RetentionSweepSeconds   int      `yaml:"retention_sweep_seconds" json:"retention_sweep_seconds"`
}

type ClusterWorker struct {
	Enabled          bool     `yaml:"enabled" json:"enabled"`
	RelayURL         string   `yaml:"relay_url" json:"relay_url"`
	IdentityFile     string   `yaml:"identity_file" json:"identity_file"`
	NodeName         string   `yaml:"node_name" json:"node_name"`
	Groups           []string `yaml:"groups" json:"groups"`
	Tags             []string `yaml:"tags" json:"tags"`
	MaxConcurrent    int      `yaml:"max_concurrent" json:"max_concurrent"`
	AllowedTasks     []string `yaml:"allowed_tasks,omitempty" json:"allowed_tasks,omitempty"`
	AllowedProviders []string `yaml:"allowed_providers,omitempty" json:"allowed_providers,omitempty"`
	AllowedModels    []string `yaml:"allowed_models,omitempty" json:"allowed_models,omitempty"`
	LocalURL         string   `yaml:"local_url" json:"local_url"`
	LocalToken       string   `yaml:"local_token" json:"-"`
	HeartbeatSeconds int      `yaml:"heartbeat_seconds" json:"heartbeat_seconds"`
}

type ClusterPolicies struct {
	AllowedTasks  []string `yaml:"allowed_tasks" json:"allowed_tasks"`
	MaxAttempts   int      `yaml:"max_attempts" json:"max_attempts"`
	MaxJobRuntime int      `yaml:"max_job_runtime_seconds" json:"max_job_runtime_seconds"`
	MaxSteps      int      `yaml:"max_pipeline_steps" json:"max_pipeline_steps"`
	MaxRuntime    int      `yaml:"max_pipeline_runtime_seconds" json:"max_pipeline_runtime_seconds"`
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

func Save(path string, cfg Config) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Terminal.Style != "" && c.Terminal.Style != "classic" && c.Terminal.Style != "panel" {
		return errors.New("terminal.style must be classic or panel")
	}
	if c.Cluster.Worker.MaxConcurrent > cluster.MaximumWorkerConcurrency {
		return fmt.Errorf("cluster.worker.max_concurrent must not exceed %d", cluster.MaximumWorkerConcurrency)
	}
	if c.Server.Token == "" || strings.Contains(c.Server.Token, "change-me") || strings.Contains(c.Server.Token, "${") {
		return errors.New("server.token must be a strong secret or environment reference")
	}
	if len(c.Server.Token) < 24 {
		return errors.New("server.token must contain at least 24 characters")
	}
	if !strings.HasPrefix(c.Server.Listen, "127.0.0.1:") && !strings.HasPrefix(c.Server.Listen, "localhost:") {
		return errors.New("server.listen must use localhost unless the source is reviewed and TLS is placed in front")
	}
	if err := c.Updates.Validate(); err != nil {
		return err
	}
	if _, ok := c.Routes["default"]; !ok {
		return errors.New("routes.default is required")
	}
	for name, route := range c.Routes {
		if !safeNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("invalid route name %s", name)
		}
		for _, provider := range append([]string{route.Provider}, route.Fallback...) {
			if route.Task == "embedding" && provider == "browser" {
				return fmt.Errorf("route %s cannot use a browser for native embeddings", name)
			}
			if provider != "ollama" && provider != "browser" {
				if _, ok := c.Engines[provider]; ok {
					continue
				}
				return fmt.Errorf("route %s references unsupported provider %s", name, provider)
			}
		}
		if route.Task != "" && route.Task != "moderation" && route.Task != "generation" && route.Task != "extraction" && route.Task != "embedding" && route.Task != "rag_ingest" && route.Task != "rag_query" {
			return fmt.Errorf("route %s has unsupported task %s", name, route.Task)
		}
		if route.Model != "" {
			if engine, ok := c.Engines[route.Provider]; ok && engine.Type == "llama_cpp" {
				if _, modelOK := c.Models[route.Model]; !modelOK {
					return fmt.Errorf("route %s references unknown model %s", name, route.Model)
				}
			}
		}
		if route.BrowserProfile != "" {
			if _, ok := c.BrowserProfiles[route.BrowserProfile]; !ok {
				return fmt.Errorf("route %s references unknown browser profile %s", name, route.BrowserProfile)
			}
		}
	}
	for name, engine := range c.Engines {
		if !safeNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("invalid engine name %s", name)
		}
		if engine.Type != "ollama" && engine.Type != "llama_cpp" && engine.Type != "browser" {
			return fmt.Errorf("engine %s has unsupported type %s", name, engine.Type)
		}
		if engine.GPU != "" && engine.GPU != "prefer" && engine.GPU != "require" && engine.GPU != "off" {
			return fmt.Errorf("engine %s gpu must be prefer, require, or off", name)
		}
		if engine.Model != "" {
			if _, ok := c.Models[engine.Model]; !ok && engine.Type == "llama_cpp" {
				return fmt.Errorf("engine %s references unknown model %s", name, engine.Model)
			}
		}
		if engine.Listen != "" && !strings.HasPrefix(engine.Listen, "127.0.0.1:") && !strings.HasPrefix(engine.Listen, "localhost:") {
			return fmt.Errorf("engine %s must listen on localhost", name)
		}
		for _, argument := range engine.Args {
			flag := strings.ToLower(strings.SplitN(strings.TrimSpace(argument), "=", 2)[0])
			if reservedRuntimeFlags[flag] {
				return fmt.Errorf("engine %s cannot override reserved runtime flag %s", name, flag)
			}
		}
	}
	for name, model := range c.Models {
		if !safeNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("invalid model name %s", name)
		}
		if strings.TrimSpace(model.Repository) == "" || strings.TrimSpace(model.File) == "" {
			return fmt.Errorf("model %s requires repository and file", name)
		}
		if !safeArtifactPattern.MatchString(model.File) || filepath.Base(model.File) != model.File || (model.ProjectorFile != "" && (!safeArtifactPattern.MatchString(model.ProjectorFile) || filepath.Base(model.ProjectorFile) != model.ProjectorFile)) {
			return fmt.Errorf("model %s files must be safe filenames without directories", name)
		}
		parts := strings.Split(model.Repository, "/")
		if len(parts) != 2 || !safeRepositoryPartPattern.MatchString(parts[0]) || !safeRepositoryPartPattern.MatchString(parts[1]) || strings.Contains(model.Repository, "..") {
			return fmt.Errorf("model %s repository must use a safe owner/name", name)
		}
		if model.SHA256 != "" && !sha256Pattern.MatchString(strings.TrimPrefix(model.SHA256, "sha256:")) {
			return fmt.Errorf("model %s sha256 must contain 64 hexadecimal characters", name)
		}
	}
	if c.RAG.Enabled {
		if c.RAG.Backend != "local" {
			return fmt.Errorf("unsupported RAG backend %s", c.RAG.Backend)
		}
		if _, ok := c.Routes[c.RAG.EmbeddingRoute]; !ok {
			return fmt.Errorf("RAG embedding route %s does not exist", c.RAG.EmbeddingRoute)
		}
	}
	if c.Cluster.Relay.Enabled {
		if len(c.Cluster.Relay.AdminToken) < 32 || strings.Contains(c.Cluster.Relay.AdminToken, "${") {
			return errors.New("cluster.relay.admin_token must contain at least 32 resolved characters")
		}
		if c.Cluster.Relay.MaxJobBytes > cluster.MaximumJobPayloadBytes {
			return fmt.Errorf("cluster.relay.max_job_bytes must not exceed %d MiB", cluster.MaximumJobPayloadBytes>>20)
		}
		if !strings.HasPrefix(c.Cluster.Relay.Listen, "127.0.0.1:") && !strings.HasPrefix(c.Cluster.Relay.Listen, "localhost:") {
			return errors.New("cluster.relay.listen must use localhost; publish it through a TLS reverse proxy")
		}
	}
	if c.Cluster.Relay.RetentionDays < 1 || c.Cluster.Relay.RetentionDays > cluster.MaximumRetentionDays {
		return fmt.Errorf("cluster.relay.retention_days must be between 1 and %d", cluster.MaximumRetentionDays)
	}
	if c.Cluster.Relay.MaxTerminalJobs < 1 || c.Cluster.Relay.MaxTerminalJobs > cluster.MaximumRetainedTerminalJobs {
		return fmt.Errorf("cluster.relay.max_terminal_jobs must be between 1 and %d", cluster.MaximumRetainedTerminalJobs)
	}
	if c.Cluster.Relay.MaxEvents < 1 || c.Cluster.Relay.MaxEvents > cluster.MaximumRetainedEvents {
		return fmt.Errorf("cluster.relay.max_events must be between 1 and %d", cluster.MaximumRetainedEvents)
	}
	if c.Cluster.Relay.MaxTerminalPipelineRuns < 1 || c.Cluster.Relay.MaxTerminalPipelineRuns > cluster.MaximumRetainedTerminalPipelineRuns {
		return fmt.Errorf("cluster.relay.max_terminal_pipeline_runs must be between 1 and %d", cluster.MaximumRetainedTerminalPipelineRuns)
	}
	if c.Cluster.Relay.MaxSessionPlacements < 1 || c.Cluster.Relay.MaxSessionPlacements > cluster.MaximumRetainedSessionPlacements {
		return fmt.Errorf("cluster.relay.max_session_placements must be between 1 and %d", cluster.MaximumRetainedSessionPlacements)
	}
	if c.Cluster.Relay.RetentionSweepSeconds < cluster.MinimumRetentionSweepSeconds || c.Cluster.Relay.RetentionSweepSeconds > cluster.MaximumRetentionSweepSeconds {
		return fmt.Errorf("cluster.relay.retention_sweep_seconds must be between %d and %d", cluster.MinimumRetentionSweepSeconds, cluster.MaximumRetentionSweepSeconds)
	}
	if c.Cluster.Worker.Enabled && !strings.HasPrefix(c.Cluster.Worker.RelayURL, "https://") && !strings.HasPrefix(c.Cluster.Worker.RelayURL, "http://127.0.0.1:") && !strings.HasPrefix(c.Cluster.Worker.RelayURL, "http://localhost:") {
		return errors.New("cluster.worker.relay_url must use HTTPS or localhost")
	}
	for name, pipeline := range c.Cluster.Pipelines {
		if !safeNamePattern.MatchString(name) || len(pipeline.Steps) == 0 || len(pipeline.Steps) > c.Cluster.Policies.MaxSteps {
			return fmt.Errorf("pipeline %s must have between 1 and %d steps", name, c.Cluster.Policies.MaxSteps)
		}
		seen := map[string]bool{}
		for _, step := range pipeline.Steps {
			if !safeNamePattern.MatchString(step.Name) || seen[step.Name] {
				return fmt.Errorf("pipeline %s has an invalid or duplicate step name", name)
			}
			seen[step.Name] = true
			if step.Retries < 0 || step.Retries > c.Cluster.Policies.MaxAttempts {
				return fmt.Errorf("pipeline %s step %s has invalid retries", name, step.Name)
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

func (c Config) Engine(name string) (Engine, bool) {
	if engine, ok := c.Engines[name]; ok {
		return engine, true
	}
	if name == "ollama" {
		url := c.Providers.Ollama.URL
		if (url == "" || url == "http://127.0.0.1:11434" || url == "http://localhost:11434") && strings.TrimSpace(os.Getenv("OLLAMA_HOST")) != "" {
			url = strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
			if !strings.Contains(url, "://") {
				url = "http://" + url
			}
		}
		return Engine{Type: "ollama", URL: url, Model: c.Providers.Ollama.Model, TimeoutSeconds: c.Providers.Ollama.Timeout}, true
	}
	if name == "browser" {
		return Engine{Type: "browser"}, true
	}
	return Engine{}, false
}

func Default(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	}
	secret, err := newSecret()
	if err != nil {
		return err
	}
	clusterSecret, err := newSecret()
	if err != nil {
		return err
	}
	content := strings.ReplaceAll(defaultYAML, "GENERATED_TOKEN", secret)
	content = strings.ReplaceAll(content, "GENERATED_CLUSTER_ADMIN_TOKEN", clusterSecret)
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
	if cfg.Storage.Models == "" {
		cfg.Storage.Models = filepath.Join(base, "models")
	} else if !filepath.IsAbs(cfg.Storage.Models) {
		cfg.Storage.Models = filepath.Join(base, cfg.Storage.Models)
	}
	if cfg.Runtime.HardwareRefreshSeconds <= 0 {
		cfg.Runtime.HardwareRefreshSeconds = 10
	}
	if cfg.Terminal.Style == "" {
		cfg.Terminal.Style = "panel"
	}
	if cfg.RAG.Backend == "" {
		cfg.RAG.Backend = "local"
	}
	if cfg.RAG.EmbeddingRoute == "" {
		cfg.RAG.EmbeddingRoute = "embedding"
	}
	if cfg.RAG.MaxDocuments <= 0 {
		cfg.RAG.MaxDocuments = 10000
	}
	if cfg.RAG.Directory == "" {
		cfg.RAG.Directory = filepath.Join(cfg.Storage.Directory, "rag")
	} else if !filepath.IsAbs(cfg.RAG.Directory) {
		cfg.RAG.Directory = filepath.Join(base, cfg.RAG.Directory)
	}
	if cfg.Engines == nil {
		cfg.Engines = map[string]Engine{}
	}
	if cfg.Models == nil {
		cfg.Models = map[string]Model{}
	}
	for name, engine := range cfg.Engines {
		if engine.TimeoutSeconds <= 0 {
			engine.TimeoutSeconds = 120
		}
		if engine.Type == "llama_cpp" {
			if engine.Listen == "" {
				engine.Listen = "127.0.0.1:32146"
			}
			if engine.Executable == "" {
				engine.Executable = "auto"
			}
			if engine.GPU == "" {
				engine.GPU = "prefer"
			}
		}
		cfg.Engines[name] = engine
	}
	if cfg.Providers.Ollama.URL == "" {
		cfg.Providers.Ollama.URL = "http://127.0.0.1:11434"
	}
	if cfg.Providers.Ollama.Model == "" {
		cfg.Providers.Ollama.Model = "auto"
	}
	if cfg.Providers.Ollama.Timeout == 0 {
		cfg.Providers.Ollama.Timeout = 45
	}
	if cfg.Providers.Browser.LeaseSeconds == 0 {
		cfg.Providers.Browser.LeaseSeconds = 90
	}
	cfg.Updates.ApplyDefaults()
	for name, route := range cfg.Routes {
		if route.TimeoutSeconds == 0 {
			route.TimeoutSeconds = 180
		}
		cfg.Routes[name] = route
	}
	if cfg.Cluster.Relay.Listen == "" {
		cfg.Cluster.Relay.Listen = "127.0.0.1:32150"
	}
	if cfg.Cluster.Relay.Database == "" {
		cfg.Cluster.Relay.Database = filepath.Join(cfg.Storage.Directory, "cluster.db")
	} else if !filepath.IsAbs(cfg.Cluster.Relay.Database) {
		cfg.Cluster.Relay.Database = filepath.Join(base, cfg.Cluster.Relay.Database)
	}
	if cfg.Cluster.Relay.MaxQueue <= 0 {
		cfg.Cluster.Relay.MaxQueue = 10000
	}
	if cfg.Cluster.Relay.MaxJobBytes <= 0 {
		cfg.Cluster.Relay.MaxJobBytes = 12 << 20
	}
	if cfg.Cluster.Relay.PairingTTLSeconds <= 0 {
		cfg.Cluster.Relay.PairingTTLSeconds = 600
	}
	if cfg.Cluster.Relay.RetentionDays == 0 {
		cfg.Cluster.Relay.RetentionDays = cluster.DefaultRetentionDays
	}
	if cfg.Cluster.Relay.MaxTerminalJobs == 0 {
		cfg.Cluster.Relay.MaxTerminalJobs = cluster.DefaultMaxTerminalJobs
	}
	if cfg.Cluster.Relay.MaxEvents == 0 {
		cfg.Cluster.Relay.MaxEvents = cluster.DefaultMaxEvents
	}
	if cfg.Cluster.Relay.MaxTerminalPipelineRuns == 0 {
		cfg.Cluster.Relay.MaxTerminalPipelineRuns = cluster.DefaultMaxTerminalPipelineRuns
	}
	if cfg.Cluster.Relay.MaxSessionPlacements == 0 {
		cfg.Cluster.Relay.MaxSessionPlacements = cluster.DefaultMaxSessionPlacements
	}
	if cfg.Cluster.Relay.RetentionSweepSeconds == 0 {
		cfg.Cluster.Relay.RetentionSweepSeconds = cluster.DefaultRetentionSweepSeconds
	}
	if cfg.Cluster.Worker.IdentityFile == "" {
		cfg.Cluster.Worker.IdentityFile = filepath.Join(cfg.Storage.Directory, "cluster-identity.json")
	} else if !filepath.IsAbs(cfg.Cluster.Worker.IdentityFile) {
		cfg.Cluster.Worker.IdentityFile = filepath.Join(base, cfg.Cluster.Worker.IdentityFile)
	}
	if cfg.Cluster.Worker.NodeName == "" {
		cfg.Cluster.Worker.NodeName = "auto"
	}
	if len(cfg.Cluster.Worker.Groups) == 0 {
		cfg.Cluster.Worker.Groups = []string{"default"}
	}
	if cfg.Cluster.Worker.MaxConcurrent <= 0 {
		cfg.Cluster.Worker.MaxConcurrent = 1
	}
	if cfg.Cluster.Worker.LocalURL == "" {
		cfg.Cluster.Worker.LocalURL = "http://" + cfg.Server.Listen
	}
	if cfg.Cluster.Worker.LocalToken == "" {
		cfg.Cluster.Worker.LocalToken = cfg.Server.Token
	}
	if cfg.Cluster.Worker.HeartbeatSeconds <= 0 {
		cfg.Cluster.Worker.HeartbeatSeconds = 5
	}
	if len(cfg.Cluster.Policies.AllowedTasks) == 0 {
		cfg.Cluster.Policies.AllowedTasks = []string{"moderation", "generation", "extraction", "embedding", "rag_ingest", "rag_query", "vision"}
	}
	if cfg.Cluster.Policies.MaxAttempts <= 0 {
		cfg.Cluster.Policies.MaxAttempts = 3
	}
	if cfg.Cluster.Policies.MaxJobRuntime <= 0 {
		cfg.Cluster.Policies.MaxJobRuntime = 900
	}
	if cfg.Cluster.Policies.MaxSteps <= 0 {
		cfg.Cluster.Policies.MaxSteps = 24
	}
	if cfg.Cluster.Policies.MaxRuntime <= 0 {
		cfg.Cluster.Policies.MaxRuntime = 1800
	}
	if cfg.Cluster.Pipelines == nil {
		cfg.Cluster.Pipelines = map[string]cluster.Pipeline{}
	}
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var safeArtifactPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,255}$`)
var safeRepositoryPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var sha256Pattern = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
var reservedRuntimeFlags = map[string]bool{
	"--host": true, "--port": true, "--model": true, "-m": true, "--mmproj": true,
	"--n-gpu-layers": true, "-ngl": true, "--embedding": true, "--pooling": true,
}

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
  models: ./models

runtime:
  hardware_refresh_seconds: 10

terminal:
  style: panel # panel or classic; applies to interactive terminals only

updates:
  enabled: null # off by default; enable from the dashboard, extension, or CLI
  channel: stable
  repository: IamAngusU/ContextBridge
  check_interval_hours: 24

routes:
  default:
    provider: ollama
    fallback: [browser]
    timeout_seconds: 180
    browser_profile: ""
  inkwall:
    provider: ollama
    fallback: [browser]
    timeout_seconds: 180
    browser_profile: ""
    task: moderation
  extraction:
    provider: nuextract
    fallback: [ollama, browser]
    timeout_seconds: 180
    browser_profile: ""
    task: extraction
  embedding:
    provider: jina
    fallback: [ollama]
    timeout_seconds: 180
    task: embedding
  rag_ingest:
    provider: jina
    timeout_seconds: 180
    task: rag_ingest
  rag_query:
    provider: jina
    timeout_seconds: 180
    task: rag_query

providers:
  ollama:
    url: http://127.0.0.1:11434
    model: auto
    images: true
    timeout_seconds: 45
  browser:
    lease_seconds: 90

engines:
  nuextract:
    type: llama_cpp
    model: nuextract3
    executable: auto
    listen: 127.0.0.1:32146
    auto_start: false
    gpu: prefer
    mode: generation
    timeout_seconds: 120
  jina:
    type: llama_cpp
    model: jina-v4-retrieval
    executable: auto
    listen: 127.0.0.1:32147
    auto_start: false
    gpu: prefer
    mode: embedding
    pooling: mean
    timeout_seconds: 120

models:
  nuextract3:
    repository: numind/NuExtract3-GGUF
    file: NuExtract3-Q4_K_M.gguf
    projector_file: mmproj-NuExtract3-BF16.gguf
    kind: extraction
  jina-v4-retrieval:
    repository: jinaai/jina-embeddings-v4-text-retrieval-GGUF
    file: jina-embeddings-v4-text-retrieval-Q4_K_M.gguf
    kind: embedding
    query_prefix: "Query: "
    passage_prefix: "Passage: "
    dimensions: 2048

tunnel:
  mode: external
  target: ""
  local_port: 0
  remote_port: 0

rag:
  enabled: true
  backend: local
  directory: ./data/rag
  embedding_route: embedding
  max_documents: 10000

cluster:
  relay:
    enabled: false
    listen: 127.0.0.1:32150
    public_url: ""
    database: ./data/cluster.db
    admin_token: GENERATED_CLUSTER_ADMIN_TOKEN
    allowed_origins: []
    max_queue: 10000
    max_job_bytes: 12582912
    pairing_ttl_seconds: 600
    retention_days: 30
    max_terminal_jobs: 500
    max_events: 5000
    max_terminal_pipeline_runs: 200
    max_session_placements: 5000
    retention_sweep_seconds: 300
  worker:
    enabled: false
    relay_url: ""
    identity_file: ./data/cluster-identity.json
    node_name: auto
    groups: [default]
    tags: []
    max_concurrent: 1
    allowed_tasks: []
    allowed_providers: []
    allowed_models: []
    local_url: http://127.0.0.1:32145
    local_token: GENERATED_TOKEN
    heartbeat_seconds: 5
  policies:
    allowed_tasks: [moderation, generation, extraction, embedding, rag_ingest, rag_query, vision]
    max_attempts: 3
    max_job_runtime_seconds: 900
    max_pipeline_steps: 24
    max_pipeline_runtime_seconds: 1800
  pricing:
    compute_per_hour_usd: 0
    input_per_million_usd: 0
    output_per_million_usd: 0
    equivalent_input_per_million_usd: 0
    equivalent_output_per_million_usd: 0
  pipelines: {}

browser_profiles:
  chatgpt:
    label: ChatGPT
    match_url: https://chatgpt.com/*
    selectors:
      input: ['#prompt-textarea', '[contenteditable="true"]']
      file_input: ['input[type="file"]']
      submit: ['button[data-testid="send-button"]', 'button[aria-label*="Send"]']
      response: ['[data-message-author-role="assistant"]', 'section[data-turn="assistant"][data-testid^="conversation-turn-"]']
  gemini:
    label: Gemini
    match_url: https://gemini.google.com/*
    selectors:
      input: ['rich-textarea div[contenteditable="true"][role="textbox"]', 'div[contenteditable="true"][aria-label*="Prompt für Gemini" i]', 'div.ql-editor[contenteditable="true"][role="textbox"]']
      file_input: ['input[type="file"]']
      submit: ['button[data-test-id="send-button"]', 'button[data-testid="send-button"]', 'button[aria-label*="prompt senden" i]', 'button[aria-label*="send message" i]']
      response: ['model-response message-content .markdown[aria-live="polite"]', 'model-response .model-response-text']
`
