package cluster

import (
	"context"
	"errors"
	"testing"
)

func TestWorkerFailureCodesAreBoundedAndBackwardCompatible(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{message: "adapter_rate_limited", want: FailureAdapterRateLimited},
		{message: "adapter_timeout", want: FailureAdapterTimeout},
		{message: "adapter_automation_error: provider dialog", want: FailureAdapterAutomation},
		{message: "cost_budget_exceeded: reservation", want: FailureCostBudgetExceeded},
		{message: "cost_budget_unverifiable: unknown price", want: FailureCostBudgetUnverifiable},
		{message: "images_missing: expected 1", want: FailureArtifactRequirement},
		{message: "provider_not_bound_to_local_route: missing", want: FailureProviderRouteUnavailable},
		{message: "unreviewed provider prose", want: FailureWorkerExecution},
	}
	for _, test := range tests {
		if got := failureCodeFromText(test.message); got != test.want {
			t.Errorf("failureCodeFromText(%q) = %q, want %q", test.message, got, test.want)
		}
	}
	if got := normalizedWorkerFailureCode("invented_by_worker", "unreviewed provider prose"); got != FailureWorkerExecution {
		t.Fatalf("untrusted worker invented durable failure code %q", got)
	}
	if got := normalizedWorkerFailureCode(FailureAdapterTimeout, "diagnostic text may change"); got != FailureAdapterTimeout {
		t.Fatalf("reviewed worker failure code was not retained: %q", got)
	}
}

func TestWorkerFailureCodesPreserveContextCancellation(t *testing.T) {
	if got := workerFailureCode(context.Canceled); got != FailureWorkerExecutionCancelled {
		t.Fatalf("cancelled code = %q", got)
	}
	wrapped := errors.Join(errors.New("provider stopped"), context.DeadlineExceeded)
	if got := workerFailureCode(wrapped); got != FailureWorkerExecutionTimeout {
		t.Fatalf("timeout code = %q", got)
	}
}
