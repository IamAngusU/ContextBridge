package cluster

import (
	"encoding/json"
	"time"
)

func nonNegativeDurationMilliseconds(value time.Duration) uint64 {
	if value <= 0 {
		return 0
	}
	// time.Duration is a signed int64 nanosecond count. Dividing a positive
	// duration first guarantees the result is non-negative and far below the
	// uint64 ceiling before conversion.
	return uint64(value / time.Millisecond) // #nosec G115 -- value is positive and time.Duration cannot exceed int64.
}

// ProtocolVersion 3 makes relay authority and assignment fences mandatory on
// the worker wire. Mixed v2/v3 peers fail the existing exact-version handshake
// instead of silently running without stale-leader protection.
const ProtocolVersion = 3

// JobContractV1 names the stable producer-to-relay job boundary. An omitted
// version is normalized to V1 for clients written before contracts became
// explicit; any other non-empty value is rejected instead of guessed.
const JobContractV1 = "contextbridge.job.v1"

// MaximumWorkerConcurrency is the protocol-level upper bound for slots a
// worker may advertise. Local configuration already rejects larger values;
// the relay must apply the same bound to untrusted hello and heartbeat frames.
const MaximumWorkerConcurrency = 64

// Adapter capability inventories are routing evidence, not a copy of provider
// payloads. Keep the per-endpoint lists deliberately small so a compromised local
// status endpoint or worker cannot turn heartbeats into an unbounded protocol
// payload.
const (
	MaximumAdapterSessions        = 256
	MaximumAdapterModelChoices    = 20
	MaximumAdapterReasoningLevels = 20
	MaximumAdapterChoiceBytes     = 100
	MaximumAdapterSessionKeyBytes = 80
	MaximumGPUCapabilities        = 256
	MaximumModelCapabilities      = 4096
	MaximumNodeListValues         = 256
	MaximumNodeHardwareBytes      = uint64(1 << 60)
)

// MaximumJobPayloadBytes is the largest cleartext job payload supported by
// every relay/worker transport path. Encryption adds wire overhead but must not
// reduce this usable budget.
const MaximumJobPayloadBytes int64 = 12 << 20

// MaximumJobResultBytes bounds the cleartext JSON result accepted from a
// worker. MaximumJobResultWireBytes additionally covers AES-GCM and RawURL
// base64 expansion for an encrypted result plus its small protocol envelope.
// Keep client response readers and the relay websocket read limit aligned with
// these constants.
const MaximumJobResultBytes int64 = 24 << 20
const MaximumJobResultWireBytes int64 = ((MaximumJobResultBytes+16)*4+2)/3 + (64 << 10)

const (
	JobReserved  = "reserved"
	JobQueued    = "queued"
	JobAssigned  = "assigned"
	JobRunning   = "running"
	JobCompleted = "completed"
	JobFailed    = "failed"
	JobCancelled = "cancelled"

	CostUnknown    = "unknown"
	CostEstimated  = "estimated"
	CostUpperBound = "upper_bound"
	CostActual     = "actual"
	CostPartial    = "partial"
)

type Requirements struct {
	Task           string `json:"task,omitempty" yaml:"task,omitempty"`
	SessionID      string `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	Provider       string `json:"provider,omitempty" yaml:"provider,omitempty"`
	AdapterProfile string `json:"adapter_profile,omitempty" yaml:"adapter_profile,omitempty"`
	Model          string `json:"model,omitempty" yaml:"model,omitempty"`
	Reasoning      string `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	// AdapterEndpointID is selected by the relay from fresh worker telemetry. A
	// producer cannot set it directly; once assigned it is authenticated by the
	// E2EE context and carried to the local adapter lease boundary.
	AdapterEndpointID int `json:"adapter_endpoint_id,omitempty" yaml:"-"`
	// AdapterPrincipal is selected exclusively from authenticated worker
	// telemetry and is never accepted from producer input.
	AdapterPrincipal string `json:"adapter_principal,omitempty" yaml:"-"`
	// AdapterSessionRecovery is relay-internal. It allows a worker on the same
	// node to prove that a durable session moved to another adapter endpoint
	// without allowing the session to migrate to another machine.
	AdapterSessionRecovery bool `json:"adapter_session_recovery,omitempty" yaml:"-"`
	// AdapterSessionKey is an opaque relay-derived selector used only while
	// ranking fresh adapter telemetry. It is never accepted from or serialized
	// back to producers and contains no raw session name, locator, or provider content.
	AdapterSessionKey string `json:"-" yaml:"-"`
	// AdapterFreshSession asks the selected adapter worker to create a new provider
	// endpoint session when this logical session does not already have a binding.
	AdapterFreshSession bool `json:"adapter_fresh_session,omitempty" yaml:"adapter_fresh_session,omitempty"`
	// AdapterEphemeralSession creates a fresh endpoint session for this one job. Its
	// completed endpoint must never become durable session affinity.
	AdapterEphemeralSession bool     `json:"adapter_ephemeral_session,omitempty" yaml:"adapter_ephemeral_session,omitempty"`
	Group                   string   `json:"group,omitempty" yaml:"group,omitempty"`
	RequiredTags            []string `json:"required_tags,omitempty" yaml:"required_tags,omitempty"`
	PreferredNodes          []string `json:"preferred_nodes,omitempty" yaml:"preferred_nodes,omitempty"`
	MinFreeVRAM             uint64   `json:"min_free_vram_bytes,omitempty" yaml:"min_free_vram_bytes,omitempty"`
	Vision                  bool     `json:"vision,omitempty" yaml:"vision,omitempty"`
	Embedding               bool     `json:"embedding,omitempty" yaml:"embedding,omitempty"`
	// InputImageCount lets the scheduler enforce a model's known hard image
	// limit before dispatch. Zero means no image input, never "unknown".
	InputImageCount      int      `json:"input_image_count,omitempty" yaml:"input_image_count,omitempty"`
	InputImageBytes      int64    `json:"input_image_bytes,omitempty" yaml:"input_image_bytes,omitempty"`
	InputImageMaxBytes   int64    `json:"input_image_max_bytes,omitempty" yaml:"input_image_max_bytes,omitempty"`
	InputImageMediaTypes []string `json:"input_image_media_types,omitempty" yaml:"input_image_media_types,omitempty"`
	// Egress is an authenticated producer constraint. local_only fails closed
	// unless the relay classifies the explicit provider as local;
	// remote_allowed does not override a stricter operator policy.
	Egress string `json:"egress,omitempty" yaml:"egress,omitempty"`
	// MaxCostUSD is a hard upper-bound request, not a cost estimate. Remote
	// workers may execute it only when their selected engine can reserve and
	// enforce an operator-reviewed cost ceiling. Unknown cost is never $0.
	MaxCostUSD float64 `json:"max_cost_usd,omitempty" yaml:"max_cost_usd,omitempty"`
}

type GPUCapability struct {
	Name        string `json:"name"`
	Backend     string `json:"backend"`
	Driver      string `json:"driver,omitempty"`
	MemoryTotal uint64 `json:"memory_total_bytes"`
	MemoryFree  uint64 `json:"memory_free_bytes"`
	Temperature int    `json:"temperature_c,omitempty"`
	Utilization int    `json:"utilization_percent,omitempty"`
}

type ModelCapability struct {
	Name                 string   `json:"name"`
	Tasks                []string `json:"tasks,omitempty"`
	Size                 int64    `json:"size_bytes,omitempty"`
	Available            bool     `json:"available"`
	Loaded               bool     `json:"loaded"`
	Vision               bool     `json:"vision"`
	Embedding            bool     `json:"embedding"`
	CapabilitiesVerified bool     `json:"capabilities_verified"`
	CapabilitySource     string   `json:"capability_source,omitempty"`
	ContextWindowTokens  int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens      int      `json:"max_output_tokens,omitempty"`
	MaxInputImages       int      `json:"max_input_images,omitempty"`
	MaxImageBytes        int64    `json:"max_image_bytes,omitempty"`
	MaxTotalImageBytes   int64    `json:"max_total_image_bytes,omitempty"`
	ImageMediaTypes      []string `json:"image_media_types,omitempty"`
	LimitsVerified       bool     `json:"limits_verified"`
	LimitSource          string   `json:"limit_source,omitempty"`
	VRAM                 int64    `json:"vram_bytes,omitempty"`
	Provider             string   `json:"provider,omitempty"`
}

// AdapterSessionCapability reports only the bounded routing selection and state
// of an explicitly registered endpoint. Provider content and display labels
// never leave the node.
type AdapterSessionCapability struct {
	EndpointID          int      `json:"endpoint_id"`
	Profile             string   `json:"profile,omitempty"`
	Principal           string   `json:"principal,omitempty"`
	State               string   `json:"state,omitempty"`
	SessionKey          string   `json:"session_key,omitempty"`
	SessionKeySupported bool     `json:"session_key_supported,omitempty"`
	CanCreateSession    bool     `json:"can_create_session,omitempty"`
	DefaultNewSession   bool     `json:"default_new_session,omitempty"`
	CurrentModel        string   `json:"current_model,omitempty"`
	CurrentReasoning    string   `json:"current_reasoning,omitempty"`
	ModelChoices        []string `json:"model_choices,omitempty"`
	ReasoningLevels     []string `json:"reasoning_levels,omitempty"`
}

type Capabilities struct {
	ClockTime        time.Time         `json:"clock_time,omitempty"`
	UTCOffsetSeconds int               `json:"utc_offset_seconds,omitempty"`
	OS               string            `json:"os"`
	OSVersion        string            `json:"os_version,omitempty"`
	Architecture     string            `json:"architecture"`
	CPU              string            `json:"cpu"`
	CPUCores         int               `json:"cpu_cores"`
	CPUFrequency     int               `json:"cpu_frequency_mhz,omitempty"`
	CPUUtilization   int               `json:"cpu_utilization_percent,omitempty"`
	UptimeSeconds    uint64            `json:"uptime_seconds,omitempty"`
	AgentVersion     string            `json:"agent_version,omitempty"`
	MemoryTotal      uint64            `json:"memory_total_bytes"`
	MemoryFree       uint64            `json:"memory_free_bytes"`
	MemoryType       string            `json:"memory_type,omitempty"`
	GPUs             []GPUCapability   `json:"gpus,omitempty"`
	Models           []ModelCapability `json:"models,omitempty"`
	Providers        []string          `json:"providers,omitempty"`
	Tasks            []string          `json:"tasks,omitempty"`
	// AutomaticTasks reports provider-scoped tasks that the worker's configured
	// route can execute without a producer selecting a concrete model. A nil map
	// identifies an older worker; a present map is authoritative even when a
	// provider has no automatically routable task.
	AutomaticTasks   map[string][]string        `json:"automatic_tasks_by_provider"`
	Tags             []string                   `json:"tags,omitempty"`
	Groups           []string                   `json:"groups,omitempty"`
	MaxConcurrent    int                        `json:"max_concurrent"`
	Running          int                        `json:"running"`
	AdapterEndpoints int                        `json:"adapter_endpoints,omitempty"`
	AdapterBusy      int                        `json:"adapter_busy_endpoints,omitempty"`
	AdapterSessions  []AdapterSessionCapability `json:"adapter_sessions,omitempty"`
	Sources          []string                   `json:"sources,omitempty"`
	Modes            []string                   `json:"modes,omitempty"`
	QueueDepth       int                        `json:"queue_depth"`
}

type Node struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	PublicKey    string       `json:"public_key,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	State        string       `json:"state"`
	Connected    bool         `json:"connected"`
	// Draining is relay-owned operator state. A draining worker stays connected
	// and may finish work it already owns, but it is never eligible for a new
	// assignment until an administrator resumes it.
	Draining        bool      `json:"draining,omitempty"`
	LastSeen        time.Time `json:"last_seen"`
	ClockOffsetMS   int64     `json:"clock_offset_ms,omitempty"`
	ConnectedAt     time.Time `json:"connected_at,omitempty"`
	JobsTotal       uint64    `json:"jobs_total"`
	JobsFailed      uint64    `json:"jobs_failed"`
	ComputeMS       uint64    `json:"compute_ms"`
	CostUSD         float64   `json:"cost_usd"`
	CostKnownJobs   uint64    `json:"cost_known_jobs,omitempty"`
	CostUnknownJobs uint64    `json:"cost_unknown_jobs,omitempty"`
	// RoutingHealth is bounded relay-owned evidence about recent transient
	// execution failures. Workers cannot set or clear it through heartbeats.
	RoutingHealth []RoutingHealth `json:"routing_health,omitempty"`
	// RoutingPerformance is bounded relay-owned evidence derived only from
	// successfully completed, fenced jobs. Workers cannot advertise a faster
	// historical estimate through heartbeats.
	RoutingPerformance []RoutingPerformance `json:"routing_performance,omitempty"`
}

// RoutingPerformance keeps a bounded smoothed execution-time estimate for one
// concrete route on one node. RouteKey has the same opaque identity as routing
// health, so adapter profile/session constraints cannot contaminate an
// independent route and no raw session identifier is persisted here.
type RoutingPerformance struct {
	RouteKey        string    `json:"route_key"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	Samples         uint32    `json:"samples"`
	EWMAComputeMS   uint64    `json:"ewma_compute_ms"`
	LastComputeMS   uint64    `json:"last_compute_ms"`
	LastCompletedAt time.Time `json:"last_completed_at"`
	// RecentSuccessMS is a bounded relay-owned distribution of successful
	// execution durations. It supports transparent user-facing historical
	// estimates without retaining prompt/result content or an unbounded log.
	RecentSuccessMS []uint64 `json:"recent_success_ms,omitempty"`
	// LoadProfiles keep a small set of relay-derived performance curves for
	// the live load class observed at assignment. The overall fields above
	// remain the neutral fallback when a class has too little fresh evidence.
	LoadProfiles []RoutingLoadPerformance `json:"load_profiles,omitempty"`
}

// RoutingLoadPerformance is a bounded point on a route's explainable load
// curve. ContextClass is generated by the relay from bounded slot, queue,
// CPU/GPU, RAM/VRAM and model-warmth evidence; it is never free-form input.
type RoutingLoadPerformance struct {
	ContextClass    string    `json:"context_class"`
	Samples         uint32    `json:"samples"`
	EWMAComputeMS   uint64    `json:"ewma_compute_ms"`
	LastComputeMS   uint64    `json:"last_compute_ms"`
	LastCompletedAt time.Time `json:"last_completed_at"`
	RecentSuccessMS []uint64  `json:"recent_success_ms,omitempty"`
}

// RoutingHealth is keyed by an opaque producer scope plus an opaque execution
// route fingerprint. Provider and model remain only as bounded operator-facing
// diagnostics. An empty owner scope is reserved for relay-observed node-wide
// failures. It never contains provider error text, prompts, tenant identifiers,
// session identifiers, or other unbounded worker-controlled data.
type RoutingHealth struct {
	OwnerScope string `json:"owner_scope,omitempty"`
	// RouteKey is an opaque relay-derived fingerprint of the execution route.
	// It keeps adapter profiles, reasoning modes, endpoint/session constraints,
	// and task kinds independent without persisting their potentially sensitive
	// or attacker-controlled raw values in relay health state.
	RouteKey            string    `json:"route_key"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model"`
	ConsecutiveFailures uint32    `json:"consecutive_failures"`
	LastFailureCode     string    `json:"last_failure_code,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitempty"`
	CircuitOpenUntil    time.Time `json:"circuit_open_until,omitempty"`
	// ProbeJobID is a durable relay-owned single-flight lease while a route is
	// in recovery probation. ProbeOwnerScope identifies which producer may
	// resolve a node-wide probe without exposing the producer identity.
	ProbeJobID      string `json:"probe_job_id,omitempty"`
	ProbeOwnerScope string `json:"probe_owner_scope,omitempty"`
}

type Usage struct {
	InputTokens       uint64  `json:"input_tokens,omitempty"`
	OutputTokens      uint64  `json:"output_tokens,omitempty"`
	TotalTokens       uint64  `json:"total_tokens,omitempty"`
	ComputeMS         uint64  `json:"compute_ms,omitempty"`
	QueueMS           uint64  `json:"queue_ms,omitempty"`
	CostStatus        string  `json:"cost_status"`
	CostSource        string  `json:"cost_source,omitempty"`
	ReservedCostUSD   float64 `json:"reserved_cost_usd,omitempty"`
	EstimatedCostUSD  float64 `json:"estimated_cost_usd,omitempty"`
	EquivalentCostUSD float64 `json:"equivalent_cloud_cost_usd,omitempty"`
	SavedCostUSD      float64 `json:"saved_cost_usd,omitempty"`
	CostKnownJobs     uint64  `json:"cost_known_jobs,omitempty"`
	CostUnknownJobs   uint64  `json:"cost_unknown_jobs,omitempty"`
	// Peak resource values are attributable measurements reported by the
	// execution engine. Node-wide hardware snapshots must not populate them or
	// set ResourceScope to "job".
	ResourceScope      string `json:"resource_scope,omitempty"`
	PeakVRAMBytes      uint64 `json:"peak_vram_bytes,omitempty"`
	PeakRAMBytes       uint64 `json:"peak_ram_bytes,omitempty"`
	PeakGPUUtilization int    `json:"peak_gpu_utilization_percent,omitempty"`
}

// ExecutionMetadata is bounded worker-reported routing evidence. Adapter endpoint
// identifiers are local to a worker and are used only to keep later turns of
// the same logical session on the endpoint session that actually ran the job.
type ExecutionMetadata struct {
	AdapterEndpointID        int  `json:"adapter_endpoint_id,omitempty"`
	EphemeralAdapterEndpoint bool `json:"ephemeral_adapter_endpoint,omitempty"`
}

type JobProgress struct {
	Sequence  uint64    `json:"sequence"`
	Text      string    `json:"text"`
	Phase     string    `json:"phase,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Percent   int       `json:"percent,omitempty"`
	Busy      bool      `json:"busy"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SealedEnvelope struct {
	Algorithm       string `json:"algorithm"`
	EphemeralPublic string `json:"ephemeral_public"`
	Nonce           string `json:"nonce"`
	Ciphertext      string `json:"ciphertext"`
}

// RelayAuthority identifies one durable relay store and one monotonically
// increasing process epoch within that store. It is intentionally independent
// from network addresses so proxy and DNS changes do not create a new cluster.
type RelayAuthority struct {
	ClusterID string `json:"cluster_id"`
	Epoch     uint64 `json:"epoch"`
}

func (authority RelayAuthority) Valid() bool {
	return validRoutingLabel(authority.ClusterID, 120) && authority.Epoch > 0
}

// AssignmentFence binds every worker-side effect and reply to the relay
// authority and exact durable assignment generation that created it.
type AssignmentFence struct {
	ClusterID  string `json:"cluster_id"`
	RelayEpoch uint64 `json:"relay_epoch"`
	Generation uint64 `json:"generation"`
}

func (fence AssignmentFence) Valid() bool {
	return validRoutingLabel(fence.ClusterID, 120) && fence.RelayEpoch > 0 && fence.Generation > 0
}

func (fence AssignmentFence) Equal(other AssignmentFence) bool {
	return fence == other
}

type Job struct {
	ID              string           `json:"id"`
	ContractVersion string           `json:"contract_version"`
	OwnerSubject    string           `json:"owner_subject,omitempty"`
	TenantID        string           `json:"tenant_id,omitempty"`
	Source          string           `json:"source,omitempty"`
	Pipeline        string           `json:"pipeline,omitempty"`
	Step            string           `json:"step,omitempty"`
	ParentID        string           `json:"parent_id,omitempty"`
	Requirements    Requirements     `json:"requirements"`
	PolicyDecision  PolicyDecision   `json:"policy_decision"`
	Payload         json.RawMessage  `json:"payload,omitempty"`
	SealedPayload   *SealedEnvelope  `json:"sealed_payload,omitempty"`
	Result          json.RawMessage  `json:"result,omitempty"`
	SealedResult    *SealedEnvelope  `json:"sealed_result,omitempty"`
	Status          string           `json:"status"`
	Priority        int              `json:"priority"`
	Attempt         int              `json:"attempt"`
	MaxAttempts     int              `json:"max_attempts"`
	AssignedNode    string           `json:"assigned_node,omitempty"`
	AssignmentFence *AssignmentFence `json:"assignment_fence,omitempty"`
	// RoutingDecision is the bounded, point-in-time evidence used for the
	// durable assignment. It intentionally excludes full node telemetry and is
	// absent until a queued job is actually placed.
	RoutingDecision *RoutingDecision `json:"routing_decision,omitempty"`
	// ExecutedAdapterEndpointID records the concrete adapter endpoint used by the worker.
	// It can differ from Requirements.AdapterEndpointID when the adapter created a
	// fresh session for the first turn of a session.
	ExecutedAdapterEndpointID int          `json:"executed_adapter_endpoint_id,omitempty"`
	EphemeralAdapterEndpoint  bool         `json:"ephemeral_adapter_endpoint,omitempty"`
	Error                     string       `json:"error,omitempty"`
	FailureCode               string       `json:"failure_code,omitempty"`
	Usage                     Usage        `json:"usage"`
	Progress                  *JobProgress `json:"progress,omitempty"`
	CreatedAt                 time.Time    `json:"created_at"`
	UpdatedAt                 time.Time    `json:"updated_at"`
	AssignedAt                time.Time    `json:"assigned_at,omitempty"`
	StartedAt                 time.Time    `json:"started_at,omitempty"`
	FinishedAt                time.Time    `json:"finished_at,omitempty"`
	ReservationKey            string       `json:"-"`
}

type SubmitRequest struct {
	ID               string          `json:"id,omitempty"`
	ContractVersion  string          `json:"contract_version,omitempty"`
	TenantID         string          `json:"tenant_id,omitempty"`
	Source           string          `json:"source,omitempty"`
	Requirements     Requirements    `json:"requirements"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	Sealed           *SealedEnvelope `json:"sealed_payload,omitempty"`
	AssignmentID     string          `json:"assignment_id,omitempty"`
	AssignmentSecret string          `json:"assignment_secret,omitempty"`
	Priority         int             `json:"priority,omitempty"`
	MaxAttempts      int             `json:"max_attempts,omitempty"`
	OwnerSubject     string          `json:"-"`
	PolicyDecision   PolicyDecision  `json:"-"`
	// Pipeline metadata is relay-internal and is persisted atomically with the
	// queued job. Keeping it out of the producer JSON surface prevents callers
	// from forging orchestration ownership while avoiding a post-admission
	// SaveJob checkpoint that could fail after the job became dispatchable.
	Pipeline string `json:"-"`
	Step     string `json:"-"`
	ParentID string `json:"-"`
}

// ContractValidation is deliberately content-minimizing. It proves that the
// authenticated relay accepted the same normalized admission boundary used by
// a real submission without echoing prompts, attachments, session names, or
// routing selectors.
type ContractValidation struct {
	ContractVersion string         `json:"contract_version"`
	Valid           bool           `json:"valid"`
	PayloadMode     string         `json:"payload_mode"`
	MaxAttempts     int            `json:"max_attempts"`
	PolicyDecision  PolicyDecision `json:"policy_decision"`
}

type Assignment struct {
	ID             string         `json:"id"`
	JobID          string         `json:"job_id"`
	NodeID         string         `json:"node_id"`
	NodeName       string         `json:"node_name"`
	PublicKey      string         `json:"public_key"`
	Attempt        int            `json:"attempt"`
	OwnerSubject   string         `json:"owner_subject"`
	TenantID       string         `json:"tenant_id,omitempty"`
	ExpiresAt      time.Time      `json:"expires_at"`
	Requirements   Requirements   `json:"requirements"`
	PolicyDecision PolicyDecision `json:"policy_decision"`
}

type AssignmentResponse struct {
	Assignment Assignment `json:"assignment"`
	Secret     string     `json:"assignment_secret"`
}

type Pairing struct {
	DeviceCodeHash       string    `json:"device_code_hash"`
	UserCode             string    `json:"user_code"`
	NodeName             string    `json:"node_name"`
	PublicKey            string    `json:"public_key"`
	PublicKeyFingerprint string    `json:"public_key_fingerprint,omitempty"`
	Groups               []string  `json:"groups,omitempty"`
	ExpiresAt            time.Time `json:"expires_at"`
	Approved             bool      `json:"approved"`
	Denied               bool      `json:"denied"`
	NodeID               string    `json:"node_id,omitempty"`
	PendingToken         string    `json:"pending_token,omitempty"`
}

type PairRequest struct {
	NodeName  string   `json:"node_name"`
	PublicKey string   `json:"public_key"`
	Groups    []string `json:"groups,omitempty"`
}

type PairResponse struct {
	DeviceCode      string    `json:"device_code"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	ExpiresAt       time.Time `json:"expires_at"`
	IntervalSeconds int       `json:"interval_seconds"`
}

// ProducerLimits are durable, admin-issued boundaries attached to one
// producer credential. Zero values retain the relay defaults. They are kept
// deliberately small and enforceable at admission time; usage- or cost-based
// budgets belong here only once the relay can reserve and prove that usage.
type ProducerLimits struct {
	MaxQueuedJobs  int      `json:"max_queued_jobs,omitempty"`
	MaxJobsPerHour int      `json:"max_jobs_per_hour,omitempty"`
	Providers      []string `json:"providers,omitempty"`
	Egress         string   `json:"egress,omitempty"`
}

type TokenRecord struct {
	ID             string         `json:"id"`
	Role           string         `json:"role"`
	Subject        string         `json:"subject"`
	Groups         []string       `json:"groups,omitempty"`
	ProducerLimits ProducerLimits `json:"producer_limits,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at,omitempty"`
	Revoked        bool           `json:"revoked"`
}

type Event struct {
	ID      string                 `json:"id"`
	Time    time.Time              `json:"time"`
	Kind    string                 `json:"kind"`
	Message string                 `json:"message"`
	JobID   string                 `json:"job_id,omitempty"`
	NodeID  string                 `json:"node_id,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

const JobEventSchemaV1 = "contextbridge.event.v1"

// JobEvent is a bounded, per-job execution event. Relay-authored lifecycle
// events are authoritative because they commit in the same Bolt transaction as
// the corresponding Job state. Worker/tool detail must use authority=advisory
// and can never create terminal job state.
type JobEvent struct {
	Schema    string            `json:"schema"`
	JobID     string            `json:"job_id,omitempty"`
	RunID     string            `json:"run_id,omitempty"`
	Sequence  uint64            `json:"seq"`
	Type      string            `json:"type"`
	Source    string            `json:"source"`
	Authority string            `json:"authority"`
	Time      time.Time         `json:"ts"`
	Attempt   int               `json:"attempt,omitempty"`
	NodeID    string            `json:"node_id,omitempty"`
	StepID    string            `json:"step_id,omitempty"`
	Progress  *JobEventProgress `json:"progress,omitempty"`
}

// JobEventProgress contains only coordination metadata. Worker-supplied text
// and detail are deliberately excluded from the event plane so reconnect and
// observability do not become a second content-retention channel.
type JobEventProgress struct {
	ReportedSequence uint64 `json:"reported_seq"`
	Phase            string `json:"phase,omitempty"`
	Percent          int    `json:"percent,omitempty"`
	Busy             bool   `json:"busy,omitempty"`
}

type JobEventPage struct {
	Events         []JobEvent `json:"events"`
	After          uint64     `json:"after"`
	Next           uint64     `json:"next"`
	OldestRetained uint64     `json:"oldest_retained,omitempty"`
	Newest         uint64     `json:"newest,omitempty"`
	Gap            bool       `json:"gap"`
}

type WireMessage struct {
	Version      int                `json:"version"`
	Type         string             `json:"type"`
	RequestID    string             `json:"request_id,omitempty"`
	Node         *Node              `json:"node,omitempty"`
	Capabilities *Capabilities      `json:"capabilities,omitempty"`
	Authority    *RelayAuthority    `json:"authority,omitempty"`
	Job          *Job               `json:"job,omitempty"`
	JobID        string             `json:"job_id,omitempty"`
	Attempt      int                `json:"attempt,omitempty"`
	Fence        *AssignmentFence   `json:"assignment_fence,omitempty"`
	Result       json.RawMessage    `json:"result,omitempty"`
	SealedResult *SealedEnvelope    `json:"sealed_result,omitempty"`
	Usage        Usage              `json:"usage,omitempty"`
	Execution    *ExecutionMetadata `json:"execution,omitempty"`
	Progress     *JobProgress       `json:"progress,omitempty"`
	Error        string             `json:"error,omitempty"`
	FailureCode  string             `json:"failure_code,omitempty"`
}

type Pricing struct {
	Mode                string  `json:"mode,omitempty" yaml:"mode,omitempty"`
	Source              string  `json:"source,omitempty" yaml:"source,omitempty"`
	ComputePerHourUSD   float64 `json:"compute_per_hour_usd" yaml:"compute_per_hour_usd"`
	InputPerMillionUSD  float64 `json:"input_per_million_usd" yaml:"input_per_million_usd"`
	OutputPerMillionUSD float64 `json:"output_per_million_usd" yaml:"output_per_million_usd"`
	EquivalentInputUSD  float64 `json:"equivalent_input_per_million_usd" yaml:"equivalent_input_per_million_usd"`
	EquivalentOutputUSD float64 `json:"equivalent_output_per_million_usd" yaml:"equivalent_output_per_million_usd"`
}

type Pipeline struct {
	Mode              string         `json:"mode,omitempty" yaml:"mode,omitempty"`
	MaxParallel       int            `json:"max_parallel,omitempty" yaml:"max_parallel,omitempty"`
	MaxRuntimeSeconds int            `json:"max_runtime_seconds" yaml:"max_runtime_seconds"`
	MaxIterations     int            `json:"max_iterations" yaml:"max_iterations"`
	TenantID          string         `json:"tenant_id,omitempty" yaml:"tenant_id,omitempty"`
	Steps             []PipelineStep `json:"steps" yaml:"steps"`
}

type PipelineStep struct {
	Name           string       `json:"name" yaml:"name"`
	DependsOn      []string     `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	Route          string       `json:"route,omitempty" yaml:"route,omitempty"`
	Requirements   Requirements `json:"requirements" yaml:"requirements"`
	Input          string       `json:"input" yaml:"input"`
	OutputMode     string       `json:"output_mode,omitempty" yaml:"output_mode,omitempty"`
	Retries        int          `json:"retries,omitempty" yaml:"retries,omitempty"`
	TimeoutSeconds int          `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
	ContinuePath   string       `json:"continue_path,omitempty" yaml:"continue_path,omitempty"`
	ContinueEquals string       `json:"continue_equals,omitempty" yaml:"continue_equals,omitempty"`
	MaxIterations  int          `json:"max_iterations,omitempty" yaml:"max_iterations,omitempty"`
}

type PipelineGraphBinding struct {
	ContractVersion string   `json:"contract_version"`
	Mode            string   `json:"mode"`
	ConfigSHA256    string   `json:"config_sha256"`
	GraphSHA256     string   `json:"graph_sha256"`
	MaxParallel     int      `json:"max_parallel"`
	StepCount       int      `json:"step_count"`
	EdgeCount       int      `json:"edge_count"`
	Order           []string `json:"topological_order"`
}

type PipelineNodeCheckpoint struct {
	Step      string    `json:"step"`
	DependsOn []string  `json:"depends_on,omitempty"`
	State     string    `json:"state"`
	JobID     string    `json:"job_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PipelineRun struct {
	ID             string                   `json:"id"`
	Pipeline       string                   `json:"pipeline"`
	OwnerSubject   string                   `json:"owner_subject,omitempty"`
	TenantID       string                   `json:"tenant_id,omitempty"`
	ProducerLimits ProducerLimits           `json:"producer_limits,omitempty"`
	Status         string                   `json:"status"`
	Input          json.RawMessage          `json:"input"`
	Output         json.RawMessage          `json:"output,omitempty"`
	Steps          []Job                    `json:"steps"`
	Usage          Usage                    `json:"usage"`
	Error          string                   `json:"error,omitempty"`
	Graph          *PipelineGraphBinding    `json:"graph,omitempty"`
	NodeStates     []PipelineNodeCheckpoint `json:"node_states,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	FinishedAt     time.Time                `json:"finished_at,omitempty"`
}

type Overview struct {
	NodesOnline      int               `json:"nodes_online"`
	NodesTotal       int               `json:"nodes_total"`
	JobsByState      map[string]uint64 `json:"jobs_by_state"`
	Usage            Usage             `json:"usage"`
	GeneratedAt      time.Time         `json:"generated_at"`
	UTCOffsetSeconds int               `json:"utc_offset_seconds"`
}
