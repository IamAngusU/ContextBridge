package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	MaximumRoutingHealthRecords         = 64
	MaximumRoutingHealthRecordsPerOwner = 8
	routingFailureThreshold             = uint32(3)
	routingFailureWindow                = 10 * time.Minute
	routingInitialCooldown              = 30 * time.Second
	routingMaximumCooldown              = 15 * time.Minute
	routingFailureScore                 = 18.0
)

func normalizedRoutingHealthLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "auto" {
		return "*"
	}
	return cleanLabel(value, 200)
}

func opaqueRoutingHealthLabel(value string) string {
	value = cleanLabel(value, 200)
	if value == "" {
		return "*"
	}
	return value
}

func routingHealthKey(requirements Requirements) (string, string, string) {
	provider := normalizedRoutingHealthLabel(requirements.Provider)
	model := normalizedRoutingHealthLabel(requirements.Model)
	parts := []string{
		"contextbridge-routing-health-route-v2",
		provider,
		model,
		normalizedRoutingHealthLabel(requirements.Task),
		normalizedRoutingHealthLabel(requirements.Reasoning),
	}
	if provider == "adapter" {
		parts = append(parts,
			normalizedRoutingHealthLabel(requirements.AdapterProfile),
			strconv.Itoa(requirements.AdapterEndpointID),
			opaqueRoutingHealthLabel(requirements.AdapterPrincipal),
			opaqueRoutingHealthLabel(canonicalSessionID(requirements.SessionID)),
			strconv.FormatBool(requirements.AdapterFreshSession),
			strconv.FormatBool(requirements.AdapterEphemeralSession),
			strconv.FormatBool(requirements.AdapterSessionRecovery),
		)
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:]), provider, model
}

func routingHealthOwnerScope(ownerSubject string) string {
	ownerSubject = strings.TrimSpace(ownerSubject)
	if ownerSubject == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("contextbridge-routing-health-owner-v1\x00" + ownerSubject))
	return hex.EncodeToString(digest[:])
}

func routingHealthFor(node Node, requirements Requirements) (RoutingHealth, bool) {
	return routingHealthForOwnerAt(node, requirements, "", time.Now().UTC())
}

func routingHealthForOwner(node Node, requirements Requirements, ownerSubject string) (RoutingHealth, bool) {
	return routingHealthForOwnerAt(node, requirements, ownerSubject, time.Now().UTC())
}

func routingHealthForOwnerAt(node Node, requirements Requirements, ownerSubject string, now time.Time) (RoutingHealth, bool) {
	routeKey, _, _ := routingHealthKey(requirements)
	ownerScope := routingHealthOwnerScope(ownerSubject)
	var selected RoutingHealth
	selectedState := 0
	found := false
	for _, health := range node.RoutingHealth {
		// Records written before route-v2 deliberately do not match. Their
		// provider/model identity is too coarse and could poison an independent
		// adapter profile after an upgrade.
		if health.RouteKey == "" || health.RouteKey != routeKey {
			continue
		}
		if health.OwnerScope != "" && (ownerScope == "" || health.OwnerScope != ownerScope) {
			continue
		}
		state := routingHealthState(health, now)
		if state == 0 {
			continue
		}
		if !found || routingHealthMoreSevere(health, state, selected, selectedState) {
			selected = health
			selectedState = state
			found = true
		}
	}
	selected.OwnerScope = ""
	return selected, found
}

// routingHealthState classifies one coherent record. Aggregation must select a
// whole record rather than independently combining a stale streak, a fresh
// timestamp, and another record's cooldown.
func routingHealthState(health RoutingHealth, now time.Time) int {
	if health.CircuitOpenUntil.After(now) {
		return 3
	}
	if health.ConsecutiveFailures >= routingFailureThreshold && !health.CircuitOpenUntil.IsZero() {
		return 2 // cooldown elapsed; recovery probation
	}
	if !health.LastFailureAt.IsZero() && !now.Before(health.LastFailureAt) && now.Sub(health.LastFailureAt) <= routingFailureWindow {
		return 1
	}
	return 0
}

func routingHealthMoreSevere(candidate RoutingHealth, candidateState int, selected RoutingHealth, selectedState int) bool {
	if candidateState != selectedState {
		return candidateState > selectedState
	}
	if candidateState >= 2 && !candidate.CircuitOpenUntil.Equal(selected.CircuitOpenUntil) {
		return candidate.CircuitOpenUntil.After(selected.CircuitOpenUntil)
	}
	if candidate.ConsecutiveFailures != selected.ConsecutiveFailures {
		return candidate.ConsecutiveFailures > selected.ConsecutiveFailures
	}
	return candidate.LastFailureAt.After(selected.LastFailureAt)
}

func trackRoutingFailure(code string) bool {
	switch code {
	case FailureAdapterRateLimited,
		FailureAdapterTimeout,
		FailureAdapterAutomation,
		FailureProviderRouteUnavailable,
		FailureWorkerExecutionTimeout,
		FailureWorkerExecution,
		FailureWorkerResultRejected,
		FailureExecutionStateAmbiguous,
		FailureExecutionTimeoutAmbiguous:
		return true
	default:
		return false
	}
}

func recordRoutingOutcome(node *Node, requirements Requirements, success bool, failureCode string, now time.Time) {
	recordRoutingOutcomeForOwner(node, requirements, "", success, failureCode, now)
}

func recordRoutingOutcomeForOwner(node *Node, requirements Requirements, ownerSubject string, success bool, failureCode string, now time.Time) {
	recordRoutingOutcomeForOwnerRoute(node, requirements, ownerSubject, "", "", success, failureCode, now)
}

func recordRoutingOutcomeForOwnerRoute(node *Node, requirements Requirements, ownerSubject, admittedRouteKey, jobID string, success bool, failureCode string, now time.Time) {
	if node == nil {
		return
	}
	routeKey, provider, model := routingHealthKey(requirements)
	if validRoutingHealthRouteKey(admittedRouteKey) {
		routeKey = admittedRouteKey
	}
	ownerScope := routingHealthOwnerScope(ownerSubject)
	trackedFailure := trackRoutingFailure(failureCode)
	index := -1
	for i := range node.RoutingHealth {
		if node.RoutingHealth[i].OwnerScope == ownerScope && node.RoutingHealth[i].RouteKey == routeKey {
			index = i
			break
		}
	}
	// Resolve any single-flight probe owned by this producer. A failed probe of
	// node-wide health must reopen that same global record; otherwise a producer-
	// scoped failure would leave the global route immediately probeable again.
	for i := range node.RoutingHealth {
		health := &node.RoutingHealth[i]
		if health.RouteKey != routeKey || health.ProbeJobID == "" || health.ProbeJobID != jobID {
			continue
		}
		matchingOwnerProbe := health.OwnerScope == ownerScope
		matchingGlobalProbe := health.OwnerScope == "" && health.ProbeOwnerScope == ownerScope
		if !matchingOwnerProbe && !matchingGlobalProbe {
			continue
		}
		failedProbe := !success && trackedFailure
		health.ProbeJobID = ""
		health.ProbeOwnerScope = ""
		// Keep an already-open recovery cycle intact even when its cooldown was
		// longer than the ordinary soft-failure window. The matching owner record
		// is updated below; a global record probed by this owner is updated here.
		if failedProbe && (health.LastFailureAt.IsZero() || now.Before(health.LastFailureAt) || now.Sub(health.LastFailureAt) > routingFailureWindow) {
			health.LastFailureAt = now
		}
		if failedProbe && matchingGlobalProbe && ownerScope != "" {
			applyRoutingFailure(health, failureCode, now)
		}
	}
	if success {
		kept := node.RoutingHealth[:0]
		for _, health := range node.RoutingHealth {
			matchingRoute := health.RouteKey == routeKey
			matchingScope := health.OwnerScope == ownerScope || (ownerScope != "" && health.OwnerScope == "")
			matchingProbe := health.ProbeJobID == "" || health.ProbeJobID == jobID
			if matchingRoute && matchingScope && matchingProbe {
				continue
			}
			kept = append(kept, health)
		}
		node.RoutingHealth = kept
		return
	}
	if !trackedFailure {
		return
	}
	if index < 0 {
		if ownerScope != "" && routingHealthScopeCount(node.RoutingHealth, ownerScope) >= MaximumRoutingHealthRecordsPerOwner {
			index = oldestRoutingHealthForScope(node.RoutingHealth, ownerScope)
			if index < 0 {
				// Every bounded record is protecting an active recovery probe.
				// Dropping one would violate single-flight; omit new soft evidence.
				return
			}
			node.RoutingHealth = append(node.RoutingHealth[:index], node.RoutingHealth[index+1:]...)
		}
		if len(node.RoutingHealth) >= MaximumRoutingHealthRecords {
			oldest := -1
			if ownerScope == "" {
				oldest = oldestOwnerScopedRoutingHealth(node.RoutingHealth)
				if oldest < 0 {
					oldest = oldestRoutingHealthForScope(node.RoutingHealth, "")
				}
			} else {
				oldest = oldestRoutingHealthForScope(node.RoutingHealth, ownerScope)
			}
			if oldest < 0 {
				return
			}
			node.RoutingHealth = append(node.RoutingHealth[:oldest], node.RoutingHealth[oldest+1:]...)
		}
		node.RoutingHealth = append(node.RoutingHealth, RoutingHealth{OwnerScope: ownerScope, RouteKey: routeKey, Provider: provider, Model: model})
		index = len(node.RoutingHealth) - 1
	}
	health := &node.RoutingHealth[index]
	applyRoutingFailure(health, failureCode, now)
}

func validRoutingHealthRouteKey(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func applyRoutingFailure(health *RoutingHealth, failureCode string, now time.Time) {
	if health == nil {
		return
	}
	if health.LastFailureAt.IsZero() || now.Sub(health.LastFailureAt) > routingFailureWindow || now.Before(health.LastFailureAt) {
		health.ConsecutiveFailures = 0
	}
	if health.ConsecutiveFailures < ^uint32(0) {
		health.ConsecutiveFailures++
	}
	health.LastFailureCode = failureCode
	health.LastFailureAt = now
	if health.ConsecutiveFailures >= routingFailureThreshold {
		shift := health.ConsecutiveFailures - routingFailureThreshold
		cooldown := routingInitialCooldown
		for shift > 0 && cooldown < routingMaximumCooldown {
			cooldown *= 2
			shift--
		}
		if cooldown > routingMaximumCooldown {
			cooldown = routingMaximumCooldown
		}
		health.CircuitOpenUntil = now.Add(cooldown)
	}
}

// applyRoutingProbeFailure preserves the fact that a route was already in an
// open-circuit recovery cycle. A failed probe advances that cycle even when
// the previous failure window elapsed during a long cooldown.
func applyRoutingProbeFailure(health *RoutingHealth, failureCode string, now time.Time) {
	if health == nil {
		return
	}
	if health.LastFailureAt.IsZero() || now.Before(health.LastFailureAt) || now.Sub(health.LastFailureAt) > routingFailureWindow {
		health.LastFailureAt = now
	}
	applyRoutingFailure(health, failureCode, now)
}

func routingHealthScopeCount(records []RoutingHealth, ownerScope string) int {
	count := 0
	for _, record := range records {
		if record.OwnerScope == ownerScope {
			count++
		}
	}
	return count
}

func oldestRoutingHealthForScope(records []RoutingHealth, ownerScope string) int {
	oldest := -1
	for i := range records {
		if records[i].OwnerScope != ownerScope || records[i].ProbeJobID != "" {
			continue
		}
		if oldest < 0 || records[i].LastFailureAt.Before(records[oldest].LastFailureAt) {
			oldest = i
		}
	}
	return oldest
}

func oldestOwnerScopedRoutingHealth(records []RoutingHealth) int {
	oldest := -1
	for i := range records {
		if records[i].OwnerScope == "" || records[i].ProbeJobID != "" {
			continue
		}
		if oldest < 0 || records[i].LastFailureAt.Before(records[oldest].LastFailureAt) {
			oldest = i
		}
	}
	return oldest
}

func boundedRoutingHealth(records []RoutingHealth) []RoutingHealth {
	if len(records) > MaximumRoutingHealthRecords {
		records = records[len(records)-MaximumRoutingHealthRecords:]
	}
	return append([]RoutingHealth(nil), records...)
}
