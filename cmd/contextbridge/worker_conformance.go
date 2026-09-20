package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const workerConformanceV1 = "contextbridge.worker-conformance.v1"

var conformanceVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type workerConformanceReport struct {
	Schema      string                  `json:"schema"`
	Compatible  bool                    `json:"compatible"`
	DurationMS  int64                   `json:"duration_ms"`
	GeneratedAt time.Time               `json:"generated_at"`
	Checks      []relayConformanceCheck `json:"relay_checks"`
	Workers     []workerConformanceNode `json:"workers"`
}

type workerConformanceNode struct {
	NodeRef       string                   `json:"node_ref"`
	AgentVersion  string                   `json:"agent_version,omitempty"`
	OS            string                   `json:"os,omitempty"`
	Architecture  string                   `json:"architecture,omitempty"`
	EvidenceAgeMS int64                    `json:"evidence_age_ms"`
	Summary       workerConformanceSummary `json:"summary"`
	Checks        []relayConformanceCheck  `json:"checks"`
}

type workerConformanceSummary struct {
	Slots           int `json:"slots"`
	Running         int `json:"running"`
	Providers       int `json:"providers"`
	Tasks           int `json:"tasks"`
	Models          int `json:"models"`
	GPUs            int `json:"gpus"`
	AdapterSessions int `json:"adapter_sessions"`
}

func clusterWorkerConformanceCommand(args []string) error {
	flags := flag.NewFlagSet("cluster conformance worker", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer/admin/observer token")
	node := flags.String("node", "", "exact worker node ID or unique node name; default checks every connected worker")
	asJSON := flags.Bool("json", false, "print machine-readable conformance report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: contextbridge cluster conformance worker [--config PATH] [--token TOKEN] [--node ID_OR_NAME] [--json]")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	*token = clusterClientToken(cfg, *token)
	if *token == "" {
		return errors.New("worker conformance requires a producer, observer, or admin token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := runWorkerConformance(ctx, clusterBaseURL(cfg), *token, strings.TrimSpace(*node))
	if err != nil {
		return err
	}
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
	} else {
		state := "PASS"
		if !report.Compatible {
			state = "FAIL"
		}
		checkCount := len(report.Checks)
		for _, worker := range report.Workers {
			checkCount += len(worker.Checks)
		}
		fmt.Printf("Worker conformance  [%s]  [%d workers]  [%d checks]  [%d ms]  [no job submitted]\n", state, len(report.Workers), checkCount, report.DurationMS)
		for _, check := range report.Checks {
			printWorkerConformanceCheck("relay", check)
		}
		for _, worker := range report.Workers {
			fmt.Printf("Worker %s  [%s/%s]  [agent %s]  [%d/%d slots]\n", shortWorkerRef(worker.NodeRef), cleanConformanceDetail(worker.OS), cleanConformanceDetail(worker.Architecture), cleanConformanceDetail(worker.AgentVersion), worker.Summary.Running, worker.Summary.Slots)
			for _, check := range worker.Checks {
				printWorkerConformanceCheck("  ", check)
			}
		}
	}
	if !report.Compatible {
		return errors.New("one or more workers do not satisfy Worker Conformance v1")
	}
	return nil
}

func printWorkerConformanceCheck(prefix string, check relayConformanceCheck) {
	marker := "✓"
	if !check.Passed {
		marker = "!"
	}
	fmt.Printf("%s %s %-26s %s\n", marker, prefix, check.ID, check.Detail)
}

func runWorkerConformance(ctx context.Context, baseURL, token, selector string) (workerConformanceReport, error) {
	started := time.Now()
	report := workerConformanceReport{Schema: workerConformanceV1, Compatible: true}
	var protocol cluster.ProtocolManifest
	if err := clusterGET(ctx, strings.TrimRight(baseURL, "/")+"/v1/cluster/protocol", token, &protocol); err != nil {
		return workerConformanceReport{}, fmt.Errorf("read relay protocol manifest: %w", err)
	}
	var overview cluster.Overview
	if err := clusterGET(ctx, strings.TrimRight(baseURL, "/")+"/v1/cluster/overview", token, &overview); err != nil {
		return workerConformanceReport{}, fmt.Errorf("read relay overview: %w", err)
	}
	var nodes []cluster.Node
	if err := clusterGET(ctx, strings.TrimRight(baseURL, "/")+"/v1/cluster/nodes", token, &nodes); err != nil {
		return workerConformanceReport{}, fmt.Errorf("read relay workers: %w", err)
	}

	addRelay := func(id string, passed bool, detail string) {
		report.Checks = append(report.Checks, relayConformanceCheck{ID: id, Passed: passed, Detail: cleanConformanceDetail(detail)})
		if !passed {
			report.Compatible = false
		}
	}
	addRelay("protocol.identity", protocol.Schema == cluster.ProtocolManifestV1 && protocol.WireProtocolVersion == cluster.ProtocolVersion,
		fmt.Sprintf("%s · wire v%d", protocol.Schema, protocol.WireProtocolVersion))
	addRelay("protocol.worker_report", containsExact(protocol.Features, "worker_conformance_report_v1"), "feature advertised")
	addRelay("relay.clock", !overview.GeneratedAt.IsZero(), "relay-generated evidence time")

	selected, err := selectConformanceWorkers(nodes, selector)
	if err != nil {
		return workerConformanceReport{}, err
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	for _, node := range selected {
		worker := evaluateWorkerConformance(node, overview.GeneratedAt)
		for _, check := range worker.Checks {
			if !check.Passed {
				report.Compatible = false
			}
		}
		report.Workers = append(report.Workers, worker)
	}
	report.GeneratedAt = overview.GeneratedAt
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

func selectConformanceWorkers(nodes []cluster.Node, selector string) ([]cluster.Node, error) {
	if selector == "" {
		selected := make([]cluster.Node, 0, len(nodes))
		for _, node := range nodes {
			if node.Connected {
				selected = append(selected, node)
			}
		}
		if len(selected) == 0 {
			return nil, errors.New("worker conformance found no connected workers")
		}
		return selected, nil
	}
	selected := make([]cluster.Node, 0, 1)
	for _, node := range nodes {
		if node.ID == selector || strings.EqualFold(node.Name, selector) {
			selected = append(selected, node)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("worker conformance node selector did not match a known worker")
	}
	if len(selected) > 1 {
		return nil, errors.New("worker conformance node name is ambiguous; use the exact node ID")
	}
	return selected, nil
}

func evaluateWorkerConformance(node cluster.Node, relayTime time.Time) workerConformanceNode {
	digest := sha256.Sum256([]byte(node.ID))
	age := relayTime.Sub(node.LastSeen)
	worker := workerConformanceNode{
		NodeRef: "sha256:" + fmt.Sprintf("%x", digest[:]), AgentVersion: node.Capabilities.AgentVersion,
		OS: node.Capabilities.OS, Architecture: node.Capabilities.Architecture, EvidenceAgeMS: age.Milliseconds(),
		Summary: workerConformanceSummary{
			Slots: node.Capabilities.MaxConcurrent, Running: node.Capabilities.Running,
			Providers: len(node.Capabilities.Providers), Tasks: len(node.Capabilities.Tasks), Models: len(node.Capabilities.Models),
			GPUs: len(node.Capabilities.GPUs), AdapterSessions: len(node.Capabilities.AdapterSessions),
		},
	}
	add := func(id string, passed bool, detail string) {
		worker.Checks = append(worker.Checks, relayConformanceCheck{ID: id, Passed: passed, Detail: cleanConformanceDetail(detail)})
	}

	identityOK := validConformanceLabel(node.ID, 128) && validWorkerPublicKey(node.PublicKey)
	add("identity.pinned_key", identityOK, "bounded node identity · X25519 public key")
	agentVersionOK := validConformanceLabel(node.Capabilities.AgentVersion, 80) && conformanceVersionPattern.MatchString(node.Capabilities.AgentVersion)
	add("agent.version", agentVersionOK, "bounded semantic worker version")
	fresh := node.Connected && node.State == "online" && !node.LastSeen.IsZero() && age >= -5*time.Second && age <= cluster.NodeFreshnessWindow
	add("heartbeat.fresh", fresh, fmt.Sprintf("connected=%t · state=%s · age=%d ms", node.Connected, node.State, age.Milliseconds()))
	clockOK := !node.Capabilities.ClockTime.IsZero() && absInt(node.Capabilities.UTCOffsetSeconds) <= 14*60*60 && absInt64(node.ClockOffsetMS) <= 5*60*1000
	add("clock.bounded", clockOK, fmt.Sprintf("offset=%d ms · utc offset=%d s", node.ClockOffsetMS, node.Capabilities.UTCOffsetSeconds))

	capabilities := node.Capabilities
	capacityOK := capabilities.MaxConcurrent > 0 && capabilities.MaxConcurrent <= cluster.MaximumWorkerConcurrency && capabilities.Running >= 0 && capabilities.Running <= capabilities.MaxConcurrent && capabilities.QueueDepth >= 0 && capabilities.AdapterEndpoints >= 0 && capabilities.AdapterBusy >= 0 && capabilities.AdapterBusy <= capabilities.AdapterEndpoints
	add("capacity.bounds", capacityOK, fmt.Sprintf("running %d/%d · queue %d · adapter %d/%d", capabilities.Running, capabilities.MaxConcurrent, capabilities.QueueDepth, capabilities.AdapterBusy, capabilities.AdapterEndpoints))

	labelsOK := boundedUniqueLabels(capabilities.Providers, 128, 80) && len(capabilities.Providers) > 0 &&
		boundedUniqueLabels(capabilities.Tasks, 128, 80) && len(capabilities.Tasks) > 0 &&
		boundedUniqueLabels(capabilities.Tags, 128, 80) && boundedUniqueLabels(capabilities.Groups, 128, 80) &&
		boundedUniqueLabels(capabilities.Sources, 128, 80) && boundedUniqueLabels(capabilities.Modes, 128, 80)
	add("capabilities.labels", labelsOK, fmt.Sprintf("%d providers · %d tasks · %d modes", len(capabilities.Providers), len(capabilities.Tasks), len(capabilities.Modes)))

	automaticOK := validateAutomaticTasks(capabilities)
	add("routes.automatic", automaticOK, fmt.Sprintf("%d provider route declarations", len(capabilities.AutomaticTasks)))
	modelsOK, verifiedModels := validateWorkerModels(capabilities)
	add("models.evidence", modelsOK, fmt.Sprintf("%d models · %d capability-verified", len(capabilities.Models), verifiedModels))
	hardwareOK := validateWorkerHardware(capabilities)
	add("hardware.bounds", hardwareOK, fmt.Sprintf("%d CPU cores · %d GPU(s)", capabilities.CPUCores, len(capabilities.GPUs)))
	adapterOK := validateWorkerAdapter(capabilities)
	add("adapter.inventory", adapterOK, fmt.Sprintf("%d declared · %d bounded sessions", capabilities.AdapterEndpoints, len(capabilities.AdapterSessions)))
	historyOK := node.JobsFailed <= node.JobsTotal && !math.IsNaN(node.CostUSD) && !math.IsInf(node.CostUSD, 0) && node.CostUSD >= 0 && node.CostKnownJobs+node.CostUnknownJobs <= node.JobsTotal
	add("history.accounting", historyOK, fmt.Sprintf("%d jobs · %d failed · %d known-cost · %d unknown-cost", node.JobsTotal, node.JobsFailed, node.CostKnownJobs, node.CostUnknownJobs))
	return worker
}

func validateAutomaticTasks(capabilities cluster.Capabilities) bool {
	if capabilities.AutomaticTasks == nil {
		return false
	}
	for provider, tasks := range capabilities.AutomaticTasks {
		if !validConformanceLabel(provider, 80) || !containsFoldedValue(capabilities.Providers, provider) || !boundedUniqueLabels(tasks, 128, 80) {
			return false
		}
		for _, task := range tasks {
			if !containsFoldedValue(capabilities.Tasks, task) {
				return false
			}
		}
	}
	return true
}

func validateWorkerModels(capabilities cluster.Capabilities) (bool, int) {
	if len(capabilities.Models) > 1024 {
		return false, 0
	}
	seen := map[string]bool{}
	verified := 0
	for _, model := range capabilities.Models {
		if !validConformanceLabel(model.Name, 200) || !validConformanceLabel(model.Provider, 80) || !containsFoldedValue(capabilities.Providers, model.Provider) || !boundedUniqueLabels(model.Tasks, 128, 80) || model.Size < 0 || model.VRAM < 0 || model.Loaded && !model.Available {
			return false, verified
		}
		key := strings.ToLower(model.Provider) + "\x00" + strings.ToLower(model.Name)
		if seen[key] {
			return false, verified
		}
		seen[key] = true
		if model.CapabilitiesVerified {
			if !validConformanceLabel(model.CapabilitySource, 100) {
				return false, verified
			}
			verified++
		} else if model.CapabilitySource != "" && !validConformanceLabel(model.CapabilitySource, 100) {
			return false, verified
		}
	}
	return true, verified
}

func validateWorkerHardware(capabilities cluster.Capabilities) bool {
	if !validConformanceLabel(capabilities.OS, 80) || !validConformanceLabel(capabilities.Architecture, 40) || !validConformanceLabel(capabilities.CPU, 200) || capabilities.CPUCores <= 0 || capabilities.CPUCores > 4096 || capabilities.CPUUtilization < 0 || capabilities.CPUUtilization > 100 || capabilities.MemoryTotal == 0 || capabilities.MemoryFree > capabilities.MemoryTotal || len(capabilities.GPUs) > 64 {
		return false
	}
	for _, gpu := range capabilities.GPUs {
		if !validConformanceLabel(gpu.Name, 200) || !validConformanceLabel(gpu.Backend, 80) || gpu.MemoryTotal == 0 || gpu.MemoryFree > gpu.MemoryTotal || gpu.Utilization < 0 || gpu.Utilization > 100 || gpu.Temperature < -100 || gpu.Temperature > 250 {
			return false
		}
	}
	return true
}

func validateWorkerAdapter(capabilities cluster.Capabilities) bool {
	if capabilities.AdapterEndpoints != len(capabilities.AdapterSessions) || len(capabilities.AdapterSessions) > cluster.MaximumAdapterSessions {
		return false
	}
	seen := map[int]bool{}
	for _, session := range capabilities.AdapterSessions {
		if session.EndpointID <= 0 || seen[session.EndpointID] || !validConformanceLabel(session.Profile, 80) || !validConformanceLabel(session.State, 80) ||
			!boundedUniqueLabels(session.ModelChoices, cluster.MaximumAdapterModelChoices, cluster.MaximumAdapterChoiceBytes) ||
			!boundedUniqueLabels(session.ReasoningLevels, cluster.MaximumAdapterReasoningLevels, cluster.MaximumAdapterChoiceBytes) ||
			!optionalConformanceLabel(session.CurrentModel, cluster.MaximumAdapterChoiceBytes) || !optionalConformanceLabel(session.CurrentReasoning, cluster.MaximumAdapterChoiceBytes) {
			return false
		}
		seen[session.EndpointID] = true
	}
	return true
}

func validWorkerPublicKey(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32
}

func boundedUniqueLabels(values []string, maximumCount, maximumBytes int) bool {
	if len(values) > maximumCount {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !validConformanceLabel(value, maximumBytes) || seen[strings.ToLower(value)] {
			return false
		}
		seen[strings.ToLower(value)] = true
	}
	return true
}

func optionalConformanceLabel(value string, maximum int) bool {
	return value == "" || validConformanceLabel(value, maximum)
}

func validConformanceLabel(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && strings.TrimSpace(value) == value && strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp)
	}) < 0
}

func containsFoldedValue(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func shortWorkerRef(reference string) string {
	if len(reference) <= len("sha256:")+12 {
		return reference
	}
	return reference[:len("sha256:")+12] + "…"
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
