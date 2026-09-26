package cluster

import (
	"strings"
	"time"
)

const (
	MaximumRoutingPerformanceRecords      = 64
	MaximumRoutingLoadProfilesPerRoute    = 12
	routingPerformanceEWMAWeight          = uint64(8)
	routingPerformanceSourceContext       = "load_context"
	routingPerformanceSourceRouteBaseline = "route_baseline"
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

type routingPerformanceEstimate struct {
	Samples         uint32
	EWMAComputeMS   uint64
	LastCompletedAt time.Time
	Source          string
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

// routingPerformanceContext turns bounded point-in-time load evidence into one
// of twelve stable classes. The class is intentionally coarse: enough to learn
// that a route behaves differently under load without creating high-cardinality
// state or pretending that noisy telemetry is an exact performance model.
func routingPerformanceContext(node Node, requirements Requirements, placementVRAM uint64) string {
	capacity := node.Capabilities.MaxConcurrent
	if capacity <= 0 {
		capacity = 1
	}
	pressure := boundedRatio(node.Capabilities.Running, capacity)
	pressure = max(pressure, boundedRatio(node.Capabilities.QueueDepth, capacity))
	pressure = max(pressure, boundedMemoryPressure(node.Capabilities.MemoryFree, node.Capabilities.MemoryTotal))
	pressure = max(pressure, boundedUtilization(node.Capabilities.CPUUtilization))
	if len(node.Capabilities.GPUs) > 0 {
		pressure = max(pressure, bestGPUUtilization(node, placementVRAM))
		pressure = max(pressure, bestGPUMemoryPressure(node, placementVRAM))
	}
	if node.Capabilities.AdapterEndpoints > 0 {
		pressure = max(pressure, boundedRatio(node.Capabilities.AdapterBusy, node.Capabilities.AdapterEndpoints))
	}

	loadClass := "high"
	switch {
	case pressure < 0.20:
		loadClass = "idle"
	case pressure < 0.45:
		loadClass = "light"
	case pressure < 0.70:
		loadClass = "moderate"
	}
	return loadClass + ":" + routingModelWarmth(node.Capabilities.Models, requirements)
}

func bestGPUMemoryPressure(node Node, required uint64) float64 {
	best := 1.0
	found := false
	for _, gpu := range node.Capabilities.GPUs {
		free := boundedFreeMemory(gpu.MemoryFree, gpu.MemoryTotal)
		if gpu.MemoryTotal == 0 || free < required {
			continue
		}
		pressure := 1 - float64(free)/float64(gpu.MemoryTotal)
		if !found || pressure < best {
			best = pressure
			found = true
		}
	}
	if !found {
		return 0
	}
	return best
}

func boundedRatio(numerator, denominator int) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	if numerator >= denominator {
		return 1
	}
	return float64(numerator) / float64(denominator)
}

func routingModelWarmth(models []ModelCapability, requirements Requirements) string {
	wanted := strings.TrimSpace(requirements.Model)
	automatic := wanted == "" || strings.EqualFold(wanted, "auto")
	found := false
	for _, model := range models {
		if !modelMatchesProvider(model, requirements.Provider) {
			continue
		}
		if !automatic {
			matches := strings.EqualFold(model.Name, wanted)
			if strings.EqualFold(requirements.Provider, "adapter") {
				matches = adapterModelEqual(model.Name, wanted)
			}
			if !matches {
				continue
			}
		} else if (requirements.Task != "" && !containsFold(model.Tasks, requirements.Task)) || !modelMeetsHardRequirements(model, requirements) {
			continue
		}
		found = true
		if model.Loaded {
			return "warm"
		}
	}
	if found {
		return "cold"
	}
	return "unknown"
}

func validRoutingPerformanceContext(value string) bool {
	for _, loadClass := range []string{"idle", "light", "moderate", "high"} {
		for _, warmth := range []string{"warm", "cold", "unknown"} {
			if value == loadClass+":"+warmth {
				return true
			}
		}
	}
	return false
}

func recordRoutingPerformance(node *Node, requirements Requirements, admittedRouteKey string, computeMS uint64, completedAt time.Time, contextClass string) {
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
	updateRoutingPerformanceSample(&record.Samples, &record.EWMAComputeMS, &record.LastComputeMS, &record.LastCompletedAt, computeMS, completedAt)

	if !validRoutingPerformanceContext(contextClass) {
		return
	}
	profileIndex := -1
	for i := range record.LoadProfiles {
		if record.LoadProfiles[i].ContextClass == contextClass {
			profileIndex = i
			break
		}
	}
	if profileIndex < 0 {
		if len(record.LoadProfiles) >= MaximumRoutingLoadProfilesPerRoute {
			oldest := 0
			for i := 1; i < len(record.LoadProfiles); i++ {
				if record.LoadProfiles[i].LastCompletedAt.Before(record.LoadProfiles[oldest].LastCompletedAt) {
					oldest = i
				}
			}
			record.LoadProfiles = append(record.LoadProfiles[:oldest], record.LoadProfiles[oldest+1:]...)
		}
		record.LoadProfiles = append(record.LoadProfiles, RoutingLoadPerformance{ContextClass: contextClass})
		profileIndex = len(record.LoadProfiles) - 1
	}
	profile := &record.LoadProfiles[profileIndex]
	updateRoutingPerformanceSample(&profile.Samples, &profile.EWMAComputeMS, &profile.LastComputeMS, &profile.LastCompletedAt, computeMS, completedAt)
}

func updateRoutingPerformanceSample(samples *uint32, estimate, last *uint64, lastAt *time.Time, computeMS uint64, completedAt time.Time) {
	if *samples == 0 || *estimate == 0 {
		*estimate = computeMS
	} else {
		// Bound one unusual completion to 4x the existing estimate after the
		// initial learning samples. This preserves real trends without letting a
		// single giant prompt poison future placement indefinitely.
		observation := computeMS
		if *samples >= 3 {
			upper := *estimate
			if upper > ^uint64(0)/4 {
				upper = ^uint64(0)
			} else {
				upper *= 4
			}
			lower := *estimate / 4
			if observation > upper {
				observation = upper
			} else if observation < lower {
				observation = lower
			}
		}
		if observation >= *estimate {
			delta := observation - *estimate
			step := delta / routingPerformanceEWMAWeight
			if delta%routingPerformanceEWMAWeight != 0 {
				step++
			}
			*estimate += step
		} else {
			*estimate -= (*estimate - observation) / routingPerformanceEWMAWeight
		}
	}
	if *samples < ^uint32(0) {
		*samples++
	}
	*last = computeMS
	*lastAt = completedAt
}

func routingObservedComputeMS(job Job) uint64 {
	if job.StartedAt.IsZero() || job.FinishedAt.IsZero() || !job.FinishedAt.After(job.StartedAt) {
		return 0
	}
	return nonNegativeDurationMilliseconds(job.FinishedAt.Sub(job.StartedAt))
}

func routingPerformanceEstimateFor(node Node, requirements Requirements, contextClass string, policy PlacementPolicy, now time.Time) (routingPerformanceEstimate, bool) {
	if !policy.PerformanceLearning {
		return routingPerformanceEstimate{}, false
	}
	policy = normalizePlacementPolicy(policy)
	routeKey, _, _ := routingHealthKey(requirements)
	for _, record := range node.RoutingPerformance {
		if record.RouteKey != routeKey {
			continue
		}
		if validRoutingPerformanceContext(contextClass) {
			for _, profile := range record.LoadProfiles {
				if profile.ContextClass == contextClass && freshRoutingPerformance(profile.Samples, profile.EWMAComputeMS, profile.LastCompletedAt, policy, now) {
					return routingPerformanceEstimate{Samples: profile.Samples, EWMAComputeMS: profile.EWMAComputeMS, LastCompletedAt: profile.LastCompletedAt, Source: routingPerformanceSourceContext}, true
				}
			}
		}
		if freshRoutingPerformance(record.Samples, record.EWMAComputeMS, record.LastCompletedAt, policy, now) {
			return routingPerformanceEstimate{Samples: record.Samples, EWMAComputeMS: record.EWMAComputeMS, LastCompletedAt: record.LastCompletedAt, Source: routingPerformanceSourceRouteBaseline}, true
		}
	}
	return routingPerformanceEstimate{}, false
}

func freshRoutingPerformance(samples uint32, estimate uint64, completedAt time.Time, policy PlacementPolicy, now time.Time) bool {
	if samples < policy.MinimumSamples || estimate == 0 || completedAt.IsZero() {
		return false
	}
	age := now.Sub(completedAt)
	return age >= 0 && age <= policy.HistoryTTL
}

// routingPerformanceFor preserves the route-baseline helper used by focused
// tests and callers that intentionally do not have point-in-time load context.
func routingPerformanceFor(node Node, requirements Requirements, policy PlacementPolicy, now time.Time) (RoutingPerformance, bool) {
	estimate, ok := routingPerformanceEstimateFor(node, requirements, "", policy, now)
	if !ok {
		return RoutingPerformance{}, false
	}
	return RoutingPerformance{Samples: estimate.Samples, EWMAComputeMS: estimate.EWMAComputeMS, LastCompletedAt: estimate.LastCompletedAt}, true
}

func boundedRoutingPerformance(records []RoutingPerformance) []RoutingPerformance {
	if len(records) > MaximumRoutingPerformanceRecords {
		records = records[len(records)-MaximumRoutingPerformanceRecords:]
	}
	result := make([]RoutingPerformance, 0, len(records))
	for _, record := range records {
		profiles := make([]RoutingLoadPerformance, 0, min(len(record.LoadProfiles), MaximumRoutingLoadProfilesPerRoute))
		seenContexts := make(map[string]struct{}, MaximumRoutingLoadProfilesPerRoute)
		for _, profile := range record.LoadProfiles {
			if !validRoutingPerformanceContext(profile.ContextClass) {
				continue
			}
			if _, exists := seenContexts[profile.ContextClass]; exists {
				continue
			}
			seenContexts[profile.ContextClass] = struct{}{}
			profiles = append(profiles, profile)
			if len(profiles) == MaximumRoutingLoadProfilesPerRoute {
				break
			}
		}
		record.LoadProfiles = profiles
		result = append(result, record)
	}
	return result
}
