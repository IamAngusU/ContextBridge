package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
	Portable        PortableResources         `yaml:"portable_resources" json:"portable_resources"`
	Updates         updater.Settings          `yaml:"updates" json:"updates"`
	Routes          map[string]Route          `yaml:"routes"`
	Providers       Providers                 `yaml:"providers"`
	Engines         map[string]Engine         `yaml:"engines"`
	Models          map[string]Model          `yaml:"models"`
	RAG             RAG                       `yaml:"rag"`
	Tunnel          Tunnel                    `yaml:"tunnel"`
	AdapterProfiles map[string]AdapterProfile `yaml:"adapter_profiles"`
	Cluster         Cluster                   `yaml:"cluster"`
}

type Server struct {
	Listen string `yaml:"listen"`
	Token  string `yaml:"token"`
}

type Storage struct {
	Directory          string `yaml:"directory"`
	Inbox              string `yaml:"inbox"`
	Models             string `yaml:"models"`
	JobRetentionDays   int    `yaml:"job_retention_days"`
	MaxJobRecords      int    `yaml:"max_job_records"`
	MaxJobStorageBytes int64  `yaml:"max_job_storage_bytes"`
}

type Runtime struct {
	HardwareRefreshSeconds int `yaml:"hardware_refresh_seconds"`
}

type Terminal struct {
	Style               string `yaml:"style"`
	MaxPromptCharacters int    `yaml:"max_prompt_characters,omitempty"`
}

// PortableResources enables bounded discovery of declarative resource-pack
// manifests on local fixed/removable volumes. A manifest may publish local
// endpoints, but it can never ask ContextBridge to execute a program.
type PortableResources struct {
	Enabled           *bool    `yaml:"enabled" json:"enabled"`
	ScanRoots         []string `yaml:"scan_roots,omitempty" json:"scan_roots,omitempty"`
	MaxPacks          int      `yaml:"max_packs,omitempty" json:"max_packs,omitempty"`
	MaxScanCandidates int      `yaml:"max_scan_candidates,omitempty" json:"max_scan_candidates,omitempty"`
}

type Route struct {
	Provider       string   `yaml:"provider" json:"provider"`
	Fallback       []string `yaml:"fallback" json:"fallback"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	AdapterProfile string   `yaml:"adapter_profile" json:"adapter_profile"`
	Task           string   `yaml:"task,omitempty" json:"task,omitempty"`
	Model          string   `yaml:"model,omitempty" json:"model,omitempty"`
}

type Engine struct {
	Type  string `yaml:"type" json:"type"`
	URL   string `yaml:"url,omitempty" json:"url,omitempty"`
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// CredentialSlot is a non-secret operator label for the credential role used
	// by this engine. It lets execution approvals distinguish accounts without
	// serializing the credential itself; rotating a secret inside the same slot
	// intentionally leaves that identity unchanged.
	CredentialSlot      string        `yaml:"credential_slot,omitempty" json:"credential_slot,omitempty"`
	APIKey              string        `yaml:"api_key,omitempty" json:"-"`
	APIKeyFile          string        `yaml:"api_key_file,omitempty" json:"-"`
	ResolvedAPIKey      string        `yaml:"-" json:"-"`
	Remote              bool          `yaml:"remote,omitempty" json:"remote,omitempty"`
	Capabilities        []string      `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	ResourcePack        string        `yaml:"resource_pack,omitempty" json:"resource_pack,omitempty"`
	Endpoint            string        `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Executable          string        `yaml:"executable,omitempty" json:"executable,omitempty"`
	Listen              string        `yaml:"listen,omitempty" json:"listen,omitempty"`
	AutoStart           bool          `yaml:"auto_start,omitempty" json:"auto_start,omitempty"`
	GPU                 string        `yaml:"gpu,omitempty" json:"gpu,omitempty"`
	Mode                string        `yaml:"mode,omitempty" json:"mode,omitempty"`
	Pooling             string        `yaml:"pooling,omitempty" json:"pooling,omitempty"`
	TimeoutSeconds      int           `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	MaxOutputTokens     int           `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	ContextWindowTokens int           `yaml:"context_window_tokens,omitempty" json:"context_window_tokens,omitempty"`
	MaxInputImages      int           `yaml:"max_input_images,omitempty" json:"max_input_images,omitempty"`
	MaxImageBytes       int64         `yaml:"max_image_bytes,omitempty" json:"max_image_bytes,omitempty"`
	MaxTotalImageBytes  int64         `yaml:"max_total_image_bytes,omitempty" json:"max_total_image_bytes,omitempty"`
	ImageMediaTypes     []string      `yaml:"image_media_types,omitempty" json:"image_media_types,omitempty"`
	ReasoningEffort     string        `yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
	BalancePath         string        `yaml:"balance_path,omitempty" json:"balance_path,omitempty"`
	MinimumBalanceUSD   float64       `yaml:"minimum_balance_usd,omitempty" json:"minimum_balance_usd,omitempty"`
	Costing             EngineCosting `yaml:"costing,omitempty" json:"costing,omitempty"`
	Args                []string      `yaml:"args,omitempty" json:"args,omitempty"`
}

// EngineCosting describes an operator-reviewed cost ceiling. It is not an
// invoice: provider-reported token counts are multiplied by these configured
// rates and remain explicitly labelled as an upper-bound estimate.
type EngineCosting struct {
	Mode                     string  `yaml:"mode,omitempty" json:"mode,omitempty"`
	Source                   string  `yaml:"source,omitempty" json:"source,omitempty"`
	InputPerMillionUSD       float64 `yaml:"input_per_million_usd,omitempty" json:"input_per_million_usd,omitempty"`
	CachedInputPerMillionUSD float64 `yaml:"cached_input_per_million_usd,omitempty" json:"cached_input_per_million_usd,omitempty"`
	OutputPerMillionUSD      float64 `yaml:"output_per_million_usd,omitempty" json:"output_per_million_usd,omitempty"`
}

type Model struct {
	Repository    string `yaml:"repository" json:"repository"`
	Revision      string `yaml:"revision,omitempty" json:"revision,omitempty"`
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
	Adapter AdapterProvider `yaml:"adapter"`
}

type OllamaProvider struct {
	URL     string `yaml:"url"`
	Model   string `yaml:"model"`
	Images  bool   `yaml:"images"`
	Timeout int    `yaml:"timeout_seconds"`
}

type AdapterProvider struct {
	LeaseSeconds int                         `yaml:"lease_seconds" json:"lease_seconds"`
	AuthMode     string                      `yaml:"auth_mode,omitempty" json:"auth_mode,omitempty"`
	Principals   map[string]AdapterPrincipal `yaml:"principals,omitempty" json:"principals,omitempty"`
}

// AdapterPrincipal is a least-privilege identity for one out-of-tree adapter
// process. Its credential is deliberately independent of server.token, which
// remains the operator credential.
type AdapterPrincipal struct {
	Token           string   `yaml:"token,omitempty" json:"-"`
	TokenFile       string   `yaml:"token_file,omitempty" json:"-"`
	ResolvedToken   string   `yaml:"-" json:"-"`
	AllowedProfiles []string `yaml:"allowed_profiles" json:"allowed_profiles"`
}

func (p AdapterPrincipal) EffectiveToken() string {
	if p.ResolvedToken != "" {
		return p.ResolvedToken
	}
	return p.Token
}

type AdapterProfile struct {
	Label   string                 `yaml:"label" json:"label"`
	Driver  string                 `yaml:"driver,omitempty" json:"driver,omitempty"`
	Options map[string]interface{} `yaml:"options,omitempty" json:"options,omitempty"`
}

type Cluster struct {
	Relay       ClusterRelay                `yaml:"relay" json:"relay"`
	Worker      ClusterWorker               `yaml:"worker" json:"worker"`
	Placement   ClusterPlacement            `yaml:"placement" json:"placement"`
	ClientToken string                      `yaml:"client_token,omitempty" json:"-"`
	Policies    ClusterPolicies             `yaml:"policies" json:"policies"`
	Pricing     cluster.Pricing             `yaml:"pricing" json:"pricing"`
	Pipelines   map[string]cluster.Pipeline `yaml:"pipelines" json:"pipelines"`
}

// ClusterPlacement contains operator-tunable soft ranking behavior. None of
// these values can widen task, provider, tenant, cost, trust, or hardware
// requirements; they only rank workers that already passed every hard gate.
type ClusterPlacement struct {
	PerformanceLearning *bool   `yaml:"performance_learning" json:"performance_learning"`
	MinimumSamples      int     `yaml:"minimum_samples" json:"minimum_samples"`
	HistoryTTLHours     int     `yaml:"history_ttl_hours" json:"history_ttl_hours"`
	LatencyWeight       float64 `yaml:"latency_weight" json:"latency_weight"`
	MaxLatencyPenalty   float64 `yaml:"max_latency_penalty" json:"max_latency_penalty"`
}

type ClusterRelay struct {
	Enabled                 bool            `yaml:"enabled" json:"enabled"`
	Listen                  string          `yaml:"listen" json:"listen"`
	PublicURL               string          `yaml:"public_url" json:"public_url"`
	LAN                     ClusterRelayLAN `yaml:"lan" json:"lan"`
	Database                string          `yaml:"database" json:"database"`
	AdminToken              string          `yaml:"admin_token" json:"-"`
	AllowedOrigins          []string        `yaml:"allowed_origins" json:"allowed_origins"`
	MaxQueue                int             `yaml:"max_queue" json:"max_queue"`
	MaxJobBytes             int64           `yaml:"max_job_bytes" json:"max_job_bytes"`
	PairingTTLSeconds       int             `yaml:"pairing_ttl_seconds" json:"pairing_ttl_seconds"`
	RetentionDays           int             `yaml:"retention_days" json:"retention_days"`
	MaxTerminalJobs         int             `yaml:"max_terminal_jobs" json:"max_terminal_jobs"`
	MaxEvents               int             `yaml:"max_events" json:"max_events"`
	MaxTerminalPipelineRuns int             `yaml:"max_terminal_pipeline_runs" json:"max_terminal_pipeline_runs"`
	MaxSessionPlacements    int             `yaml:"max_session_placements" json:"max_session_placements"`
	RetentionSweepSeconds   int             `yaml:"retention_sweep_seconds" json:"retention_sweep_seconds"`
}

type ClusterRelayLAN struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	Listen          string `yaml:"listen" json:"listen"`
	PublicURL       string `yaml:"public_url" json:"public_url"`
	CertificateFile string `yaml:"certificate_file" json:"certificate_file"`
	PrivateKeyFile  string `yaml:"private_key_file" json:"-"`
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
	AllowedTasks  []string                      `yaml:"allowed_tasks" json:"allowed_tasks"`
	MaxAttempts   int                           `yaml:"max_attempts" json:"max_attempts"`
	MaxJobRuntime int                           `yaml:"max_job_runtime_seconds" json:"max_job_runtime_seconds"`
	MaxSteps      int                           `yaml:"max_pipeline_steps" json:"max_pipeline_steps"`
	MaxRuntime    int                           `yaml:"max_pipeline_runtime_seconds" json:"max_pipeline_runtime_seconds"`
	Execution     cluster.ExecutionPolicyConfig `yaml:"execution" json:"execution"`
	// AgentAuthorities are named, operator-owned envelopes for automatic text
	// workflows. Planner output can select only targets inside one envelope; it
	// cannot create, edit, or widen the envelope itself.
	AgentAuthorities map[string]AgentAuthority `yaml:"agent_authorities,omitempty" json:"agent_authorities,omitempty"`
}

type AgentAuthority struct {
	Enabled                bool         `yaml:"enabled" json:"enabled"`
	TenantID               string       `yaml:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	Group                  string       `yaml:"group,omitempty" json:"group,omitempty"`
	Planner                AgentPlanner `yaml:"planner" json:"planner"`
	AllowedProviders       []string     `yaml:"allowed_providers" json:"allowed_providers"`
	AllowedAdapterProfiles []string     `yaml:"allowed_adapter_profiles,omitempty" json:"allowed_adapter_profiles,omitempty"`
	Egress                 string       `yaml:"egress" json:"egress"`
	MaxCostUSD             float64      `yaml:"max_cost_usd,omitempty" json:"max_cost_usd,omitempty"`
	AllowUnknownCost       bool         `yaml:"allow_unknown_cost,omitempty" json:"allow_unknown_cost,omitempty"`
	MaxSteps               int          `yaml:"max_steps" json:"max_steps"`
	StepTimeoutSeconds     int          `yaml:"step_timeout_seconds" json:"step_timeout_seconds"`
	MaxRuntimeSeconds      int          `yaml:"max_runtime_seconds" json:"max_runtime_seconds"`
}

type AgentPlanner struct {
	Provider       string `yaml:"provider" json:"provider"`
	AdapterProfile string `yaml:"adapter_profile,omitempty" json:"adapter_profile,omitempty"`
	Model          string `yaml:"model,omitempty" json:"model,omitempty"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

func Load(path string) (Config, error) {
	raw, err := readConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	expanded := expandEnvironment(string(raw))
	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err == nil {
		return Config{}, errors.New("parse config: multiple YAML documents are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg, filepath.Dir(path))
	if err := resolveEngineSecretFiles(&cfg, filepath.Dir(path)); err != nil {
		return Config{}, err
	}
	if err := resolveAdapterSecretFiles(&cfg, filepath.Dir(path)); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

const maximumConfigBytes int64 = 4 << 20

func readConfigFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumConfigBytes {
		return nil, fmt.Errorf("config must be a regular file no larger than %d bytes", maximumConfigBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximumConfigBytes {
		return nil, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	return raw, nil
}

const maximumProviderSecretBytes int64 = 16 << 10

// EffectiveAPIKey returns an already resolved file secret when present, or the
// explicitly configured inline/environment-expanded value otherwise. The
// resolved value is deliberately excluded from YAML and JSON serialization so
// loading and later saving a config can never copy a file secret into it.
func (e Engine) EffectiveAPIKey() string {
	if e.ResolvedAPIKey != "" {
		return e.ResolvedAPIKey
	}
	return e.APIKey
}

func resolveEngineSecretFiles(cfg *Config, configDirectory string) error {
	for name, engine := range cfg.Engines {
		if strings.TrimSpace(engine.APIKey) != "" && strings.TrimSpace(engine.APIKeyFile) != "" {
			return fmt.Errorf("engine %s must set only one of api_key or api_key_file", name)
		}
		if strings.TrimSpace(engine.APIKeyFile) == "" {
			continue
		}
		secretPath := filepath.Clean(engine.APIKeyFile)
		if !filepath.IsAbs(secretPath) {
			secretPath = filepath.Join(configDirectory, secretPath)
		}
		info, err := os.Lstat(secretPath)
		if err != nil {
			return fmt.Errorf("engine %s api_key_file: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("engine %s api_key_file must be a regular non-symlink file", name)
		}
		if info.Size() <= 0 || info.Size() > maximumProviderSecretBytes {
			return fmt.Errorf("engine %s api_key_file must contain 1 to %d bytes", name, maximumProviderSecretBytes)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("engine %s api_key_file permissions must deny group and other access", name)
		}
		raw, err := os.ReadFile(secretPath)
		if err != nil {
			return fmt.Errorf("engine %s api_key_file: %w", name, err)
		}
		secret := strings.TrimSpace(string(raw))
		if secret == "" || strings.ContainsAny(secret, "\x00\r\n") {
			return fmt.Errorf("engine %s api_key_file must contain exactly one non-empty secret line", name)
		}
		engine.ResolvedAPIKey = secret
		cfg.Engines[name] = engine
	}
	return nil
}

func resolveAdapterSecretFiles(cfg *Config, configDirectory string) error {
	for id, principal := range cfg.Providers.Adapter.Principals {
		if strings.TrimSpace(principal.Token) != "" && strings.TrimSpace(principal.TokenFile) != "" {
			return fmt.Errorf("adapter principal %s must set only one of token or token_file", id)
		}
		if strings.TrimSpace(principal.TokenFile) == "" {
			continue
		}
		secretPath := filepath.Clean(principal.TokenFile)
		if !filepath.IsAbs(secretPath) {
			secretPath = filepath.Join(configDirectory, secretPath)
		}
		info, err := os.Lstat(secretPath)
		if err != nil {
			return fmt.Errorf("adapter principal %s token_file: %w", id, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("adapter principal %s token_file must be a regular non-symlink file", id)
		}
		if info.Size() <= 0 || info.Size() > maximumProviderSecretBytes {
			return fmt.Errorf("adapter principal %s token_file must contain 1 to %d bytes", id, maximumProviderSecretBytes)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("adapter principal %s token_file permissions must deny group and other access", id)
		}
		raw, err := os.ReadFile(secretPath)
		if err != nil {
			return fmt.Errorf("adapter principal %s token_file: %w", id, err)
		}
		secret := strings.TrimSpace(string(raw))
		if secret == "" || strings.ContainsAny(secret, "\x00\r\n") {
			return fmt.Errorf("adapter principal %s token_file must contain exactly one non-empty secret line", id)
		}
		principal.ResolvedToken = secret
		cfg.Providers.Adapter.Principals[id] = principal
	}
	return nil
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
	if c.Terminal.MaxPromptCharacters < 0 || c.Terminal.MaxPromptCharacters > 65536 || (c.Terminal.MaxPromptCharacters > 0 && c.Terminal.MaxPromptCharacters < 64) {
		return errors.New("terminal.max_prompt_characters must be between 64 and 65536 when set")
	}
	if c.Runtime.HardwareRefreshSeconds < 0 || c.Runtime.HardwareRefreshSeconds > 86400 {
		return errors.New("runtime.hardware_refresh_seconds must be between 1 and 86400 when set")
	}
	if c.Storage.JobRetentionDays < 1 || c.Storage.JobRetentionDays > 3650 {
		return errors.New("storage.job_retention_days must be between 1 and 3650")
	}
	if c.Storage.MaxJobRecords < 1 || c.Storage.MaxJobRecords > 1_000_000 {
		return errors.New("storage.max_job_records must be between 1 and 1000000")
	}
	if c.Storage.MaxJobStorageBytes < 64<<20 || c.Storage.MaxJobStorageBytes > 1<<50 {
		return errors.New("storage.max_job_storage_bytes must be between 64 MiB and 1 PiB")
	}
	if c.Portable.MaxPacks < 1 || c.Portable.MaxPacks > 128 {
		return errors.New("portable_resources.max_packs must be between 1 and 128")
	}
	if c.Portable.MaxScanCandidates < 1 || c.Portable.MaxScanCandidates > 32768 {
		return errors.New("portable_resources.max_scan_candidates must be between 1 and 32768")
	}
	if len(c.Portable.ScanRoots) > 32 {
		return errors.New("portable_resources.scan_roots accepts at most 32 paths")
	}
	for _, root := range c.Portable.ScanRoots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("portable resource scan root must be absolute: %s", root)
		}
	}
	if c.Cluster.Worker.MaxConcurrent < 0 || c.Cluster.Worker.MaxConcurrent > cluster.MaximumWorkerConcurrency {
		return fmt.Errorf("cluster.worker.max_concurrent must be between 1 and %d when set", cluster.MaximumWorkerConcurrency)
	}
	if c.Server.Token == "" || strings.Contains(c.Server.Token, "change-me") || strings.Contains(c.Server.Token, "${") {
		return errors.New("server.token must be a strong secret or environment reference")
	}
	if len(c.Server.Token) < 24 {
		return errors.New("server.token must contain at least 24 characters")
	}
	if err := validateLoopbackListen(c.Server.Listen); err != nil {
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
			if route.Task == "embedding" && provider == "adapter" {
				return fmt.Errorf("route %s cannot use a adapter for native embeddings", name)
			}
			if provider != "ollama" && provider != "adapter" {
				if _, ok := c.Engines[provider]; ok {
					continue
				}
				return fmt.Errorf("route %s references unsupported provider %s", name, provider)
			}
		}
		if route.Task != "" && route.Task != "moderation" && route.Task != "generation" && route.Task != "extraction" && route.Task != "embedding" && route.Task != "rag_ingest" && route.Task != "rag_query" {
			return fmt.Errorf("route %s has unsupported task %s", name, route.Task)
		}
		if route.TimeoutSeconds < 0 || route.TimeoutSeconds > 86400 {
			return fmt.Errorf("route %s timeout_seconds must be between 1 and 86400 when set", name)
		}
		if route.Model != "" {
			if engine, ok := c.Engines[route.Provider]; ok && engine.Type == "llama_cpp" {
				if _, modelOK := c.Models[route.Model]; !modelOK {
					return fmt.Errorf("route %s references unknown model %s", name, route.Model)
				}
			}
		}
		if route.AdapterProfile != "" {
			if _, ok := c.AdapterProfiles[route.AdapterProfile]; !ok {
				return fmt.Errorf("route %s references unknown adapter profile %s", name, route.AdapterProfile)
			}
		}
	}
	for name, profile := range c.AdapterProfiles {
		if len(name) > 80 || !safeNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("invalid adapter profile name %s", name)
		}
		if label := profile.Label; label != "" && (len(label) > 100 || strings.TrimSpace(label) != label || !utf8.ValidString(label) || strings.IndexFunc(label, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		}) >= 0) {
			return fmt.Errorf("adapter profile %s label must be at most 100 UTF-8 bytes without surrounding whitespace or control characters", name)
		}
		if driver := profile.Driver; driver != "" && (len(driver) > 80 || !safeNamePattern.MatchString(driver) || strings.Contains(driver, "..")) {
			return fmt.Errorf("adapter profile %s has an invalid driver ID", name)
		}
		if len(profile.Options) > 64 {
			return fmt.Errorf("adapter profile %s has more than 64 options", name)
		}
		for option := range profile.Options {
			if len(option) > 80 || !safeNamePattern.MatchString(option) || strings.Contains(option, "..") {
				return fmt.Errorf("adapter profile %s has an invalid option name", name)
			}
		}
	}
	for name, engine := range c.Engines {
		if !safeNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("invalid engine name %s", name)
		}
		if engine.Type != "ollama" && engine.Type != "llama_cpp" && engine.Type != "adapter" && engine.Type != "openai_compatible" {
			return fmt.Errorf("engine %s has unsupported type %s", name, engine.Type)
		}
		if engine.AutoStart && engine.Type != "llama_cpp" {
			return fmt.Errorf("engine %s auto_start is supported only for ContextBridge-managed llama_cpp engines", name)
		}
		if engine.TimeoutSeconds < 0 || engine.TimeoutSeconds > 86400 {
			return fmt.Errorf("engine %s timeout_seconds must be between 1 and 86400 when set", name)
		}
		if engine.MaxOutputTokens < 0 || engine.MaxOutputTokens > 1_000_000 {
			return fmt.Errorf("engine %s max_output_tokens must be between 1 and 1000000 when set", name)
		}
		if engine.ContextWindowTokens < 0 || engine.ContextWindowTokens > 10_000_000 {
			return fmt.Errorf("engine %s context_window_tokens must be between 1 and 10000000 when set", name)
		}
		if engine.MaxInputImages < 0 || engine.MaxInputImages > 1024 {
			return fmt.Errorf("engine %s max_input_images must be between 1 and 1024 when set", name)
		}
		if engine.MaxImageBytes < 0 || engine.MaxImageBytes > 1<<30 || engine.MaxTotalImageBytes < 0 || engine.MaxTotalImageBytes > 4<<30 {
			return fmt.Errorf("engine %s image byte limits are outside the supported range", name)
		}
		if engine.MaxImageBytes > 0 && engine.MaxTotalImageBytes > 0 && engine.MaxTotalImageBytes < engine.MaxImageBytes {
			return fmt.Errorf("engine %s max_total_image_bytes must not be smaller than max_image_bytes", name)
		}
		if len(engine.ImageMediaTypes) > 16 {
			return fmt.Errorf("engine %s image_media_types accepts at most 16 entries", name)
		}
		for _, mediaType := range engine.ImageMediaTypes {
			switch strings.ToLower(strings.TrimSpace(mediaType)) {
			case "image/png", "image/jpeg", "image/webp", "image/gif":
			default:
				return fmt.Errorf("engine %s has unsupported image media type %q", name, mediaType)
			}
		}
		if (engine.MaxInputImages > 0 || engine.MaxImageBytes > 0 || engine.MaxTotalImageBytes > 0 || len(engine.ImageMediaTypes) > 0) && !containsFoldConfig(engine.Capabilities, "vision") {
			return fmt.Errorf("engine %s image limits require the vision capability", name)
		}
		if engine.CredentialSlot != "" && (!safeNamePattern.MatchString(engine.CredentialSlot) || strings.Contains(engine.CredentialSlot, "..")) {
			return fmt.Errorf("engine %s credential_slot must be a stable safe identifier", name)
		}
		if engine.ResourcePack != "" {
			if !safeNamePattern.MatchString(engine.ResourcePack) || strings.Contains(engine.ResourcePack, "..") {
				return fmt.Errorf("engine %s resource_pack must be a stable safe identifier", name)
			}
			if engine.Endpoint == "" || !safeNamePattern.MatchString(engine.Endpoint) || strings.Contains(engine.Endpoint, "..") {
				return fmt.Errorf("engine %s endpoint is required with resource_pack", name)
			}
			if strings.TrimSpace(engine.APIKey) != "" || strings.TrimSpace(engine.APIKeyFile) != "" || strings.TrimSpace(engine.ResolvedAPIKey) != "" {
				return fmt.Errorf("engine %s resource_pack cannot be combined with api credentials", name)
			}
		}
		if engine.Type == "openai_compatible" {
			if strings.TrimSpace(engine.APIKey) != "" && strings.TrimSpace(engine.APIKeyFile) != "" {
				return fmt.Errorf("engine %s must set only one of api_key or api_key_file", name)
			}
			if engine.Model == "" {
				return fmt.Errorf("engine %s requires an explicit model", name)
			}
			if engine.URL == "" && engine.ResourcePack == "" {
				return fmt.Errorf("engine %s requires url or resource_pack", name)
			}
			if engine.URL != "" {
				remote, err := validateProviderURL(engine.URL)
				if err != nil {
					return fmt.Errorf("engine %s: %w", name, err)
				}
				if remote && !engine.Remote {
					return fmt.Errorf("engine %s must set remote: true before prompts may leave this device", name)
				}
				secret := strings.TrimSpace(engine.EffectiveAPIKey())
				if remote && (secret == "" || strings.Contains(secret, "${")) {
					return fmt.Errorf("engine %s requires a resolved api_key for remote access", name)
				}
			}
			if len(engine.Capabilities) == 0 {
				return fmt.Errorf("engine %s requires explicit capabilities", name)
			}
			if engine.ReasoningEffort != "" {
				switch strings.ToLower(strings.TrimSpace(engine.ReasoningEffort)) {
				case "none", "low", "high", "max":
				default:
					return fmt.Errorf("engine %s reasoning_effort must be none, low, high, or max", name)
				}
			}
			if engine.BalancePath != "" {
				parsed, err := url.Parse(engine.BalancePath)
				if err != nil || !strings.HasPrefix(engine.BalancePath, "/") || strings.HasPrefix(engine.BalancePath, "//") || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
					return fmt.Errorf("engine %s balance_path must be an absolute path on the configured provider origin", name)
				}
			}
			if math.IsNaN(engine.MinimumBalanceUSD) || math.IsInf(engine.MinimumBalanceUSD, 0) || engine.MinimumBalanceUSD < 0 || engine.MinimumBalanceUSD > 1_000_000_000 {
				return fmt.Errorf("engine %s minimum_balance_usd is invalid", name)
			}
			if err := validateEngineCosting(name, engine.Costing); err != nil {
				return err
			}
			if engine.MinimumBalanceUSD > 0 && (engine.BalancePath == "" || engine.MaxOutputTokens == 0 || engine.Costing.Mode != "upper_bound") {
				return fmt.Errorf("engine %s minimum_balance_usd requires balance_path, max_output_tokens, and upper_bound costing", name)
			}
		}
		for _, capability := range engine.Capabilities {
			switch strings.ToLower(strings.TrimSpace(capability)) {
			case "text", "vision", "embedding":
			case "incremental_output":
				if engine.Type != "openai_compatible" {
					return fmt.Errorf("engine %s incremental_output is supported only by openai_compatible engines", name)
				}
			default:
				return fmt.Errorf("engine %s has unsupported capability %s", name, capability)
			}
		}
		if engine.GPU != "" && engine.GPU != "prefer" && engine.GPU != "require" && engine.GPU != "off" {
			return fmt.Errorf("engine %s gpu must be prefer, require, or off", name)
		}
		if engine.Model != "" {
			if _, ok := c.Models[engine.Model]; !ok && engine.Type == "llama_cpp" {
				return fmt.Errorf("engine %s references unknown model %s", name, engine.Model)
			}
		}
		if engine.Listen != "" {
			if err := validateLoopbackListen(engine.Listen); err != nil {
				return fmt.Errorf("engine %s must listen on localhost", name)
			}
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
		if model.Revision != "" && !huggingFaceRevisionPattern.MatchString(model.Revision) {
			return fmt.Errorf("model %s revision must be an immutable 40- or 64-character hexadecimal commit", name)
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
	if c.RAG.MaxDocuments < 0 || c.RAG.MaxDocuments > 1_000_000 {
		return errors.New("rag.max_documents must be between 1 and 1000000 when set")
	}
	if c.Providers.Ollama.Timeout < 0 || c.Providers.Ollama.Timeout > 86400 {
		return errors.New("providers.ollama.timeout_seconds must be between 1 and 86400 when set")
	}
	if c.Providers.Adapter.LeaseSeconds < 0 || c.Providers.Adapter.LeaseSeconds > 3600 {
		return errors.New("providers.adapter.lease_seconds must be between 1 and 3600 when set")
	}
	if err := c.validateAdapterAuthentication(); err != nil {
		return err
	}
	if c.Tunnel.LocalPort < 0 || c.Tunnel.LocalPort > 65535 || c.Tunnel.RemotePort < 0 || c.Tunnel.RemotePort > 65535 {
		return errors.New("tunnel ports must be between 1 and 65535 when set")
	}
	if c.Cluster.Relay.Enabled {
		if len(c.Cluster.Relay.AdminToken) < 32 || strings.Contains(c.Cluster.Relay.AdminToken, "${") {
			return errors.New("cluster.relay.admin_token must contain at least 32 resolved characters")
		}
		if c.Cluster.Relay.MaxJobBytes < 0 || c.Cluster.Relay.MaxJobBytes > cluster.MaximumJobPayloadBytes {
			return fmt.Errorf("cluster.relay.max_job_bytes must not exceed %d MiB", cluster.MaximumJobPayloadBytes>>20)
		}
		if err := validateLoopbackListen(c.Cluster.Relay.Listen); err != nil {
			return errors.New("cluster.relay.listen must use localhost; publish it through a TLS reverse proxy")
		}
		if c.Cluster.Relay.LAN.Enabled {
			if err := validateLANListen(c.Cluster.Relay.LAN.Listen); err != nil {
				return fmt.Errorf("cluster.relay.lan.listen: %w", err)
			}
			if err := cluster.ValidateRelayURL(c.Cluster.Relay.LAN.PublicURL); err != nil || !strings.HasPrefix(strings.ToLower(c.Cluster.Relay.LAN.PublicURL), "https://") {
				return errors.New("cluster.relay.lan.public_url must be an absolute HTTPS URL")
			}
			if err := cluster.ValidateLANListenerEndpoint(c.Cluster.Relay.LAN.Listen, c.Cluster.Relay.LAN.PublicURL); err != nil {
				return fmt.Errorf("cluster.relay.lan endpoint: %w", err)
			}
			if strings.TrimSpace(c.Cluster.Relay.LAN.CertificateFile) == "" || strings.TrimSpace(c.Cluster.Relay.LAN.PrivateKeyFile) == "" {
				return errors.New("cluster.relay.lan certificate_file and private_key_file are required")
			}
		}
	}
	if c.Cluster.Relay.MaxQueue < 0 || c.Cluster.Relay.MaxQueue > 1_000_000 {
		return errors.New("cluster.relay.max_queue must be between 1 and 1000000 when set")
	}
	if c.Cluster.Relay.PairingTTLSeconds < 0 || c.Cluster.Relay.PairingTTLSeconds > 86400 {
		return errors.New("cluster.relay.pairing_ttl_seconds must be between 1 and 86400 when set")
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
	if c.Cluster.Relay.PublicURL != "" {
		if err := cluster.ValidateRelayURL(c.Cluster.Relay.PublicURL); err != nil {
			return fmt.Errorf("cluster.relay.public_url: %w", err)
		}
	}
	if c.Cluster.Worker.Enabled && strings.TrimSpace(c.Cluster.Worker.RelayURL) == "" {
		return errors.New("cluster.worker.relay_url is required when the worker is enabled")
	}
	if strings.TrimSpace(c.Cluster.Worker.RelayURL) != "" {
		if err := cluster.ValidateRelayURL(c.Cluster.Worker.RelayURL); err != nil {
			return fmt.Errorf("cluster.worker.relay_url: %w", err)
		}
	}
	if strings.TrimSpace(c.Cluster.Worker.LocalURL) != "" {
		if err := cluster.ValidateLocalWorkerURL(c.Cluster.Worker.LocalURL); err != nil {
			return fmt.Errorf("cluster.worker.local_url: %w", err)
		}
	}
	if c.Cluster.Worker.HeartbeatSeconds < 0 || c.Cluster.Worker.HeartbeatSeconds > 300 {
		return errors.New("cluster.worker.heartbeat_seconds must be between 1 and 300 when set")
	}
	if c.Cluster.Placement.MinimumSamples < 1 || c.Cluster.Placement.MinimumSamples > 1000 {
		return errors.New("cluster.placement.minimum_samples must be between 1 and 1000")
	}
	if c.Cluster.Placement.HistoryTTLHours < 1 || c.Cluster.Placement.HistoryTTLHours > 8760 {
		return errors.New("cluster.placement.history_ttl_hours must be between 1 and 8760")
	}
	if math.IsNaN(c.Cluster.Placement.LatencyWeight) || math.IsInf(c.Cluster.Placement.LatencyWeight, 0) || c.Cluster.Placement.LatencyWeight <= 0 || c.Cluster.Placement.LatencyWeight > 100 {
		return errors.New("cluster.placement.latency_weight must be finite and between 0 and 100")
	}
	if math.IsNaN(c.Cluster.Placement.MaxLatencyPenalty) || math.IsInf(c.Cluster.Placement.MaxLatencyPenalty, 0) || c.Cluster.Placement.MaxLatencyPenalty <= 0 || c.Cluster.Placement.MaxLatencyPenalty > 1000 {
		return errors.New("cluster.placement.max_latency_penalty must be finite and between 0 and 1000")
	}
	if c.Cluster.Policies.MaxAttempts < 0 || c.Cluster.Policies.MaxAttempts > 10 {
		return errors.New("cluster.policies.max_attempts must be between 1 and 10 when set")
	}
	if c.Cluster.Policies.MaxJobRuntime < 0 || c.Cluster.Policies.MaxJobRuntime > 86400 {
		return errors.New("cluster.policies.max_job_runtime_seconds must be between 1 and 86400 when set")
	}
	if c.Cluster.Policies.MaxSteps < 0 || c.Cluster.Policies.MaxSteps > 256 {
		return errors.New("cluster.policies.max_pipeline_steps must be between 1 and 256 when set")
	}
	if c.Cluster.Policies.MaxRuntime < 0 || c.Cluster.Policies.MaxRuntime > 604800 {
		return errors.New("cluster.policies.max_pipeline_runtime_seconds must be between 1 and 604800 when set")
	}
	pricingRates := []float64{c.Cluster.Pricing.ComputePerHourUSD, c.Cluster.Pricing.InputPerMillionUSD, c.Cluster.Pricing.OutputPerMillionUSD, c.Cluster.Pricing.EquivalentInputUSD, c.Cluster.Pricing.EquivalentOutputUSD}
	for _, rate := range pricingRates {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1_000_000 {
			return errors.New("cluster.pricing rates must be finite non-negative USD values")
		}
	}
	if c.Cluster.Pricing.Mode != "" && c.Cluster.Pricing.Mode != "estimated" {
		return errors.New("cluster.pricing.mode must be estimated when set")
	}
	if c.Cluster.Pricing.Mode == "estimated" && strings.TrimSpace(c.Cluster.Pricing.Source) == "" {
		return errors.New("cluster.pricing.source is required for estimated costs")
	}
	if err := c.Cluster.Policies.Execution.Validate(); err != nil {
		return err
	}
	if err := validateAgentAuthorities(c.Cluster.Policies.AgentAuthorities); err != nil {
		return err
	}
	for name, pipeline := range c.Cluster.Pipelines {
		if !safeNamePattern.MatchString(name) || len(pipeline.Steps) == 0 || len(pipeline.Steps) > c.Cluster.Policies.MaxSteps {
			return fmt.Errorf("pipeline %s must have between 1 and %d steps", name, c.Cluster.Policies.MaxSteps)
		}
		if strings.TrimSpace(pipeline.TenantID) != "" && (!safeNamePattern.MatchString(pipeline.TenantID) || strings.Contains(pipeline.TenantID, "..")) {
			return fmt.Errorf("pipeline %s has an invalid tenant_id", name)
		}
		if pipeline.MaxRuntimeSeconds < 0 || pipeline.MaxRuntimeSeconds > c.Cluster.Policies.MaxRuntime {
			return fmt.Errorf("pipeline %s max_runtime_seconds must not exceed the cluster pipeline runtime limit", name)
		}
		if pipeline.MaxIterations < 0 || pipeline.MaxIterations > 20 {
			return fmt.Errorf("pipeline %s max_iterations must be between 1 and 20 when set", name)
		}
		globalIterations := pipeline.MaxIterations
		if globalIterations == 0 {
			globalIterations = 3
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
			if step.TimeoutSeconds < 0 || step.TimeoutSeconds > c.Cluster.Policies.MaxJobRuntime {
				return fmt.Errorf("pipeline %s step %s timeout_seconds exceeds the cluster job runtime limit", name, step.Name)
			}
			if step.MaxIterations < 0 || step.MaxIterations > globalIterations {
				return fmt.Errorf("pipeline %s step %s max_iterations exceeds the pipeline limit", name, step.Name)
			}
		}
		if _, err := cluster.PlanPipelineGraph(pipeline); err != nil {
			return fmt.Errorf("pipeline %s: %w", name, err)
		}
	}
	return nil
}

func (c Config) validateAdapterAuthentication() error {
	mode := strings.ToLower(strings.TrimSpace(c.Providers.Adapter.AuthMode))
	if mode == "" {
		mode = "scoped"
	}
	if mode != "scoped" && mode != "dual" {
		return errors.New("providers.adapter.auth_mode must be scoped or dual")
	}
	if len(c.Providers.Adapter.Principals) > 32 {
		return errors.New("providers.adapter.principals accepts at most 32 identities")
	}
	seenTokens := map[[32]byte]string{}
	coveredProfiles := map[string]bool{}
	for id, principal := range c.Providers.Adapter.Principals {
		if len(id) > 80 || !safeNamePattern.MatchString(id) || strings.Contains(id, "..") {
			return fmt.Errorf("invalid adapter principal ID %s", id)
		}
		token := strings.TrimSpace(principal.EffectiveToken())
		if len(token) < 32 || strings.Contains(token, "change-me") || strings.Contains(token, "${") {
			return fmt.Errorf("adapter principal %s requires an independent token of at least 32 characters", id)
		}
		if token == c.Server.Token {
			return fmt.Errorf("adapter principal %s must not reuse server.token", id)
		}
		digest := sha256.Sum256([]byte(token))
		if previous, exists := seenTokens[digest]; exists {
			return fmt.Errorf("adapter principals %s and %s must not share a token", previous, id)
		}
		seenTokens[digest] = id
		if len(principal.AllowedProfiles) == 0 || len(principal.AllowedProfiles) > 32 {
			return fmt.Errorf("adapter principal %s must allow between 1 and 32 profiles", id)
		}
		seenProfiles := map[string]bool{}
		for _, profile := range principal.AllowedProfiles {
			profile = strings.TrimSpace(profile)
			if _, exists := c.AdapterProfiles[profile]; !exists {
				return fmt.Errorf("adapter principal %s references unknown profile %s", id, profile)
			}
			if seenProfiles[profile] {
				return fmt.Errorf("adapter principal %s repeats profile %s", id, profile)
			}
			seenProfiles[profile] = true
			coveredProfiles[profile] = true
		}
	}
	if mode == "scoped" {
		for routeName, route := range c.Routes {
			usesAdapter := strings.EqualFold(route.Provider, "adapter") || containsFoldConfig(route.Fallback, "adapter")
			if usesAdapter && strings.TrimSpace(route.AdapterProfile) == "" {
				return fmt.Errorf("route %s requires adapter_profile when providers.adapter.auth_mode is scoped", routeName)
			}
			if usesAdapter && !coveredProfiles[route.AdapterProfile] {
				return fmt.Errorf("route %s adapter profile %s has no scoped adapter principal", routeName, route.AdapterProfile)
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
	if name == "adapter" {
		return Engine{Type: "adapter"}, true
	}
	return Engine{}, false
}

func validateAgentAuthorities(authorities map[string]AgentAuthority) error {
	if len(authorities) > 64 {
		return errors.New("cluster.policies.agent_authorities accepts at most 64 named policies")
	}
	seenNames := map[string]string{}
	for name, authority := range authorities {
		if !safeNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("agent authority %q has an invalid name", name)
		}
		foldedName := strings.ToLower(name)
		if existing, found := seenNames[foldedName]; found {
			return fmt.Errorf("agent authority names %q and %q are case-insensitively ambiguous", existing, name)
		}
		seenNames[foldedName] = name
		if authority.TenantID != "" && (!safeNamePattern.MatchString(authority.TenantID) || strings.Contains(authority.TenantID, "..")) {
			return fmt.Errorf("agent authority %s has an invalid tenant_id", name)
		}
		if authority.Group != "" && (!safeNamePattern.MatchString(authority.Group) || strings.Contains(authority.Group, "..")) {
			return fmt.Errorf("agent authority %s has an invalid group", name)
		}
		if authority.Egress != "local_only" && authority.Egress != "remote_allowed" {
			return fmt.Errorf("agent authority %s egress must be local_only or remote_allowed", name)
		}
		if math.IsNaN(authority.MaxCostUSD) || math.IsInf(authority.MaxCostUSD, 0) || authority.MaxCostUSD < 0 || authority.MaxCostUSD > 1_000_000 {
			return fmt.Errorf("agent authority %s max_cost_usd must be a finite value from 0 through 1000000", name)
		}
		if authority.MaxSteps < 1 || authority.MaxSteps > 6 {
			return fmt.Errorf("agent authority %s max_steps must be between 1 and 6", name)
		}
		if authority.StepTimeoutSeconds < 10 || authority.StepTimeoutSeconds > 900 {
			return fmt.Errorf("agent authority %s step_timeout_seconds must be between 10 and 900", name)
		}
		if authority.MaxRuntimeSeconds < 30 || authority.MaxRuntimeSeconds > 1800 {
			return fmt.Errorf("agent authority %s max_runtime_seconds must be between 30 and 1800", name)
		}
		if authority.Planner.TimeoutSeconds < 10 || authority.Planner.TimeoutSeconds > 600 {
			return fmt.Errorf("agent authority %s planner.timeout_seconds must be between 10 and 600", name)
		}
		if err := validateAgentAuthorityLabels(name, "allowed_providers", authority.AllowedProviders, 32, 80); err != nil {
			return err
		}
		if len(authority.AllowedProviders) == 0 {
			return fmt.Errorf("agent authority %s requires allowed_providers", name)
		}
		if err := validateAgentAuthorityLabels(name, "allowed_adapter_profiles", authority.AllowedAdapterProfiles, 32, 80); err != nil {
			return err
		}
		plannerProvider := strings.ToLower(strings.TrimSpace(authority.Planner.Provider))
		if !safeNamePattern.MatchString(plannerProvider) || strings.Contains(plannerProvider, "..") {
			return fmt.Errorf("agent authority %s has an invalid planner provider", name)
		}
		if authority.Planner.Model != "" && !validConfiguredRoutingLabel(authority.Planner.Model, 160) {
			return fmt.Errorf("agent authority %s has an invalid planner model", name)
		}
		if plannerProvider == "adapter" {
			if authority.Planner.AdapterProfile == "" || !containsFoldConfig(authority.AllowedAdapterProfiles, authority.Planner.AdapterProfile) {
				return fmt.Errorf("agent authority %s adapter planner requires an allowed adapter_profile", name)
			}
		} else if authority.Planner.AdapterProfile != "" {
			return fmt.Errorf("agent authority %s planner.adapter_profile requires provider adapter", name)
		}
		if len(authority.AllowedAdapterProfiles) > 0 && !containsFoldConfig(authority.AllowedProviders, "adapter") {
			return fmt.Errorf("agent authority %s adapter profiles require adapter in allowed_providers", name)
		}
		if containsFoldConfig(authority.AllowedProviders, "adapter") && len(authority.AllowedAdapterProfiles) == 0 {
			return fmt.Errorf("agent authority %s must name at least one allowed_adapter_profile when adapter is allowed", name)
		}
	}
	return nil
}

func validConfiguredRoutingLabel(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		}) < 0
}

func validateAgentAuthorityLabels(authority, field string, values []string, maximumCount, maximumBytes int) error {
	if len(values) > maximumCount {
		return fmt.Errorf("agent authority %s %s accepts at most %d entries", authority, field, maximumCount)
	}
	seen := map[string]bool{}
	for _, value := range values {
		if len(value) == 0 || len(value) > maximumBytes || strings.TrimSpace(value) != value || !safeNamePattern.MatchString(value) || strings.Contains(value, "..") {
			return fmt.Errorf("agent authority %s has an invalid %s entry", authority, field)
		}
		folded := strings.ToLower(value)
		if seen[folded] {
			return fmt.Errorf("agent authority %s %s contains duplicate %q", authority, field, value)
		}
		seen[folded] = true
	}
	return nil
}

func containsFoldConfig(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
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
	if cfg.Storage.JobRetentionDays == 0 {
		cfg.Storage.JobRetentionDays = 30
	}
	if cfg.Storage.MaxJobRecords == 0 {
		cfg.Storage.MaxJobRecords = 1000
	}
	if cfg.Storage.MaxJobStorageBytes == 0 {
		cfg.Storage.MaxJobStorageBytes = 4 << 30
	}
	if cfg.Runtime.HardwareRefreshSeconds == 0 {
		cfg.Runtime.HardwareRefreshSeconds = 10
	}
	if cfg.Terminal.Style == "" {
		cfg.Terminal.Style = "panel"
	}
	if cfg.Terminal.MaxPromptCharacters == 0 {
		cfg.Terminal.MaxPromptCharacters = 4096
	}
	if cfg.Portable.Enabled == nil {
		enabled := true
		cfg.Portable.Enabled = &enabled
	}
	if cfg.Portable.MaxPacks == 0 {
		cfg.Portable.MaxPacks = 32
	}
	if cfg.Portable.MaxScanCandidates == 0 {
		cfg.Portable.MaxScanCandidates = 4096
	}
	for index, root := range cfg.Portable.ScanRoots {
		if strings.TrimSpace(root) != "" && !filepath.IsAbs(root) {
			cfg.Portable.ScanRoots[index] = filepath.Join(base, root)
		}
	}
	if cfg.RAG.Backend == "" {
		cfg.RAG.Backend = "local"
	}
	if cfg.RAG.EmbeddingRoute == "" {
		cfg.RAG.EmbeddingRoute = "embedding"
	}
	if cfg.RAG.MaxDocuments == 0 {
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
		if engine.TimeoutSeconds == 0 {
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
	if cfg.Providers.Adapter.LeaseSeconds == 0 {
		cfg.Providers.Adapter.LeaseSeconds = 90
	}
	if cfg.Providers.Adapter.AuthMode == "" {
		cfg.Providers.Adapter.AuthMode = "scoped"
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
	if cfg.Cluster.Relay.LAN.Listen == "" {
		// Enabling LAN manually without running the explicit initialization
		// ceremony must not expose the relay on every interface. `cluster lan
		// init` replaces this host with the selected advertised LAN address.
		cfg.Cluster.Relay.LAN.Listen = "127.0.0.1:32151"
	}
	if cfg.Cluster.Relay.LAN.CertificateFile == "" {
		cfg.Cluster.Relay.LAN.CertificateFile = filepath.Join(cfg.Storage.Directory, "lan", "relay-cert.pem")
	} else if !filepath.IsAbs(cfg.Cluster.Relay.LAN.CertificateFile) {
		cfg.Cluster.Relay.LAN.CertificateFile = filepath.Join(base, cfg.Cluster.Relay.LAN.CertificateFile)
	}
	if cfg.Cluster.Relay.LAN.PrivateKeyFile == "" {
		cfg.Cluster.Relay.LAN.PrivateKeyFile = filepath.Join(cfg.Storage.Directory, "lan", "relay-key.pem")
	} else if !filepath.IsAbs(cfg.Cluster.Relay.LAN.PrivateKeyFile) {
		cfg.Cluster.Relay.LAN.PrivateKeyFile = filepath.Join(base, cfg.Cluster.Relay.LAN.PrivateKeyFile)
	}
	if cfg.Cluster.Relay.Database == "" {
		cfg.Cluster.Relay.Database = filepath.Join(cfg.Storage.Directory, "cluster.db")
	} else if !filepath.IsAbs(cfg.Cluster.Relay.Database) {
		cfg.Cluster.Relay.Database = filepath.Join(base, cfg.Cluster.Relay.Database)
	}
	if cfg.Cluster.Relay.MaxQueue == 0 {
		cfg.Cluster.Relay.MaxQueue = 10000
	}
	if cfg.Cluster.Relay.MaxJobBytes == 0 {
		cfg.Cluster.Relay.MaxJobBytes = 12 << 20
	}
	if cfg.Cluster.Relay.PairingTTLSeconds == 0 {
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
	if cfg.Cluster.Worker.MaxConcurrent == 0 {
		cfg.Cluster.Worker.MaxConcurrent = 1
	}
	if cfg.Cluster.Worker.LocalURL == "" {
		cfg.Cluster.Worker.LocalURL = "http://" + cfg.Server.Listen
	}
	if cfg.Cluster.Worker.LocalToken == "" {
		cfg.Cluster.Worker.LocalToken = cfg.Server.Token
	}
	if cfg.Cluster.Worker.HeartbeatSeconds == 0 {
		cfg.Cluster.Worker.HeartbeatSeconds = 5
	}
	if cfg.Cluster.Placement.PerformanceLearning == nil {
		enabled := true
		cfg.Cluster.Placement.PerformanceLearning = &enabled
	}
	if cfg.Cluster.Placement.MinimumSamples == 0 {
		cfg.Cluster.Placement.MinimumSamples = 3
	}
	if cfg.Cluster.Placement.HistoryTTLHours == 0 {
		cfg.Cluster.Placement.HistoryTTLHours = 168
	}
	if cfg.Cluster.Placement.LatencyWeight == 0 {
		cfg.Cluster.Placement.LatencyWeight = 12
	}
	if cfg.Cluster.Placement.MaxLatencyPenalty == 0 {
		cfg.Cluster.Placement.MaxLatencyPenalty = 60
	}
	if len(cfg.Cluster.Policies.AllowedTasks) == 0 {
		cfg.Cluster.Policies.AllowedTasks = []string{"moderation", "generation", "extraction", "embedding", "rag_ingest", "rag_query", "vision"}
	}
	if cfg.Cluster.Policies.MaxAttempts == 0 {
		cfg.Cluster.Policies.MaxAttempts = 3
	}
	if cfg.Cluster.Policies.MaxJobRuntime == 0 {
		cfg.Cluster.Policies.MaxJobRuntime = 900
	}
	if cfg.Cluster.Policies.MaxSteps == 0 {
		cfg.Cluster.Policies.MaxSteps = 24
	}
	if cfg.Cluster.Policies.MaxRuntime == 0 {
		cfg.Cluster.Policies.MaxRuntime = 1800
	}
	if len(cfg.Cluster.Policies.Execution.LocalProviders) == 0 {
		cfg.Cluster.Policies.Execution.LocalProviders = []string{"arsenal", "llama_cpp", "modelkit", "modelkit_vision", "ollama"}
	}
	if len(cfg.Cluster.Policies.Execution.RemoteProviders) == 0 {
		cfg.Cluster.Policies.Execution.RemoteProviders = []string{"adapter"}
	}
	if cfg.Cluster.Pricing.Mode == "" && (cfg.Cluster.Pricing.ComputePerHourUSD > 0 || cfg.Cluster.Pricing.InputPerMillionUSD > 0 || cfg.Cluster.Pricing.OutputPerMillionUSD > 0) {
		cfg.Cluster.Pricing.Mode = "estimated"
		if strings.TrimSpace(cfg.Cluster.Pricing.Source) == "" {
			cfg.Cluster.Pricing.Source = "legacy_relay_config"
		}
	}
	if cfg.Cluster.Pipelines == nil {
		cfg.Cluster.Pipelines = map[string]cluster.Pipeline{}
	}
	if cfg.Cluster.Policies.AgentAuthorities == nil {
		cfg.Cluster.Policies.AgentAuthorities = map[string]AgentAuthority{}
	}
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var safeArtifactPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,255}$`)
var safeRepositoryPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var sha256Pattern = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
var huggingFaceRevisionPattern = regexp.MustCompile(`^[A-Fa-f0-9]{40}(?:[A-Fa-f0-9]{24})?$`)
var reservedRuntimeFlags = map[string]bool{
	"--host": true, "--port": true, "--model": true, "-m": true, "--mmproj": true,
	"--n-gpu-layers": true, "-ngl": true, "--embedding": true, "--pooling": true,
}

func validateEngineCosting(name string, costing EngineCosting) error {
	rates := []float64{costing.InputPerMillionUSD, costing.CachedInputPerMillionUSD, costing.OutputPerMillionUSD}
	for _, rate := range rates {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1_000_000 {
			return fmt.Errorf("engine %s costing rates must be finite non-negative USD values", name)
		}
	}
	switch costing.Mode {
	case "":
		if costing.Source != "" || costing.InputPerMillionUSD != 0 || costing.CachedInputPerMillionUSD != 0 || costing.OutputPerMillionUSD != 0 {
			return fmt.Errorf("engine %s costing rates require mode: upper_bound", name)
		}
	case "upper_bound":
		if strings.TrimSpace(costing.Source) == "" || len(costing.Source) > 200 {
			return fmt.Errorf("engine %s upper_bound costing requires a short source", name)
		}
		if costing.InputPerMillionUSD == 0 || costing.OutputPerMillionUSD == 0 {
			return fmt.Errorf("engine %s upper_bound costing requires positive input and output rates", name)
		}
		if costing.CachedInputPerMillionUSD > costing.InputPerMillionUSD {
			return fmt.Errorf("engine %s cached input rate cannot exceed its input ceiling", name)
		}
	default:
		return fmt.Errorf("engine %s costing mode must be upper_bound when set", name)
	}
	return nil
}

// validateProviderURL returns true only for non-loopback HTTPS endpoints.
// Remote endpoints must be acknowledged explicitly because they receive the
// trusted task prompt and submitted content.
func validateProviderURL(value string) (bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false, errors.New("url must be an absolute endpoint without credentials, query, or fragment")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if loopback && parsed.Scheme == "http" {
		return false, nil
	}
	if parsed.Scheme != "https" {
		return false, errors.New("url must use HTTPS unless it is loopback HTTP")
	}
	return !loopback, nil
}

// ProviderURLIsRemote classifies a resolved execution endpoint using the same
// fail-closed URL rules as configuration validation. HTTP is accepted only for
// loopback; any non-loopback endpoint must use HTTPS.
func ProviderURLIsRemote(value string) (bool, error) {
	return validateProviderURL(value)
}

func validateLoopbackListen(value string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || host == "" || port == "" {
		return errors.New("listen address must contain a loopback host and numeric port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("listen address port must be between 1 and 65535")
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("listen address host must be loopback")
	}
	return nil
}

func validateLANListen(value string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || port == "" {
		return errors.New("LAN listen address must contain an IP host and numeric port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("LAN listen address port must be between 1 and 65535")
	}
	if host == "" {
		return errors.New("LAN listen address must use an explicit IP or wildcard")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("LAN listen address host must be an IP or wildcard, not a DNS name")
	}
	if !ip.IsUnspecified() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
		return errors.New("LAN listen address must be private, link-local, loopback, or a wildcard")
	}
	return nil
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
  job_retention_days: 30
  max_job_records: 1000
  max_job_storage_bytes: 4294967296 # 4 GiB; inbox delivery artifacts are separate

runtime:
  hardware_refresh_seconds: 10

terminal:
  style: panel # panel or classic; applies to interactive terminals only
  max_prompt_characters: 4096 # bounded interactive send; relay byte limits still apply

portable_resources:
  enabled: true # bounded marker discovery only; never auto-runs removable-drive code
  scan_roots: [] # empty = local fixed/removable volumes and common mount roots
  max_packs: 32
  max_scan_candidates: 4096 # hard global work bound; exhaustion fails closed

updates:
  enabled: null # off by default; enable from the dashboard or CLI
  channel: stable
  repository: IamAngusU/ContextBridge
  check_interval_hours: 24

routes:
  default:
    provider: ollama
    fallback: []
    timeout_seconds: 180
    adapter_profile: ""
  inkwall:
    provider: ollama
    fallback: []
    timeout_seconds: 180
    adapter_profile: ""
    task: moderation
  extraction:
    provider: nuextract
    fallback: [ollama]
    timeout_seconds: 180
    adapter_profile: ""
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
  adapter:
    lease_seconds: 90
    auth_mode: scoped # adapter credentials never reuse the operator token
    principals: {}

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
    lan:
      enabled: false
      listen: 127.0.0.1:32151
      public_url: ""
      certificate_file: ./data/lan/relay-cert.pem
      private_key_file: ./data/lan/relay-key.pem
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
  placement:
    # Soft evidence only. Hard permissions/capabilities and current load are
    # always evaluated first; unknown workers remain eligible to learn.
    performance_learning: true
    minimum_samples: 3
    history_ttl_hours: 168
    latency_weight: 12
    max_latency_penalty: 60
  policies:
    allowed_tasks: [moderation, generation, extraction, embedding, rag_ingest, rag_query, vision]
    # Assignment generations. Only proven pre-execution worker capacity or
    # shutdown refusals are retried; ambiguous execution is always terminal.
    max_attempts: 3
    max_job_runtime_seconds: 900
    max_pipeline_steps: 24
    max_pipeline_runtime_seconds: 1800
    # Optional named project authority envelopes are disabled unless the owner
    # adds and explicitly enables them. An authority's max_cost_usd is one
    # aggregate reservation ceiling across its planner and remote agent steps,
    # not a fresh allowance for every job. See docs/bounded-agent.md.
    agent_authorities: {}
    execution:
      enabled: false
      tenant_mode: open
      require_tenant: false
      local_providers: [arsenal, llama_cpp, modelkit, modelkit_vision, ollama]
      remote_providers: []
      cost_bounded_providers: []
      default:
        egress: any
        allowed_providers: []
        allowed_groups: []
        require_cost_budget: false
        max_cost_usd: 0
      tenants: {}
  pricing:
    compute_per_hour_usd: 0
    input_per_million_usd: 0
    output_per_million_usd: 0
    equivalent_input_per_million_usd: 0
    equivalent_output_per_million_usd: 0
  pipelines: {}

adapter_profiles: {}
`
