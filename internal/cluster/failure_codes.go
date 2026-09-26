package cluster

import (
	"context"
	"errors"
	"strings"
)

const (
	FailureAdapterRateLimited          = "adapter_rate_limited"
	FailureAdapterTimeout              = "adapter_timeout"
	FailureAdapterAutomation           = "adapter_automation_error"
	FailureCostBudgetExceeded          = "cost_budget_exceeded"
	FailureCostBudgetUnverifiable      = "cost_budget_unverifiable"
	FailureArtifactRequirement         = "artifact_requirement_unsatisfied"
	FailureProviderRouteUnavailable    = "provider_route_unavailable"
	FailureWorkerCapacity              = "worker_capacity_exceeded"
	FailureWorkerStopping              = "worker_stopping"
	FailureWorkerExecutionCancelled    = "worker_execution_cancelled"
	FailureWorkerExecutionTimeout      = "worker_execution_timeout"
	FailureWorkerExecution             = "worker_execution_failed"
	FailureWorkerResultRejected        = "worker_result_rejected"
	FailureExecutionStateAmbiguous     = "execution_state_ambiguous"
	FailureExecutionTimeoutAmbiguous   = "execution_timeout_ambiguous"
	FailureEncryptedReservationExpired = "encrypted_reservation_expired"
	FailurePipelineParentTerminal      = "pipeline_parent_terminal"
)

var stableFailureCodeList = []string{
	FailureAdapterAutomation,
	FailureAdapterRateLimited,
	FailureAdapterTimeout,
	FailureArtifactRequirement,
	FailureCostBudgetExceeded,
	FailureCostBudgetUnverifiable,
	FailureEncryptedReservationExpired,
	FailureExecutionStateAmbiguous,
	FailureExecutionTimeoutAmbiguous,
	FailurePipelineParentTerminal,
	FailureProviderRouteUnavailable,
	FailureWorkerCapacity,
	FailureWorkerExecutionCancelled,
	FailureWorkerExecution,
	FailureWorkerExecutionTimeout,
	FailureWorkerResultRejected,
	FailureWorkerStopping,
}

var stableFailureCodes = func() map[string]struct{} {
	result := make(map[string]struct{}, len(stableFailureCodeList))
	for _, code := range stableFailureCodeList {
		result[code] = struct{}{}
	}
	return result
}()

func StableRuntimeFailureCodes() []string {
	return append([]string(nil), stableFailureCodeList...)
}

func workerFailureCode(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == FailureExecutionStateAmbiguous || strings.HasPrefix(message, FailureExecutionStateAmbiguous+":") || strings.HasPrefix(message, FailureExecutionStateAmbiguous+"\n") {
		return FailureExecutionStateAmbiguous
	}
	if errors.Is(err, errWorkerExecutionPanicked) {
		return FailureExecutionStateAmbiguous
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureWorkerExecutionTimeout
	}
	if errors.Is(err, context.Canceled) {
		return FailureWorkerExecutionCancelled
	}
	return failureCodeFromText(err.Error())
}

func normalizedWorkerFailureCode(reported, message string) string {
	if message == "" {
		return ""
	}
	if _, ok := stableFailureCodes[reported]; ok {
		return reported
	}
	return failureCodeFromText(message)
}

func failureCodeFromText(message string) string {
	value := strings.ToLower(strings.TrimSpace(message))
	switch {
	case value == FailureExecutionStateAmbiguous || strings.HasPrefix(value, FailureExecutionStateAmbiguous+":"):
		return FailureExecutionStateAmbiguous
	case value == FailureAdapterRateLimited || strings.HasPrefix(value, FailureAdapterRateLimited+":"):
		return FailureAdapterRateLimited
	case value == FailureAdapterTimeout || strings.HasPrefix(value, FailureAdapterTimeout+":"):
		return FailureAdapterTimeout
	case value == FailureAdapterAutomation || strings.HasPrefix(value, FailureAdapterAutomation+":"):
		return FailureAdapterAutomation
	case value == FailureCostBudgetExceeded || strings.HasPrefix(value, FailureCostBudgetExceeded+":"):
		return FailureCostBudgetExceeded
	case value == FailureCostBudgetUnverifiable || strings.HasPrefix(value, FailureCostBudgetUnverifiable+":"):
		return FailureCostBudgetUnverifiable
	case strings.HasPrefix(value, "artifacts_missing:"), strings.HasPrefix(value, "images_missing:"), strings.HasPrefix(value, "music_missing:"):
		return FailureArtifactRequirement
	case strings.HasPrefix(value, "provider_not_bound_to_local_route:"):
		return FailureProviderRouteUnavailable
	case strings.Contains(value, "worker capacity exceeded"):
		return FailureWorkerCapacity
	case strings.Contains(value, "worker is stopping"):
		return FailureWorkerStopping
	default:
		return FailureWorkerExecution
	}
}

func sealedJobFailureMessage(failureCode string) string {
	if _, ok := stableFailureCodes[failureCode]; !ok {
		failureCode = FailureWorkerExecution
	}
	return "sealed job failed: " + failureCode
}
