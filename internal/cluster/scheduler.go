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

func Rank(nodes []Node, requirements Requirements) []Candidate {
	return RankWithEstimate(nodes, requirements, 0)
}

// RankWithEstimate treats measured VRAM use as a placement hint, never as a
// hard GPU requirement. This keeps CPU-only (zero-GPU) workers eligible unless
// the producer explicitly sets min_free_vram_bytes.
func RankWithEstimate(nodes []Node, requirements Requirements, estimatedVRAM uint64) []Candidate {
	candidates := make([]Candidate, 0, len(nodes))
	for _, node := range nodes {
		if !node.Connected || time.Since(node.LastSeen) > 30*time.Second || !matchesNode(node, requirements) {
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
		memoryPressure := 0.0
		if node.Capabilities.MemoryTotal > 0 {
			memoryPressure = 1 - float64(node.Capabilities.MemoryFree)/float64(node.Capabilities.MemoryTotal)
		}
		vramHeadroom := bestVRAMHeadroom(node, requirements.MinFreeVRAM)
		score := busy*60 + queue*20 + memoryPressure*10 - vramHeadroom*12
		if requirements.MinFreeVRAM == 0 && estimatedVRAM > 0 {
			if hasVRAM(node, estimatedVRAM) {
				score -= 10
			} else {
				// CPU fallback remains valid but loses to a GPU with enough measured
				// headroom when the other load signals are similar.
				score += 8
			}
		}
		if contains(requirements.PreferredNodes, node.ID) || contains(requirements.PreferredNodes, node.Name) {
			score -= 25
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
		if gpu.MemoryFree >= required {
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
	if requirements.Task != "" && !containsFold(capability.Tasks, requirements.Task) && !modelSupports(capability.Models, requirements) {
		return false
	}
	if requirements.Model != "" && !hasModel(capability.Models, requirements.Model) {
		return false
	}
	if requirements.Vision && !modelFeature(capability.Models, true, false) {
		return false
	}
	if requirements.Embedding && !modelFeature(capability.Models, false, true) {
		return false
	}
	if requirements.MinFreeVRAM > 0 {
		found := false
		for _, gpu := range capability.GPUs {
			if gpu.MemoryFree >= requirements.MinFreeVRAM {
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

func modelSupports(models []ModelCapability, requirements Requirements) bool {
	for _, model := range models {
		if requirements.Task != "" && containsFold(model.Tasks, requirements.Task) {
			return true
		}
	}
	return false
}

func hasModel(models []ModelCapability, name string) bool {
	for _, model := range models {
		if strings.EqualFold(model.Name, name) {
			return true
		}
	}
	return false
}

func modelFeature(models []ModelCapability, vision, embedding bool) bool {
	for _, model := range models {
		if (!vision || model.Vision) && (!embedding || model.Embedding) {
			return true
		}
	}
	return false
}

func bestVRAMHeadroom(node Node, required uint64) float64 {
	best := 0.0
	for _, gpu := range node.Capabilities.GPUs {
		if gpu.MemoryTotal == 0 || gpu.MemoryFree < required {
			continue
		}
		headroom := float64(gpu.MemoryFree-required) / float64(gpu.MemoryTotal)
		if headroom > best {
			best = headroom
		}
	}
	return best
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
