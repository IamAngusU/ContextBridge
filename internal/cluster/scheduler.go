package cluster

import (
	"math"
	"sort"
	"strings"
	"time"
)

type Candidate struct {
	Node  Node    `json:"node"`
	Score float64 `json:"score"`
}

// NodeFreshnessWindow is the hard scheduler boundary for worker telemetry.
// Readiness tools must use the same value so they cannot report capacity that
// the scheduler itself would reject.
const NodeFreshnessWindow = 30 * time.Second

func Rank(nodes []Node, requirements Requirements) []Candidate {
	return RankWithEstimate(nodes, requirements, 0)
}

// RankWithEstimate treats measured VRAM use as a placement hint, never as a
// hard GPU requirement. This keeps CPU-only (zero-GPU) workers eligible unless
// the producer explicitly sets min_free_vram_bytes.
func RankWithEstimate(nodes []Node, requirements Requirements, estimatedVRAM uint64) []Candidate {
	candidates := make([]Candidate, 0, len(nodes))
	for _, node := range nodes {
		if !node.Connected || time.Since(node.LastSeen) > NodeFreshnessWindow || !matchesNode(node, requirements) {
			continue
		}
		capacity := node.Capabilities.MaxConcurrent
		if capacity <= 0 {
			capacity = 1
		}
		if node.Capabilities.Running >= capacity {
			continue
		}
		busy := float64(node.Capabilities.Running) / float64(capacity)
		queue := float64(node.Capabilities.QueueDepth) / float64(capacity)
		memoryPressure := boundedMemoryPressure(node.Capabilities.MemoryFree, node.Capabilities.MemoryTotal)
		placementVRAM := requirements.MinFreeVRAM
		if placementVRAM == 0 {
			placementVRAM = estimatedVRAM
		}
		vramHeadroom := bestVRAMHeadroom(node, placementVRAM)
		gpuPressure := bestGPUUtilization(node, placementVRAM)
		cpuPressure := boundedUtilization(node.Capabilities.CPUUtilization)
		score := busy*60 + queue*20 + memoryPressure*10 + cpuPressure*10 + gpuPressure*15 - vramHeadroom*12
		if strings.EqualFold(requirements.Provider, "browser") && node.Capabilities.BrowserTabs > 0 {
			score += float64(node.Capabilities.BrowserBusy) / float64(node.Capabilities.BrowserTabs) * 40
		}
		if requirements.MinFreeVRAM == 0 && estimatedVRAM > 0 {
			if hasVRAM(node, estimatedVRAM) {
				score -= 10
			} else {
				// CPU fallback remains valid but loses to a GPU with enough measured
				// headroom when the other load signals are similar.
				score += 8
			}
		}
		for index, preferred := range requirements.PreferredNodes {
			if preferred == node.ID || preferred == node.Name {
				// The first preference is strongest. Relay-injected session
				// affinity therefore wins ties without becoming a hard lock.
				score -= math.Max(10, 30-float64(index)*3)
				break
			}
		}
		candidates = append(candidates, Candidate{Node: node, Score: score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].Score-candidates[j].Score) > 0.0001 {
			return candidates[i].Score < candidates[j].Score
		}
		return candidates[i].Node.ID < candidates[j].Node.ID
	})
	return candidates
}

func hasVRAM(node Node, required uint64) bool {
	for _, gpu := range node.Capabilities.GPUs {
		if boundedFreeMemory(gpu.MemoryFree, gpu.MemoryTotal) >= required {
			return true
		}
	}
	return false
}

func matchesNode(node Node, requirements Requirements) bool {
	capability := node.Capabilities
	if requirements.Group != "" && !containsFold(capability.Groups, requirements.Group) {
		return false
	}
	for _, tag := range requirements.RequiredTags {
		if !containsFold(capability.Tags, tag) {
			return false
		}
	}
	if requirements.Provider != "" && !containsFold(capability.Providers, requirements.Provider) {
		return false
	}
	if requirements.BrowserProfile != "" {
		if !strings.EqualFold(requirements.Provider, "browser") || !hasReadyBrowserProfile(capability.BrowserSessions, requirements.BrowserProfile) {
			return false
		}
	}
	if strings.EqualFold(requirements.Provider, "browser") && ((capability.BrowserTabs > 0 && capability.BrowserBusy >= capability.BrowserTabs) || (capability.AutomaticTasks != nil && capability.BrowserTabs <= 0)) {
		return false
	}
	explicitModel := strings.TrimSpace(requirements.Model) != "" && !strings.EqualFold(strings.TrimSpace(requirements.Model), "auto")
	if explicitModel {
		if !selectedModelSupports(capability.Models, requirements) {
			return false
		}
	} else if requirements.Task != "" {
		automaticTasksAreAuthoritative := strings.TrimSpace(requirements.Provider) != "" && capability.AutomaticTasks != nil
		if automaticTasksAreAuthoritative && !providerTaskSupported(capability.AutomaticTasks, requirements.Provider, requirements.Task) {
			return false
		}
		modelsAreAuthoritative := hasProviderModelInventory(capability.Models, requirements.Provider)
		if modelsAreAuthoritative && !modelSupports(capability.Models, requirements) {
			return false
		}
		if !modelsAreAuthoritative && (requirements.Vision || requirements.Embedding) && !modelSupports(capability.Models, requirements) {
			return false
		}
		if !automaticTasksAreAuthoritative && !modelsAreAuthoritative && !requirements.Vision && !requirements.Embedding && !containsFold(capability.Tasks, requirements.Task) && !modelSupports(capability.Models, requirements) {
			return false
		}
	} else if (requirements.Vision || requirements.Embedding) && !modelFeature(capability.Models, requirements.Model, requirements.Provider, requirements.Vision, requirements.Embedding) {
		return false
	}
	if requirements.MinFreeVRAM > 0 {
		found := false
		for _, gpu := range capability.GPUs {
			if boundedFreeMemory(gpu.MemoryFree, gpu.MemoryTotal) >= requirements.MinFreeVRAM {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func providerTaskSupported(tasks map[string][]string, provider, task string) bool {
	for name, advertised := range tasks {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(provider)) {
			return containsFold(advertised, task)
		}
	}
	return false
}

func hasReadyBrowserProfile(sessions []BrowserSessionCapability, wanted string) bool {
	for _, session := range sessions {
		if strings.EqualFold(strings.TrimSpace(session.Profile), strings.TrimSpace(wanted)) && strings.EqualFold(strings.TrimSpace(session.State), "waiting") {
			return true
		}
	}
	return false
}

func modelSupports(models []ModelCapability, requirements Requirements) bool {
	for _, model := range models {
		if modelMatchesProvider(model, requirements.Provider) && requirements.Task != "" && containsFold(model.Tasks, requirements.Task) && (!requirements.Vision || model.Vision) && (!requirements.Embedding || model.Embedding) {
			return true
		}
	}
	return false
}

func hasProviderModelInventory(models []ModelCapability, provider string) bool {
	if strings.TrimSpace(provider) == "" {
		return false
	}
	for _, model := range models {
		if strings.TrimSpace(model.Provider) != "" && strings.EqualFold(model.Provider, provider) {
			return true
		}
	}
	return false
}

func selectedModelSupports(models []ModelCapability, requirements Requirements) bool {
	for _, model := range models {
		if !modelMatchesProvider(model, requirements.Provider) {
			continue
		}
		matches := strings.EqualFold(model.Name, requirements.Model)
		if strings.EqualFold(requirements.Provider, "browser") {
			matches = browserModelEqual(model.Name, requirements.Model)
		}
		if matches && (requirements.Task == "" || containsFold(model.Tasks, requirements.Task)) && (!requirements.Vision || model.Vision) && (!requirements.Embedding || model.Embedding) {
			return true
		}
	}
	return false
}

// Websites may use non-breaking spaces in labels that users naturally type
// with regular spaces. Keep local-model identifiers exact; only browser labels
// get whitespace folding, so an allowlist cannot accidentally admit a
// different Ollama/ComfyUI model name.
func browserModelEqual(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
}

func containsBrowserModel(values []string, wanted string) bool {
	for _, value := range values {
		if browserModelEqual(value, wanted) {
			return true
		}
	}
	return false
}

func modelFeature(models []ModelCapability, name, provider string, vision, embedding bool) bool {
	for _, model := range models {
		if !modelMatchesProvider(model, provider) {
			continue
		}
		if name != "" {
			matches := strings.EqualFold(model.Name, name)
			if strings.EqualFold(provider, "browser") {
				matches = browserModelEqual(model.Name, name)
			}
			if !matches {
				continue
			}
		}
		if (!vision || model.Vision) && (!embedding || model.Embedding) {
			return true
		}
	}
	return false
}

func modelMatchesProvider(model ModelCapability, provider string) bool {
	return provider == "" || strings.EqualFold(model.Provider, provider)
}

func bestVRAMHeadroom(node Node, required uint64) float64 {
	best := 0.0
	for _, gpu := range node.Capabilities.GPUs {
		free := boundedFreeMemory(gpu.MemoryFree, gpu.MemoryTotal)
		if gpu.MemoryTotal == 0 || free < required {
			continue
		}
		headroom := float64(free-required) / float64(gpu.MemoryTotal)
		if headroom > best {
			best = headroom
		}
	}
	return best
}

func bestGPUUtilization(node Node, required uint64) float64 {
	best := 1.0
	found := false
	for _, gpu := range node.Capabilities.GPUs {
		if boundedFreeMemory(gpu.MemoryFree, gpu.MemoryTotal) < required {
			continue
		}
		utilization := boundedUtilization(gpu.Utilization)
		if !found || utilization < best {
			best = utilization
			found = true
		}
	}
	if !found {
		return 0
	}
	return best
}

// Worker telemetry is a routing hint, not trusted input. Bound impossible or
// stale samples so they cannot give a node a negative load score or unbounded
// preference. Unknown totals remain neutral rather than looking full.
func boundedMemoryPressure(free, total uint64) float64 {
	if total == 0 || free >= total {
		return 0
	}
	return 1 - float64(free)/float64(total)
}

func boundedFreeMemory(free, total uint64) uint64 {
	if total > 0 && free > total {
		return total
	}
	return free
}

func boundedUtilization(value int) float64 {
	if value <= 0 {
		return 0
	}
	if value >= 100 {
		return 1
	}
	return float64(value) / 100
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
