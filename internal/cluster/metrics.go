package cluster

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleMetrics exposes only fixed-cardinality, pool-level operational data.
// It deliberately omits job, tenant, model, provider, prompt, and node labels:
// those are either sensitive, attacker-controlled, or unbounded. Access is
// restricted by Handler to administrators and read-only observers.
func (r *Relay) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	retainedJobs, retainedUsage, err := r.store.retainedJobMetrics()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	nodes, err := r.store.ListNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	var nodesOnline, draining, slotsTotal, slotsBusy, circuitsOpen, circuitsProbation uint64
	for _, node := range nodes {
		if node.Draining {
			draining++
		}
		age := now.Sub(node.LastSeen)
		if age < 0 {
			age = 0
		}
		if node.Connected && age <= NodeFreshnessWindow {
			nodesOnline = saturatingUint64Add(nodesOnline, 1)
			capacity := boundedWorkerCapacity(node.Capabilities.MaxConcurrent)
			running := node.Capabilities.Running
			if running < 0 {
				running = 0
			}
			if running > capacity {
				running = capacity
			}
			slotsTotal = saturatingUint64Add(slotsTotal, boundedMetricSlotCount(capacity))
			slotsBusy = saturatingUint64Add(slotsBusy, boundedMetricSlotCount(running))
		}
		for _, health := range node.RoutingHealth {
			if !validRoutingHealthRouteKey(health.RouteKey) {
				continue
			}
			if health.CircuitOpenUntil.After(now) {
				circuitsOpen = saturatingUint64Add(circuitsOpen, 1)
			} else if routingHealthState(health, now) == 2 {
				circuitsProbation = saturatingUint64Add(circuitsProbation, 1)
			}
		}
	}

	var output strings.Builder
	writeMetricHelp(&output, "contextbridge_nodes", "Relay-known worker nodes by fixed state.", "gauge")
	fmt.Fprintf(&output, "contextbridge_nodes{state=\"total\"} %d\n", len(nodes))
	fmt.Fprintf(&output, "contextbridge_nodes{state=\"online\"} %d\n", nodesOnline)
	fmt.Fprintf(&output, "contextbridge_nodes{state=\"draining\"} %d\n", draining)
	writeMetricHelp(&output, "contextbridge_worker_slots", "Worker execution slots by fixed state.", "gauge")
	fmt.Fprintf(&output, "contextbridge_worker_slots{state=\"total\"} %d\n", slotsTotal)
	fmt.Fprintf(&output, "contextbridge_worker_slots{state=\"busy\"} %d\n", slotsBusy)
	writeMetricHelp(&output, "contextbridge_jobs", "Retained jobs by fixed durable state.", "gauge")
	for _, state := range []string{JobQueued, JobAssigned, JobRunning, JobCompleted, JobFailed, JobCancelled} {
		fmt.Fprintf(&output, "contextbridge_jobs{state=\"%s\"} %d\n", state, retainedJobs[state])
	}
	writeMetricHelp(&output, "contextbridge_routing_circuits_open", "Currently open relay-owned routing circuits.", "gauge")
	fmt.Fprintf(&output, "contextbridge_routing_circuits_open %d\n", circuitsOpen)
	writeMetricHelp(&output, "contextbridge_routing_circuits_probation", "Relay-owned routing circuits awaiting or running one recovery probe.", "gauge")
	fmt.Fprintf(&output, "contextbridge_routing_circuits_probation %d\n", circuitsProbation)
	writeMetricHelp(&output, "contextbridge_retained_compute_seconds", "Attributed compute seconds represented by retained aggregate state.", "gauge")
	fmt.Fprintf(&output, "contextbridge_retained_compute_seconds %.3f\n", float64(retainedUsage.ComputeMS)/1000)
	writeMetricHelp(&output, "contextbridge_retained_job_tokens", "Tokens represented by retained aggregate state, by fixed direction.", "gauge")
	fmt.Fprintf(&output, "contextbridge_retained_job_tokens{direction=\"input\"} %d\n", retainedUsage.InputTokens)
	fmt.Fprintf(&output, "contextbridge_retained_job_tokens{direction=\"output\"} %d\n", retainedUsage.OutputTokens)
	fmt.Fprintf(&output, "contextbridge_retained_job_tokens{direction=\"total\"} %d\n", retainedUsage.TotalTokens)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(output.String()))
}

func boundedMetricSlotCount(value int) uint64 {
	if value <= 0 {
		return 0
	}
	if value > MaximumWorkerConcurrency {
		return MaximumWorkerConcurrency
	}
	return uint64(value)
}

func writeMetricHelp(output *strings.Builder, name, help, metricType string) {
	fmt.Fprintf(output, "# HELP %s %s\n", name, help)
	fmt.Fprintf(output, "# TYPE %s %s\n", name, metricType)
}
