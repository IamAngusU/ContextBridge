package cluster

import (
	"crypto/sha256"
	"encoding/hex"
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

func routingHealthKey(requirements Requirements) (string, string) {
	return normalizedRoutingHealthLabel(requirements.Provider), normalizedRoutingHealthLabel(requirements.Model)
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
	return routingHealthForOwner(node, requirements, "")
}

func routingHealthForOwner(node Node, requirements Requirements, ownerSubject string) (RoutingHealth, bool) {
	provider, model := routingHealthKey(requirements)
	ownerScope := routingHealthOwnerScope(ownerSubject)
	var combined RoutingHealth
	found := false
	for _, health := range node.RoutingHealth {
		if health.Provider != provider || health.Model != model {
			continue
		}
		if health.OwnerScope != "" && (ownerScope == "" || health.OwnerScope != ownerScope) {
			continue
		}
		if !found {
			combined = health
			combined.OwnerScope = ""
			found = true
			continue
		}
		if health.ConsecutiveFailures > combined.ConsecutiveFailures {
			combined.ConsecutiveFailures = health.ConsecutiveFailures
		}
		if health.LastFailureAt.After(combined.LastFailureAt) {
			combined.LastFailureAt = health.LastFailureAt
			combined.LastFailureCode = health.LastFailureCode
		}
		if health.CircuitOpenUntil.After(combined.CircuitOpenUntil) {
			combined.CircuitOpenUntil = health.CircuitOpenUntil
		}
	}
	return combined, found
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
	if node == nil {
		return
	}
	provider, model := routingHealthKey(requirements)
	ownerScope := routingHealthOwnerScope(ownerSubject)
	index := -1
	for i := range node.RoutingHealth {
		if node.RoutingHealth[i].OwnerScope == ownerScope && node.RoutingHealth[i].Provider == provider && node.RoutingHealth[i].Model == model {
			index = i
			break
		}
	}
	if success {
		kept := node.RoutingHealth[:0]
		for _, health := range node.RoutingHealth {
			matchingRoute := health.Provider == provider && health.Model == model
			matchingScope := health.OwnerScope == ownerScope || (ownerScope != "" && health.OwnerScope == "")
			if matchingRoute && matchingScope {
				continue
			}
			kept = append(kept, health)
		}
		node.RoutingHealth = kept
		return
	}
	if !trackRoutingFailure(failureCode) {
		return
	}
	if index < 0 {
		if ownerScope != "" && routingHealthScopeCount(node.RoutingHealth, ownerScope) >= MaximumRoutingHealthRecordsPerOwner {
			index = oldestRoutingHealthForScope(node.RoutingHealth, ownerScope)
			if index >= 0 {
				node.RoutingHealth = append(node.RoutingHealth[:index], node.RoutingHealth[index+1:]...)
			}
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
		node.RoutingHealth = append(node.RoutingHealth, RoutingHealth{OwnerScope: ownerScope, Provider: provider, Model: model})
		index = len(node.RoutingHealth) - 1
	}
	health := &node.RoutingHealth[index]
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
		if records[i].OwnerScope != ownerScope {
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
		if records[i].OwnerScope == "" {
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
