package cluster

const ProtocolManifestV1 = "contextbridge.protocol-manifest.v1"

type ProtocolManifest struct {
	Schema              string         `json:"schema"`
	WireProtocolVersion int            `json:"wire_protocol_version"`
	JobContractVersions []string       `json:"job_contract_versions"`
	Features            []string       `json:"features"`
	AdmissionErrorCodes []string       `json:"admission_error_codes"`
	RuntimeFailureCodes []string       `json:"runtime_failure_codes"`
	Limits              ProtocolLimits `json:"limits"`
}

type ProtocolLimits struct {
	MaximumConfiguredJobPayloadBytes int64 `json:"maximum_configured_job_payload_bytes"`
	MaximumJobPayloadBytes           int64 `json:"maximum_job_payload_bytes"`
	MaximumJobResultBytes            int64 `json:"maximum_job_result_bytes"`
	MaximumWorkerConcurrency         int   `json:"maximum_worker_concurrency"`
	MaximumAdapterSessions           int   `json:"maximum_adapter_sessions"`
	MaximumAdapterModelChoices       int   `json:"maximum_adapter_model_choices"`
	MaximumAdapterReasoningLevels    int   `json:"maximum_adapter_reasoning_levels"`
	MaximumGPUCapabilities           int   `json:"maximum_gpu_capabilities"`
	MaximumModelCapabilities         int   `json:"maximum_model_capabilities"`
	MaximumNodeListValues            int   `json:"maximum_node_list_values"`
}

func CurrentProtocolManifest(configuredJobBytes int64) ProtocolManifest {
	if configuredJobBytes <= 0 || configuredJobBytes > MaximumJobPayloadBytes {
		configuredJobBytes = MaximumJobPayloadBytes
	}
	return ProtocolManifest{
		Schema:              ProtocolManifestV1,
		WireProtocolVersion: ProtocolVersion,
		JobContractVersions: []string{JobContractV1},
		Features: []string{
			"content_minimizing_execution_receipts",
			"durable_execution_policy_v1",
			"durable_worker_drain_v1",
			"job_contract_dry_run",
			"producer_scoped_idempotency",
			"relay_conformance_v1",
			"stable_runtime_failure_codes",
			"worker_conformance_report_v1",
		},
		AdmissionErrorCodes: StableAdmissionErrorCodes(),
		RuntimeFailureCodes: StableRuntimeFailureCodes(),
		Limits: ProtocolLimits{
			MaximumConfiguredJobPayloadBytes: configuredJobBytes,
			MaximumJobPayloadBytes:           MaximumJobPayloadBytes,
			MaximumJobResultBytes:            MaximumJobResultBytes,
			MaximumWorkerConcurrency:         MaximumWorkerConcurrency,
			MaximumAdapterSessions:           MaximumAdapterSessions,
			MaximumAdapterModelChoices:       MaximumAdapterModelChoices,
			MaximumAdapterReasoningLevels:    MaximumAdapterReasoningLevels,
			MaximumGPUCapabilities:           MaximumGPUCapabilities,
			MaximumModelCapabilities:         MaximumModelCapabilities,
			MaximumNodeListValues:            MaximumNodeListValues,
		},
	}
}
