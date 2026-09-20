package bridge

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

const (
	maximumPoolSummaryBytes = 8 << 20
	maximumPoolNodes        = 10000
)

// poolSummary is intentionally aggregate-only. The adapter can explain the
// usable pool without receiving node names, identifiers, relay locations, or
// credentials.
type poolSummary struct {
	NodesOnline    int    `json:"nodes_online"`
	NodesTotal     int    `json:"nodes_total"`
	SlotsBusy      int    `json:"slots_busy"`
	SlotsTotal     int    `json:"slots_total"`
	CPUCores       int    `json:"cpu_cores"`
	MemoryFree     uint64 `json:"memory_free_bytes"`
	MemoryTotal    uint64 `json:"memory_total_bytes"`
	GPUs           int    `json:"gpus"`
	VRAMFree       uint64 `json:"vram_free_bytes"`
	VRAMTotal      uint64 `json:"vram_total_bytes"`
	ZeroGPUNodes   int    `json:"zero_gpu_nodes"`
	CapabilityView bool   `json:"capability_view"`
}

func summarizePool(nodes []cluster.Node) poolSummary {
	if len(nodes) > maximumPoolNodes {
		nodes = nodes[:maximumPoolNodes]
	}
	result := poolSummary{NodesTotal: len(nodes), CapabilityView: true}
	for _, node := range nodes {
		if !node.Connected {
			continue
		}
		result.NodesOnline = saturatingPoolIntAdd(result.NodesOnline, 1)
		result.SlotsBusy = saturatingPoolIntAdd(result.SlotsBusy, max(0, node.Capabilities.Running))
		result.SlotsTotal = saturatingPoolIntAdd(result.SlotsTotal, max(0, node.Capabilities.MaxConcurrent))
		result.CPUCores = saturatingPoolIntAdd(result.CPUCores, max(0, node.Capabilities.CPUCores))
		result.MemoryFree = saturatingMetricAdd(result.MemoryFree, min(node.Capabilities.MemoryFree, node.Capabilities.MemoryTotal))
		result.MemoryTotal = saturatingMetricAdd(result.MemoryTotal, node.Capabilities.MemoryTotal)
		if len(node.Capabilities.GPUs) == 0 {
			result.ZeroGPUNodes = saturatingPoolIntAdd(result.ZeroGPUNodes, 1)
		}
		for _, gpu := range node.Capabilities.GPUs {
			result.GPUs = saturatingPoolIntAdd(result.GPUs, 1)
			result.VRAMFree = saturatingMetricAdd(result.VRAMFree, min(gpu.MemoryFree, gpu.MemoryTotal))
			result.VRAMTotal = saturatingMetricAdd(result.VRAMTotal, gpu.MemoryTotal)
		}
	}
	return result
}

func (s *Server) poolReadTarget() (string, string) {
	token := strings.TrimSpace(s.cfg.Cluster.ClientToken)
	if token == "" {
		token = strings.TrimSpace(s.cfg.Cluster.Relay.AdminToken)
	}
	base := strings.TrimRight(strings.TrimSpace(s.cfg.Cluster.Relay.PublicURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(s.cfg.Cluster.Worker.RelayURL), "/")
	}
	if base == "" && s.cfg.Cluster.Relay.Enabled && strings.TrimSpace(s.cfg.Cluster.Relay.Listen) != "" {
		base = "http://" + strings.TrimSpace(s.cfg.Cluster.Relay.Listen)
	}
	return base, token
}

func (s *Server) readPoolSummary(parent context.Context) interface{} {
	base, token := s.poolReadTarget()
	if base == "" || token == "" {
		return nil
	}
	s.poolSummaryMu.Lock()
	defer s.poolSummaryMu.Unlock()
	if s.poolSummary != nil && time.Since(s.poolSummaryAt) < 10*time.Second {
		return s.poolSummary
	}
	ctx, cancel := context.WithTimeout(parent, 700*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/cluster/nodes", nil)
	if err != nil {
		return s.poolSummary
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return s.poolSummary
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return s.poolSummary
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maximumPoolSummaryBytes+1))
	if err != nil || len(raw) > maximumPoolSummaryBytes {
		return s.poolSummary
	}
	var nodes []cluster.Node
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) > maximumPoolNodes {
		return s.poolSummary
	}
	result := summarizePool(nodes)
	s.poolSummary = result
	s.poolSummaryAt = time.Now()
	return result
}

func saturatingPoolIntAdd(left, right int) int {
	if right <= 0 {
		return left
	}
	if left > math.MaxInt-right {
		return math.MaxInt
	}
	return left + right
}
