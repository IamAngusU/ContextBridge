package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/vectorstore"
	"gopkg.in/yaml.v3"
)

const ManagedSecretMarker = "__CONTEXTBRIDGE_KEEP_SECRET__"

var managedConfigMu sync.Mutex

type ManagedConfig struct {
	Schema          string   `json:"schema"`
	YAML            string   `json:"yaml"`
	Revision        string   `json:"revision"`
	SecretMarker    string   `json:"secret_marker"`
	ChangedSections []string `json:"changed_sections,omitempty"`
	RestartRequired bool     `json:"restart_required"`
}

type ManagedValidation struct {
	Schema           string   `json:"schema"`
	Valid            bool     `json:"valid"`
	Revision         string   `json:"revision,omitempty"`
	BaseRevision     string   `json:"base_revision,omitempty"`
	ProposedRevision string   `json:"proposed_revision,omitempty"`
	ChangedSections  []string `json:"changed_sections,omitempty"`
	RestartRequired  bool     `json:"restart_required"`
}

// ManagedConfigConstraints is a UI-oriented catalog of the bounds enforced by
// Config.Validate. Wildcards address named map entries. Cross-field rules stay
// authoritative in POST /v1/config and are summarized separately by the
// management schema endpoint.
func ManagedConfigConstraints() []map[string]interface{} {
	constraint := func(path, kind string, values ...interface{}) map[string]interface{} {
		item := map[string]interface{}{"path": path, "type": kind}
		for index := 0; index+1 < len(values); index += 2 {
			item[values[index].(string)] = values[index+1]
		}
		return item
	}
	return []map[string]interface{}{
		constraint("version", "integer", "const", 1),
		constraint("server.listen", "string", "format", "loopback-host-port"),
		constraint("server.token", "secret", "min_length", 24, "write_only", true),
		constraint("terminal.style", "string", "enum", []string{"classic", "panel"}),
		constraint("terminal.max_prompt_characters", "integer", "minimum_when_set", 64, "maximum", 65536, "zero_means_default", true),
		constraint("runtime.hardware_refresh_seconds", "integer", "minimum_when_set", 1, "maximum", 86400, "zero_means_default", true),
		constraint("storage.job_retention_days", "integer", "minimum", 1, "maximum", 3650),
		constraint("storage.max_job_records", "integer", "minimum", 1, "maximum", 1000000),
		constraint("storage.max_job_storage_bytes", "integer", "minimum", int64(64<<20), "maximum", int64(1<<50)),
		constraint("portable_resources.scan_roots", "array", "max_items", 32, "item_format", "absolute-path"),
		constraint("portable_resources.max_packs", "integer", "minimum", 1, "maximum", 128),
		constraint("portable_resources.max_scan_candidates", "integer", "minimum", 1, "maximum", 32768),
		constraint("updates.channel", "string", "enum", []string{"stable", "preview"}),
		constraint("updates.check_interval_hours", "integer", "minimum", 1, "maximum", 720),
		constraint("routes.*.task", "string", "enum", []string{"", "moderation", "generation", "extraction", "embedding", "rag_ingest", "rag_query", "speech_to_text"}),
		constraint("routes.*.timeout_seconds", "integer", "minimum_when_set", 1, "maximum", 86400, "zero_means_default", true),
		constraint("adapter_profiles.*.label", "string", "max_bytes", 100),
		constraint("adapter_profiles.*.options", "object", "max_properties", 64),
		constraint("engines.*.type", "string", "enum", []string{"ollama", "llama_cpp", "adapter", "openai_compatible"}),
		constraint("engines.*.api_key", "secret", "write_only", true),
		constraint("engines.*.api_key_file", "string", "read_only", true, "change_via", "local-config-file"),
		constraint("engines.*.timeout_seconds", "integer", "minimum_when_set", 1, "maximum", 86400, "zero_means_default", true),
		constraint("engines.*.max_output_tokens", "integer", "minimum_when_set", 1, "maximum", 1000000, "zero_means_default", true),
		constraint("engines.*.context_window_tokens", "integer", "minimum_when_set", 1, "maximum", 10000000, "zero_means_default", true),
		constraint("engines.*.max_input_images", "integer", "minimum_when_set", 1, "maximum", 1024, "zero_means_default", true),
		constraint("engines.*.max_image_bytes", "integer", "minimum_when_set", 1, "maximum", int64(1<<30), "zero_means_default", true),
		constraint("engines.*.max_total_image_bytes", "integer", "minimum_when_set", 1, "maximum", int64(4<<30), "zero_means_default", true),
		constraint("engines.*.image_media_types", "array", "max_items", 16, "item_enum", []string{"image/png", "image/jpeg", "image/webp", "image/gif"}),
		constraint("engines.*.capabilities", "array", "item_enum", []string{"text", "vision", "embedding", "incremental_output"}),
		constraint("engines.*.reasoning_effort", "string", "enum", []string{"", "none", "low", "high", "max"}),
		constraint("engines.*.gpu", "string", "enum", []string{"", "prefer", "require", "off"}),
		constraint("engines.*.minimum_balance_usd", "number", "minimum", 0, "maximum", 1000000000, "finite", true),
		constraint("engines.*.costing.mode", "string", "enum", []string{"", "upper_bound"}),
		constraint("engines.*.costing.*_per_million_usd", "number", "minimum", 0, "maximum", 1000000, "finite", true),
		constraint("models.*.revision", "string", "pattern", "^[A-Fa-f0-9]{40}(?:[A-Fa-f0-9]{24})?$"),
		constraint("models.*.sha256", "string", "pattern", "^(?:sha256:)?[A-Fa-f0-9]{64}$"),
		constraint("models.*.dimensions", "integer", "minimum_when_set", 1, "maximum", vectorstore.MaximumVectorDimensions, "zero_means_default", true),
		constraint("rag.embedding_revision", "string", "max_bytes", 200),
		constraint("rag.max_documents", "integer", "minimum_when_set", 1, "maximum", 1000000, "zero_means_default", true),
		constraint("providers.ollama.timeout_seconds", "integer", "minimum_when_set", 1, "maximum", 86400, "zero_means_default", true),
		constraint("providers.adapter.lease_seconds", "integer", "minimum_when_set", 1, "maximum", 3600, "zero_means_default", true),
		constraint("providers.adapter.auth_mode", "string", "enum", []string{"", "scoped", "dual"}),
		constraint("providers.adapter.principals", "object", "max_properties", 32),
		constraint("providers.adapter.principals.*.token", "secret", "min_length", 32, "write_only", true),
		constraint("providers.adapter.principals.*.token_file", "string", "read_only", true, "change_via", "local-config-file"),
		constraint("providers.adapter.principals.*.allowed_profiles", "array", "min_items", 1, "max_items", 32),
		constraint("tunnel.local_port", "integer", "minimum_when_set", 1, "maximum", 65535, "zero_means_default", true),
		constraint("tunnel.remote_port", "integer", "minimum_when_set", 1, "maximum", 65535, "zero_means_default", true),
		constraint("cluster.client_token", "secret", "write_only", true),
		constraint("cluster.pool_authority_file", "string", "max_length", 4096, "format", "local-regular-file"),
		constraint("cluster.active_account", "string", "references", "cluster.accounts"),
		constraint("cluster.accounts", "object", "max_properties", 64),
		constraint("cluster.accounts.*.relay_url", "string", "format", "relay-url"),
		constraint("cluster.accounts.*.client_token", "secret", "write_only", true),
		constraint("cluster.accounts.*.pool_authority_file", "string", "max_length", 4096, "format", "local-regular-file"),
		constraint("cluster.relay.admin_token", "secret", "min_length", 32, "write_only", true),
		constraint("cluster.relay.max_queue", "integer", "minimum_when_set", 1, "maximum", 1000000, "zero_means_default", true),
		constraint("cluster.relay.max_job_bytes", "integer", "minimum", 0, "maximum", cluster.MaximumJobPayloadBytes),
		constraint("cluster.relay.pairing_ttl_seconds", "integer", "minimum_when_set", 1, "maximum", 86400, "zero_means_default", true),
		constraint("cluster.relay.retention_days", "integer", "minimum", 1, "maximum", cluster.MaximumRetentionDays),
		constraint("cluster.relay.max_terminal_jobs", "integer", "minimum", 1, "maximum", cluster.MaximumRetainedTerminalJobs),
		constraint("cluster.relay.max_events", "integer", "minimum", 1, "maximum", cluster.MaximumRetainedEvents),
		constraint("cluster.relay.max_terminal_pipeline_runs", "integer", "minimum", 1, "maximum", cluster.MaximumRetainedTerminalPipelineRuns),
		constraint("cluster.relay.max_session_placements", "integer", "minimum", 1, "maximum", cluster.MaximumRetainedSessionPlacements),
		constraint("cluster.relay.retention_sweep_seconds", "integer", "minimum", cluster.MinimumRetentionSweepSeconds, "maximum", cluster.MaximumRetentionSweepSeconds),
		constraint("cluster.worker.local_token", "secret", "write_only", true),
		constraint("cluster.worker.max_concurrent", "integer", "minimum_when_set", 1, "maximum", cluster.MaximumWorkerConcurrency, "zero_means_default", true),
		constraint("cluster.worker.heartbeat_seconds", "integer", "minimum_when_set", 1, "maximum", 300, "zero_means_default", true),
		constraint("cluster.placement.minimum_samples", "integer", "minimum", 1, "maximum", 1000),
		constraint("cluster.placement.history_ttl_hours", "integer", "minimum", 1, "maximum", 8760),
		constraint("cluster.placement.latency_weight", "number", "exclusive_minimum", 0, "maximum", 100, "finite", true),
		constraint("cluster.placement.max_latency_penalty", "number", "exclusive_minimum", 0, "maximum", 1000, "finite", true),
		constraint("cluster.policies.max_attempts", "integer", "minimum_when_set", 1, "maximum", 10, "zero_means_default", true),
		constraint("cluster.policies.max_job_runtime_seconds", "integer", "minimum_when_set", 1, "maximum", 86400, "zero_means_default", true),
		constraint("cluster.policies.max_pipeline_steps", "integer", "minimum_when_set", 1, "maximum", 256, "zero_means_default", true),
		constraint("cluster.policies.max_pipeline_runtime_seconds", "integer", "minimum_when_set", 1, "maximum", 604800, "zero_means_default", true),
		constraint("cluster.pricing.mode", "string", "enum", []string{"", "estimated"}),
		constraint("cluster.pricing.*_usd", "number", "minimum", 0, "maximum", 1000000, "finite", true),
		constraint("cluster.policies.execution.tenant_mode", "string", "enum", []string{"", "open", "listed_only"}),
		constraint("cluster.policies.execution.*.egress", "string", "enum", []string{"", "any", "local_only", "remote_only"}),
		constraint("cluster.policies.execution.*.max_cost_usd", "number", "minimum", 0, "maximum", 1000000, "finite", true),
		constraint("cluster.policies.agent_authorities", "object", "max_properties", 64),
		constraint("cluster.policies.agent_authorities.*.egress", "string", "enum", []string{"local_only", "remote_allowed"}),
		constraint("cluster.policies.agent_authorities.*.max_cost_usd", "number", "minimum", 0, "maximum", 1000000, "finite", true),
		constraint("cluster.policies.agent_authorities.*.max_steps", "integer", "minimum", 1, "maximum", 6),
		constraint("cluster.policies.agent_authorities.*.step_timeout_seconds", "integer", "minimum", 10, "maximum", 900),
		constraint("cluster.policies.agent_authorities.*.max_runtime_seconds", "integer", "minimum", 30, "maximum", 1800),
		constraint("cluster.policies.agent_authorities.*.planner.timeout_seconds", "integer", "minimum", 10, "maximum", 600),
		constraint("cluster.policies.agent_authorities.*.allowed_providers", "array", "min_items", 1, "max_items", 32),
		constraint("cluster.policies.agent_authorities.*.allowed_adapter_profiles", "array", "max_items", 32),
		constraint("cluster.pipelines.*.mode", "string", "enum", []string{"", cluster.PipelineModeLinear, cluster.PipelineModeDAG}),
		constraint("cluster.pipelines.*.max_parallel", "integer", "minimum_when_set", 1, "maximum", cluster.MaximumPipelineParallelism, "zero_means_linear", true),
		constraint("cluster.pipelines.*.max_runtime_seconds", "integer", "minimum", 0, "maximum_from", "cluster.policies.max_pipeline_runtime_seconds"),
		constraint("cluster.pipelines.*.max_iterations", "integer", "minimum_when_set", 1, "maximum", 20, "zero_means_default", true),
		constraint("cluster.pipelines.*.steps", "array", "min_items", 1, "max_items_from", "cluster.policies.max_pipeline_steps"),
		constraint("cluster.pipelines.*.steps.*.depends_on", "array", "max_items", cluster.MaximumPipelineDependencyFan),
		constraint("cluster.pipelines.*.steps.*.retries", "integer", "minimum", 0, "maximum_from", "cluster.policies.max_attempts"),
		constraint("cluster.pipelines.*.steps.*.timeout_seconds", "integer", "minimum_when_set", 1, "maximum_from", "cluster.policies.max_job_runtime_seconds", "zero_means_default", true),
		constraint("cluster.pipelines.*.steps.*.max_iterations", "integer", "minimum", 0, "maximum_from", "cluster.pipelines.*.max_iterations"),
		constraint("cluster.pipelines.*.steps.*.requirements.task", "string", "min_bytes", 1, "max_bytes", 160),
		constraint("cluster.pipelines.*.steps.*.requirements.provider", "string", "max_bytes", 80),
		constraint("cluster.pipelines.*.steps.*.requirements.required_tags", "array", "max_items", 32, "item_max_bytes", 80),
		constraint("cluster.pipelines.*.steps.*.requirements.preferred_nodes", "array", "max_items", 32, "item_max_bytes", 160),
		constraint("cluster.pipelines.*.steps.*.requirements.min_free_vram_bytes", "integer", "minimum", 0, "maximum", cluster.MaximumNodeHardwareBytes),
		constraint("cluster.pipelines.*.steps.*.requirements.input_image_count", "integer", "minimum", 0, "maximum", 12),
		constraint("cluster.pipelines.*.steps.*.requirements.input_image_bytes", "integer", "minimum", 0, "maximum", int64(8<<20)),
		constraint("cluster.pipelines.*.steps.*.requirements.input_image_max_bytes", "integer", "minimum", 0, "maximum_from", "cluster.pipelines.*.steps.*.requirements.input_image_bytes"),
		constraint("cluster.pipelines.*.steps.*.requirements.input_image_media_types", "array", "max_items", 4, "item_enum", []string{"image/png", "image/jpeg", "image/webp", "image/gif"}),
		constraint("cluster.pipelines.*.steps.*.requirements.egress", "string", "enum", []string{"", "local_only", "remote_allowed"}),
		constraint("cluster.pipelines.*.steps.*.requirements.max_cost_usd", "number", "minimum", 0, "maximum", 1000000, "finite", true),
	}
}

// ReadManagedConfig returns editable YAML with secret scalar values replaced
// by a reserved keep-existing marker. The revision always hashes the original
// bytes, so clients can perform an optimistic concurrency check on apply.
func ReadManagedConfig(path string) (ManagedConfig, error) {
	raw, err := readManagedConfigFile(path)
	if err != nil {
		return ManagedConfig{}, err
	}
	node, err := decodeManagedYAML(raw)
	if err != nil {
		return ManagedConfig{}, err
	}
	redactManagedSecrets(node)
	redacted, err := yaml.Marshal(node)
	if err != nil {
		return ManagedConfig{}, fmt.Errorf("encode redacted config: %w", err)
	}
	return ManagedConfig{
		Schema: "contextbridge.managed-config.v1", YAML: string(redacted), Revision: managedRevision(raw),
		SecretMarker: ManagedSecretMarker, RestartRequired: false,
	}, nil
}

func ValidateManagedConfig(path string, proposed []byte, baseRevision string) (ManagedValidation, error) {
	managedConfigMu.Lock()
	defer managedConfigMu.Unlock()
	current, materialized, changes, err := prepareManagedConfig(path, proposed, baseRevision)
	if err != nil {
		return ManagedValidation{}, err
	}
	return ManagedValidation{Schema: "contextbridge.config-validation.v1", Valid: true, BaseRevision: managedRevision(current), ProposedRevision: managedRevision(materialized), ChangedSections: changes, RestartRequired: len(changes) > 0}, nil
}

func ApplyManagedConfig(path string, proposed []byte, baseRevision string) (ManagedValidation, error) {
	managedConfigMu.Lock()
	defer managedConfigMu.Unlock()
	current, materialized, changes, err := prepareManagedConfig(path, proposed, baseRevision)
	if err != nil {
		return ManagedValidation{}, err
	}
	if len(changes) == 0 {
		return ManagedValidation{Schema: "contextbridge.config-apply.v1", Valid: true, Revision: managedRevision(current), RestartRequired: false}, nil
	}
	if err := replaceManagedConfig(path, current, materialized); err != nil {
		return ManagedValidation{}, err
	}
	return ManagedValidation{Schema: "contextbridge.config-apply.v1", Valid: true, Revision: managedRevision(materialized), ChangedSections: changes, RestartRequired: true}, nil
}

func prepareManagedConfig(path string, proposed []byte, baseRevision string) ([]byte, []byte, []string, error) {
	if len(proposed) == 0 || int64(len(proposed)) > maximumConfigBytes {
		return nil, nil, nil, fmt.Errorf("proposed config must contain 1 to %d bytes", maximumConfigBytes)
	}
	current, err := readManagedConfigFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	if baseRevision == "" || baseRevision != managedRevision(current) {
		return nil, nil, nil, ErrManagedConfigConflict
	}
	currentNode, err := decodeManagedYAML(current)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse current config: %w", err)
	}
	proposedNode, err := decodeManagedYAML(proposed)
	if err != nil {
		return nil, nil, nil, err
	}
	currentSecrets := map[string]string{}
	visitManagedScalars(currentNode, nil, func(path []string, node *yaml.Node) {
		if managedSecretPath(path) && node.Value != "" {
			currentSecrets[strings.Join(path, "\x00")] = node.Value
		}
	})
	var markerErr error
	visitManagedScalars(proposedNode, nil, func(path []string, node *yaml.Node) {
		if markerErr != nil || !managedSecretPath(path) || node.Value != ManagedSecretMarker {
			return
		}
		secret, ok := currentSecrets[strings.Join(path, "\x00")]
		if !ok {
			markerErr = fmt.Errorf("secret marker at %s has no existing secret", strings.Join(path, "."))
			return
		}
		node.Value = secret
	})
	if markerErr != nil {
		return nil, nil, nil, markerErr
	}
	materialized, err := yaml.Marshal(proposedNode)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode proposed config: %w", err)
	}
	if int64(len(materialized)) > maximumConfigBytes {
		return nil, nil, nil, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	if err := validateManagedMaterializedConfig(current, materialized, filepath.Dir(path)); err != nil {
		return nil, nil, nil, err
	}
	return current, materialized, managedChangedSections(currentNode, proposedNode), nil
}

// validateManagedMaterializedConfig deliberately separates HTTP-managed YAML
// from filesystem secret resolution. Existing file-backed credential paths are
// allowed, but this API cannot add, remove, or redirect them; those changes
// require direct local access to the configuration file. The already trusted
// current config supplies the resolved values needed for complete validation.
func validateManagedMaterializedConfig(current, proposed []byte, configDirectory string) error {
	trusted, err := parseConfig(current, configDirectory)
	if err != nil {
		return fmt.Errorf("parse current config: %w", err)
	}
	candidate, err := decodeConfig(proposed, configDirectory)
	if err != nil {
		return err
	}
	for name, engine := range candidate.Engines {
		trustedEngine := trusted.Engines[name]
		if strings.TrimSpace(engine.APIKeyFile) != strings.TrimSpace(trustedEngine.APIKeyFile) {
			return fmt.Errorf("engine %s api_key_file cannot be changed through the managed API; edit the local config file directly", name)
		}
		if strings.TrimSpace(engine.APIKeyFile) != "" {
			engine.ResolvedAPIKey = trustedEngine.ResolvedAPIKey
			candidate.Engines[name] = engine
		}
	}
	for id, principal := range candidate.Providers.Adapter.Principals {
		trustedPrincipal := trusted.Providers.Adapter.Principals[id]
		if strings.TrimSpace(principal.TokenFile) != strings.TrimSpace(trustedPrincipal.TokenFile) {
			return fmt.Errorf("adapter principal %s token_file cannot be changed through the managed API; edit the local config file directly", id)
		}
		if strings.TrimSpace(principal.TokenFile) != "" {
			principal.ResolvedToken = trustedPrincipal.ResolvedToken
			candidate.Providers.Adapter.Principals[id] = principal
		}
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	return nil
}

var ErrManagedConfigConflict = errors.New("config revision changed; reload before applying")

func readManagedConfigFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("managed config must be a regular non-symlink file")
	}
	return readConfigFile(path)
}

func decodeManagedYAML(raw []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return nil, errors.New("parse config: multiple YAML documents are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("parse config: top-level value must be a mapping")
	}
	if hasManagedAlias(&root) {
		return nil, errors.New("managed config does not accept YAML aliases")
	}
	return &root, nil
}

func hasManagedAlias(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if hasManagedAlias(child) {
			return true
		}
	}
	return false
}

func visitManagedScalars(node *yaml.Node, path []string, visit func([]string, *yaml.Node)) {
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		visitManagedScalars(node.Content[0], path, visit)
		return
	}
	if node.Kind != yaml.MappingNode {
		return
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		childPath := append(append([]string(nil), path...), key.Value)
		if value.Kind == yaml.ScalarNode {
			visit(childPath, value)
		} else {
			visitManagedScalars(value, childPath, visit)
		}
	}
}

func managedSecretPath(path []string) bool {
	joined := strings.Join(path, ".")
	switch joined {
	case "server.token", "cluster.client_token", "cluster.relay.admin_token", "cluster.worker.local_token":
		return true
	}
	return len(path) == 3 && path[0] == "engines" && path[2] == "api_key" ||
		len(path) == 4 && path[0] == "cluster" && path[1] == "accounts" && path[3] == "client_token" ||
		len(path) == 5 && path[0] == "providers" && path[1] == "adapter" && path[2] == "principals" && path[4] == "token"
}

func redactManagedSecrets(node *yaml.Node) {
	visitManagedScalars(node, nil, func(path []string, node *yaml.Node) {
		if managedSecretPath(path) && node.Value != "" {
			node.Tag = "!!str"
			node.Value = ManagedSecretMarker
		}
	})
}

func managedChangedSections(current, proposed *yaml.Node) []string {
	currentSections := managedTopLevelSections(current)
	proposedSections := managedTopLevelSections(proposed)
	keys := map[string]struct{}{}
	for key := range currentSections {
		keys[key] = struct{}{}
	}
	for key := range proposedSections {
		keys[key] = struct{}{}
	}
	changed := []string{}
	for key := range keys {
		if currentSections[key] != proposedSections[key] {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

func managedTopLevelSections(root *yaml.Node) map[string]string {
	sections := map[string]string{}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return sections
	}
	mapping := root.Content[0]
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		raw, _ := yaml.Marshal(mapping.Content[index+1])
		sections[mapping.Content[index].Value] = string(raw)
	}
	return sections
}

func managedRevision(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func replaceManagedConfig(path string, expected, replacement []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".contextbridge-config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(replacement); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	latest, err := readManagedConfigFile(path)
	if err != nil {
		return err
	}
	if managedRevision(latest) != managedRevision(expected) {
		return ErrManagedConfigConflict
	}
	if err := replaceConfigFile(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
