package cluster

import "time"

const (
	MaximumRoutingPerformanceRecords = 64
	routingPerformanceEWMAWeight     = uint64(8)
)

// PlacementPolicy controls only soft performance ranking. Hard requirements,
// policy, trust, capacity and failure circuits always run first and cannot be
// weakened through these values.
type PlacementPolicy struct {
	PerformanceLearning bool
	MinimumSamples      uint32
	HistoryTTL          time.Duration
	LatencyWeight       float64
	MaxLatencyPenalty   float64
}

func DefaultPlacementPolicy() PlacementPolicy {
	return PlacementPolicy{
		PerformanceLearning: true,
		MinimumSamples:      3,
		HistoryTTL:          7 * 24 * time.Hour,
		LatencyWeight:       12,
		MaxLatencyPenalty:   60,
	}
}

func normalizePlacementPolicy(policy PlacementPolicy) PlacementPolicy {
	defaults := DefaultPlacementPolicy()
	if policy.MinimumSamples == 0 {
		policy.MinimumSamples = defaults.MinimumSamples
	}
	if policy.HistoryTTL <= 0 {
		policy.HistoryTTL = defaults.HistoryTTL
	}
	if policy.LatencyWeight <= 0 {
		policy.LatencyWeight = defaults.LatencyWeight
	}
	if policy.MaxLatencyPenalty <= 0 {
		policy.MaxLatencyPenalty = defaults.MaxLatencyPenalty
	}
	return policy
}

func recordRoutingPerformance(node *Node, requirements Requirements, admittedRouteKey string, computeMS uint64, completedAt time.Time) {
	if node == nil || computeMS == 0 || completedAt.IsZero() {
		return
	}
	routeKey, provider, model := routingHealthKey(requirements)
	if validRoutingHealthRouteKey(admittedRouteKey) {
		routeKey = admittedRouteKey
	}
	index := -1
	for i := range node.RoutingPerformance {
		if node.RoutingPerformance[i].RouteKey == routeKey {
			index = i
			break
		}
	}
	if index < 0 {
		if len(node.RoutingPerformance) >= MaximumRoutingPerformanceRecords {
			oldest := 0
			for i := 1; i < len(node.RoutingPerformance); i++ {
				if node.RoutingPerformance[i].LastCompletedAt.Before(node.RoutingPerformance[oldest].LastCompletedAt) {
					oldest = i
				}
			}
			node.RoutingPerformance = append(node.RoutingPerformance[:oldest], node.RoutingPerformance[oldest+1:]...)
		}
		node.RoutingPerformance = append(node.RoutingPerformance, RoutingPerformance{
			RouteKey: routeKey, Provider: provider, Model: model,
		})
		index = len(node.RoutingPerformance) - 1
	}
	record := &node.RoutingPerformance[index]
	if record.Samples == 0 || record.EWMAComputeMS == 0 {
		record.EWMAComputeMS = computeMS
	} else {
		// Bound one unusual completion to 4x the existing estimate after the
		// initial learning samples. This preserves real trends without letting a
		// single giant prompt poison future placement indefinitely.
		observation := computeMS
		if record.Samples >= 3 {
			upper := record.EWMAComputeMS
			if upper > ^uint64(0)/4 {
				upper = ^uint64(0)
			} else {
				upper *= 4
			}
			lower := record.EWMAComputeMS / 4
			if observation > upper {
				observation = upper
			} else if observation < lower {
				observation = lower
			}
		}
		if observation >= record.EWMAComputeMS {
			delta := observation - record.EWMAComputeMS
			step := delta / routingPerformanceEWMAWeight
			if delta%routingPerformanceEWMAWeight != 0 {
				step++
			}
			record.EWMAComputeMS += step
		} else {
			record.EWMAComputeMS -= (record.EWMAComputeMS - observation) / routingPerformanceEWMAWeight
		}
	}
	if record.Samples < ^uint32(0) {
		record.Samples++
	}
	record.LastComputeMS = computeMS
	record.LastCompletedAt = completedAt
}

func routingObservedComputeMS(job Job) uint64 {
	if job.StartedAt.IsZero() || job.FinishedAt.IsZero() || !job.FinishedAt.After(job.StartedAt) {
		return 0
	}
	return nonNegativeDurationMilliseconds(job.FinishedAt.Sub(job.StartedAt))
}

func routingPerformanceFor(node Node, requirements Requirements, policy PlacementPolicy, now time.Time) (RoutingPerformance, bool) {
	if !policy.PerformanceLearning {
		return RoutingPerformance{}, false
	}
	policy = normalizePlacementPolicy(policy)
	routeKey, _, _ := routingHealthKey(requirements)
	for _, record := range node.RoutingPerformance {
		if record.RouteKey != routeKey || record.Samples < policy.MinimumSamples || record.EWMAComputeMS == 0 || record.LastCompletedAt.IsZero() {
			continue
		}
		age := now.Sub(record.LastCompletedAt)
		if age < 0 || age > policy.HistoryTTL {
			continue
		}
		return record, true
	}
	return RoutingPerformance{}, false
}

func boundedRoutingPerformance(records []RoutingPerformance) []RoutingPerformance {
	if len(records) > MaximumRoutingPerformanceRecords {
		records = records[len(records)-MaximumRoutingPerformanceRecords:]
	}
	return append([]RoutingPerformance(nil), records...)
}
