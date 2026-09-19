package cluster

import (
	"encoding/json"
	"time"
)

const ProtocolVersion = 2

// MaximumWorkerConcurrency is the protocol-level upper bound for slots a
// worker may advertise. Local configuration already rejects larger values;
// the relay must apply the same bound to untrusted hello and heartbeat frames.
const MaximumWorkerConcurrency = 64

// Browser capability inventories are routing evidence, not a copy of page
// contents. Keep the per-tab lists deliberately small so a compromised local
// status endpoint or worker cannot turn heartbeats into an unbounded protocol
// payload.
const (
	MaximumBrowserSessions        = 256
	MaximumBrowserModelChoices    = 20
	MaximumBrowserReasoningLevels = 20
	MaximumBrowserChoiceBytes     = 100
	MaximumBrowserSessionKeyBytes = 80
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
)

type Requirements struct {
	Task           string `json:"task,omitempty" yaml:"task,omitempty"`
	SessionID      string `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	Provider       string `json:"provider,omitempty" yaml:"provider,omitempty"`
	BrowserProfile string `json:"browser_profile,omitempty" yaml:"browser_profile,omitempty"`
	Model          string `json:"model,omitempty" yaml:"model,omitempty"`
	Reasoning      string `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	// BrowserTabID is selected by the relay from fresh worker telemetry. A
	// producer cannot set it directly; once assigned it is authenticated by the
	// E2EE context and carried to the local browser lease boundary.
	BrowserTabID int `json:"browser_tab_id,omitempty" yaml:"-"`
	// BrowserSessionRecovery is relay-internal. It allows a worker on the same
	// node to prove that a saved conversation moved to another attached tab
	// without allowing the session to migrate to another machine.
	BrowserSessionRecovery bool `json:"browser_session_recovery,omitempty" yaml:"-"`
	// BrowserSessionKey is an opaque relay-derived selector used only while
	// ranking fresh browser telemetry. It is never accepted from or serialized
	// back to producers and contains no raw session name, URL, or page content.
	BrowserSessionKey string `json:"-" yaml:"-"`
	// BrowserFreshChat asks the selected browser worker to create a new provider
	// conversation when this logical session does not already have a binding.
	BrowserFreshChat bool `json:"browser_fresh_chat,omitempty" yaml:"browser_fresh_chat,omitempty"`
	// BrowserEphemeralChat creates a fresh conversation for this one job. Its
	// completed tab must never become durable session affinity.
	BrowserEphemeralChat bool     `json:"browser_ephemeral_chat,omitempty" yaml:"browser_ephemeral_chat,omitempty"`
	Group                string   `json:"group,omitempty" yaml:"group,omitempty"`
	RequiredTags         []string `json:"required_tags,omitempty" yaml:"required_tags,omitempty"`
	PreferredNodes       []string `json:"preferred_nodes,omitempty" yaml:"preferred_nodes,omitempty"`
	MinFreeVRAM          uint64   `json:"min_free_vram_bytes,omitempty" yaml:"min_free_vram_bytes,omitempty"`
	Vision               bool     `json:"vision,omitempty" yaml:"vision,omitempty"`
	Embedding            bool     `json:"embedding,omitempty" yaml:"embedding,omitempty"`
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
	VRAM                 int64    `json:"vram_bytes,omitempty"`
	Provider             string   `json:"provider,omitempty"`
}

// BrowserSessionCapability reports only the visible selection and state of an
// explicitly attached tab. Chat contents and tab titles never leave the PC.
type BrowserSessionCapability struct {
	TabID               int      `json:"tab_id"`
	Profile             string   `json:"profile,omitempty"`
	State               string   `json:"state,omitempty"`
	SessionKey          string   `json:"session_key,omitempty"`
	SessionKeySupported bool     `json:"session_key_supported,omitempty"`
	CanCreateFreshChat  bool     `json:"can_create_fresh_chat,omitempty"`
	DefaultFreshChat    bool     `json:"default_fresh_chat,omitempty"`
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
	AutomaticTasks  map[string][]string        `json:"automatic_tasks_by_provider"`
	Tags            []string                   `json:"tags,omitempty"`
	Groups          []string                   `json:"groups,omitempty"`
	MaxConcurrent   int                        `json:"max_concurrent"`
	Running         int                        `json:"running"`
	BrowserTabs     int                        `json:"browser_tabs,omitempty"`
	BrowserBusy     int                        `json:"browser_busy_tabs,omitempty"`
	BrowserSessions []BrowserSessionCapability `json:"browser_sessions,omitempty"`
	Sources         []string                   `json:"sources,omitempty"`
	Modes           []string                   `json:"modes,omitempty"`
	QueueDepth      int                        `json:"queue_depth"`
}

type Node struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	PublicKey     string       `json:"public_key,omitempty"`
	Capabilities  Capabilities `json:"capabilities"`
	State         string       `json:"state"`
	Connected     bool         `json:"connected"`
	LastSeen      time.Time    `json:"last_seen"`
	ClockOffsetMS int64        `json:"clock_offset_ms,omitempty"`
	ConnectedAt   time.Time    `json:"connected_at,omitempty"`
	JobsTotal     uint64       `json:"jobs_total"`
	JobsFailed    uint64       `json:"jobs_failed"`
	ComputeMS     uint64       `json:"compute_ms"`
	CostUSD       float64      `json:"cost_usd"`
}

type Usage struct {
	InputTokens       uint64  `json:"input_tokens,omitempty"`
	OutputTokens      uint64  `json:"output_tokens,omitempty"`
	TotalTokens       uint64  `json:"total_tokens,omitempty"`
	ComputeMS         uint64  `json:"compute_ms,omitempty"`
	QueueMS           uint64  `json:"queue_ms,omitempty"`
	EstimatedCostUSD  float64 `json:"estimated_cost_usd,omitempty"`
	EquivalentCostUSD float64 `json:"equivalent_cloud_cost_usd,omitempty"`
	SavedCostUSD      float64 `json:"saved_cost_usd,omitempty"`
	// Peak resource values are attributable measurements reported by the
	// execution engine. Node-wide hardware snapshots must not populate them or
	// set ResourceScope to "job".
	ResourceScope      string `json:"resource_scope,omitempty"`
	PeakVRAMBytes      uint64 `json:"peak_vram_bytes,omitempty"`
	PeakRAMBytes       uint64 `json:"peak_ram_bytes,omitempty"`
	PeakGPUUtilization int    `json:"peak_gpu_utilization_percent,omitempty"`
}

// ExecutionMetadata is bounded worker-reported routing evidence. Browser tab
// identifiers are local to a worker and are used only to keep later turns of
// the same logical session on the conversation that actually ran the job.
type ExecutionMetadata struct {
	BrowserTabID        int  `json:"browser_tab_id,omitempty"`
	EphemeralBrowserTab bool `json:"ephemeral_browser_tab,omitempty"`
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

type Job struct {
	ID            string          `json:"id"`
	OwnerSubject  string          `json:"owner_subject,omitempty"`
	TenantID      string          `json:"tenant_id,omitempty"`
	Source        string          `json:"source,omitempty"`
	Pipeline      string          `json:"pipeline,omitempty"`
	Step          string          `json:"step,omitempty"`
	ParentID      string          `json:"parent_id,omitempty"`
	Requirements  Requirements    `json:"requirements"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	SealedPayload *SealedEnvelope `json:"sealed_payload,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	SealedResult  *SealedEnvelope `json:"sealed_result,omitempty"`
	Status        string          `json:"status"`
	Priority      int             `json:"priority"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"max_attempts"`
	AssignedNode  string          `json:"assigned_node,omitempty"`
	// RoutingDecision is the bounded, point-in-time evidence used for the
	// durable assignment. It intentionally excludes full node telemetry and is
	// absent until a queued job is actually placed.
	RoutingDecision *RoutingDecision `json:"routing_decision,omitempty"`
	// ExecutedBrowserTabID records the concrete browser tab used by the worker.
	// It can differ from Requirements.BrowserTabID when the extension created a
	// fresh chat for the first turn of a session.
	ExecutedBrowserTabID int          `json:"executed_browser_tab_id,omitempty"`
	EphemeralBrowserTab  bool         `json:"ephemeral_browser_tab,omitempty"`
	Error                string       `json:"error,omitempty"`
	Usage                Usage        `json:"usage"`
	Progress             *JobProgress `json:"progress,omitempty"`
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
	AssignedAt           time.Time    `json:"assigned_at,omitempty"`
	StartedAt            time.Time    `json:"started_at,omitempty"`
	FinishedAt           time.Time    `json:"finished_at,omitempty"`
	ReservationKey       string       `json:"-"`
}

type SubmitRequest struct {
	ID               string          `json:"id,omitempty"`
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
	// Pipeline metadata is relay-internal and is persisted atomically with the
	// queued job. Keeping it out of the producer JSON surface prevents callers
	// from forging orchestration ownership while avoiding a post-admission
	// SaveJob checkpoint that could fail after the job became dispatchable.
	Pipeline string `json:"-"`
	Step     string `json:"-"`
	ParentID string `json:"-"`
}

type Assignment struct {
	ID           string       `json:"id"`
	JobID        string       `json:"job_id"`
	NodeID       string       `json:"node_id"`
	NodeName     string       `json:"node_name"`
	PublicKey    string       `json:"public_key"`
	Attempt      int          `json:"attempt"`
	OwnerSubject string       `json:"owner_subject"`
	TenantID     string       `json:"tenant_id,omitempty"`
	ExpiresAt    time.Time    `json:"expires_at"`
	Requirements Requirements `json:"requirements"`
}

type AssignmentResponse struct {
	Assignment Assignment `json:"assignment"`
	Secret     string     `json:"assignment_secret"`
}

type Pairing struct {
	DeviceCodeHash string    `json:"device_code_hash"`
	UserCode       string    `json:"user_code"`
	NodeName       string    `json:"node_name"`
	PublicKey      string    `json:"public_key"`
	Groups         []string  `json:"groups,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	Approved       bool      `json:"approved"`
	Denied         bool      `json:"denied"`
	NodeID         string    `json:"node_id,omitempty"`
	PendingToken   string    `json:"pending_token,omitempty"`
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

type TokenRecord struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Subject   string    `json:"subject"`
	Groups    []string  `json:"groups,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Revoked   bool      `json:"revoked"`
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

type WireMessage struct {
	Version      int                `json:"version"`
	Type         string             `json:"type"`
	RequestID    string             `json:"request_id,omitempty"`
	Node         *Node              `json:"node,omitempty"`
	Capabilities *Capabilities      `json:"capabilities,omitempty"`
	Job          *Job               `json:"job,omitempty"`
	JobID        string             `json:"job_id,omitempty"`
	Attempt      int                `json:"attempt,omitempty"`
	Result       json.RawMessage    `json:"result,omitempty"`
	SealedResult *SealedEnvelope    `json:"sealed_result,omitempty"`
	Usage        Usage              `json:"usage,omitempty"`
	Execution    *ExecutionMetadata `json:"execution,omitempty"`
	Progress     *JobProgress       `json:"progress,omitempty"`
	Error        string             `json:"error,omitempty"`
}

type Pricing struct {
	ComputePerHourUSD   float64 `json:"compute_per_hour_usd" yaml:"compute_per_hour_usd"`
	InputPerMillionUSD  float64 `json:"input_per_million_usd" yaml:"input_per_million_usd"`
	OutputPerMillionUSD float64 `json:"output_per_million_usd" yaml:"output_per_million_usd"`
	EquivalentInputUSD  float64 `json:"equivalent_input_per_million_usd" yaml:"equivalent_input_per_million_usd"`
	EquivalentOutputUSD float64 `json:"equivalent_output_per_million_usd" yaml:"equivalent_output_per_million_usd"`
}

type Pipeline struct {
	MaxRuntimeSeconds int            `json:"max_runtime_seconds" yaml:"max_runtime_seconds"`
	MaxIterations     int            `json:"max_iterations" yaml:"max_iterations"`
	Steps             []PipelineStep `json:"steps" yaml:"steps"`
}

type PipelineStep struct {
	Name           string       `json:"name" yaml:"name"`
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

type PipelineRun struct {
	ID           string          `json:"id"`
	Pipeline     string          `json:"pipeline"`
	OwnerSubject string          `json:"owner_subject,omitempty"`
	Status       string          `json:"status"`
	Input        json.RawMessage `json:"input"`
	Output       json.RawMessage `json:"output,omitempty"`
	Steps        []Job           `json:"steps"`
	Usage        Usage           `json:"usage"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	FinishedAt   time.Time       `json:"finished_at,omitempty"`
}

type Overview struct {
	NodesOnline      int               `json:"nodes_online"`
	NodesTotal       int               `json:"nodes_total"`
	JobsByState      map[string]uint64 `json:"jobs_by_state"`
	Usage            Usage             `json:"usage"`
	GeneratedAt      time.Time         `json:"generated_at"`
	UTCOffsetSeconds int               `json:"utc_offset_seconds"`
}
