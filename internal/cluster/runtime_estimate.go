package cluster

import (
	"sort"
	"time"
)

const (
	HistoricalRuntimeEstimateV1      = "contextbridge.historical-runtime-estimate.v1"
	MinimumRuntimeEstimateSamples    = 5
	minimumRuntimeRemainingSurvivors = 3
)

// HistoricalRuntimeEstimate is explicitly advisory. Lifecycle state and
// elapsed time remain relay-authoritative; the quantiles below are derived
// only from a bounded, local history of successful comparable executions.
type HistoricalRuntimeEstimate struct {
	Schema             string `json:"schema"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Source             string `json:"source"`
	Samples            int    `json:"samples"`
	Profile            string `json:"profile,omitempty"`
	ElapsedMS          uint64 `json:"elapsed_ms"`
	TotalP50MS         uint64 `json:"total_p50_ms,omitempty"`
	TotalP90MS         uint64 `json:"total_p90_ms,omitempty"`
	RemainingP50MS     uint64 `json:"remaining_p50_ms,omitempty"`
	RemainingP90MS     uint64 `json:"remaining_p90_ms,omitempty"`
	EvidenceAgeSeconds uint64 `json:"evidence_age_seconds,omitempty"`
	OutsideTypical     bool   `json:"outside_typical_range,omitempty"`
}

type runtimeDistribution struct {
	values          []uint64
	lastCompletedAt time.Time
	profile         string
}

// EstimateJobRuntimeAt projects a dynamic remaining-time range from local
// successful history. It does not affect eligibility, placement, fencing,
// cancellation, completion, or any other authoritative job state.
func EstimateJobRuntimeAt(job Job, node Node, policy PlacementPolicy, now time.Time) HistoricalRuntimeEstimate {
	estimate := HistoricalRuntimeEstimate{
		Schema: HistoricalRuntimeEstimateV1, Status: "unavailable", Source: "local_success_history",
	}
	if !policy.PerformanceLearning {
		estimate.Reason = "learning_disabled"
		return estimate
	}
	if job.Status != JobAssigned && job.Status != JobRunning {
		estimate.Reason = "job_not_active"
		return estimate
	}
	if job.AssignedNode == "" || node.ID == "" || job.AssignedNode != node.ID {
		estimate.Reason = "selected_node_unavailable"
		return estimate
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	distribution, ok := runtimeDistributionFor(node, job, policy, now)
	if !ok {
		estimate.Reason = "insufficient_comparable_history"
		return estimate
	}
	values := append([]uint64(nil), distribution.values...)
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	estimate.Status = "available"
	estimate.Samples = len(values)
	estimate.Profile = distribution.profile
	estimate.TotalP50MS = durationQuantile(values, 50)
	estimate.TotalP90MS = durationQuantile(values, 90)
	if age := now.Sub(distribution.lastCompletedAt); age >= 0 {
		estimate.EvidenceAgeSeconds = uint64(age / time.Second) // #nosec G115 -- non-negative time.Duration is bounded by int64.
	}
	if job.Status == JobRunning && !job.StartedAt.IsZero() {
		estimate.ElapsedMS = nonNegativeDurationMilliseconds(now.Sub(job.StartedAt))
	}
	survivors := make([]uint64, 0, len(values))
	for _, total := range values {
		if total > estimate.ElapsedMS {
			survivors = append(survivors, total-estimate.ElapsedMS)
		}
	}
	if len(survivors) >= minimumRuntimeRemainingSurvivors {
		estimate.RemainingP50MS = durationQuantile(survivors, 50)
		estimate.RemainingP90MS = durationQuantile(survivors, 90)
		return estimate
	}
	if estimate.ElapsedMS >= estimate.TotalP90MS {
		estimate.Status = "outside_typical_range"
		estimate.Reason = "elapsed_exceeds_typical_history"
		estimate.OutsideTypical = true
		return estimate
	}
	estimate.Status = "uncertain"
	estimate.Reason = "too_few_longer_comparable_runs"
	return estimate
}

func runtimeDistributionFor(node Node, job Job, policy PlacementPolicy, now time.Time) (runtimeDistribution, bool) {
	policy = normalizePlacementPolicy(policy)
	if policy.MinimumSamples > MaximumRoutingDurationSamples {
		return runtimeDistribution{}, false
	}
	minimum := max(MinimumRuntimeEstimateSamples, int(policy.MinimumSamples))
	routeKey, _, _ := routingHealthKey(job.Requirements)
	if admitted := jobRoutingHealthRouteKey(job); validRoutingHealthRouteKey(admitted) {
		routeKey = admitted
	}
	contextClass := jobRoutingPerformanceContext(job)
	for _, record := range node.RoutingPerformance {
		if record.RouteKey != routeKey {
			continue
		}
		if validRoutingPerformanceContext(contextClass) {
			for _, profile := range record.LoadProfiles {
				if profile.ContextClass == contextClass {
					if values, lastCompletedAt, ok := freshRuntimeDistribution(profile.RecentSuccessSamples, minimum, policy.HistoryTTL, now); ok {
						return runtimeDistribution{values: values, lastCompletedAt: lastCompletedAt, profile: "node_route_load"}, true
					}
				}
			}
		}
		if values, lastCompletedAt, ok := freshRuntimeDistribution(record.RecentSuccessSamples, minimum, policy.HistoryTTL, now); ok {
			return runtimeDistribution{values: values, lastCompletedAt: lastCompletedAt, profile: "node_route"}, true
		}
	}
	return runtimeDistribution{}, false
}

func freshRuntimeDistribution(samples []RoutingDurationSample, minimum int, ttl time.Duration, now time.Time) ([]uint64, time.Time, bool) {
	samples = freshRoutingDurationSamples(samples, ttl, now)
	if len(samples) < minimum {
		return nil, time.Time{}, false
	}
	values := make([]uint64, 0, len(samples))
	var lastCompletedAt time.Time
	for _, sample := range samples {
		values = append(values, sample.ComputeMS)
		if sample.CompletedAt.After(lastCompletedAt) {
			lastCompletedAt = sample.CompletedAt
		}
	}
	return values, lastCompletedAt, true
}

func durationQuantile(sorted []uint64, percentile int) uint64 {
	if len(sorted) == 0 || percentile < 1 || percentile > 100 {
		return 0
	}
	// Nearest-rank quantile: ceil(percentile*n/100)-1. The numerator is
	// bounded by 100*MaximumRoutingDurationSamples.
	index := (percentile*len(sorted) + 99) / 100
	return sorted[index-1]
}
