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

// RoutingScoreComponents records the additive, bounded inputs used for one
// eligible node. Values are already weighted, so their sum is Score. Keeping
// this separate from raw hardware telemetry makes route explanations useful
// without turning them into another unrestricted node-status response.
type RoutingScoreComponents struct {
	ActiveLoad       float64 `json:"active_load,omitempty"`
	QueueDepth       float64 `json:"queue_depth,omitempty"`
	MemoryPressure   float64 `json:"memory_pressure,omitempty"`
	CPUPressure      float64 `json:"cpu_pressure,omitempty"`
	GPUPressure      float64 `json:"gpu_pressure,omitempty"`
	VRAMHeadroom     float64 `json:"vram_headroom,omitempty"`
	BrowserPressure  float64 `json:"browser_pressure,omitempty"`
	LoadedModel      float64 `json:"loaded_model,omitempty"`
	EstimatedVRAMFit float64 `json:"estimated_vram_fit,omitempty"`
	PreferredNode    float64 `json:"preferred_node,omitempty"`
}

// RoutingCandidateDecision is deliberately smaller than Node. It contains the
// evidence needed to audit a placement decision without copying a worker's
// complete hardware or browser-session inventory into every job.
type RoutingCandidateDecision struct {
	NodeID           string                 `json:"node_id"`
	NodeName         string                 `json:"node_name,omitempty"`
	Eligible         bool                   `json:"eligible"`
	RejectionReasons []string               `json:"rejection_reasons,omitempty"`
	Score            float64                `json:"score,omitempty"`
	ScoreComponents  RoutingScoreComponents `json:"score_components,omitempty"`
	EvidenceAgeMS    int64                  `json:"evidence_age_ms"`
}

// RoutingDecision is a point-in-time explanation. The relay adds ID, JobID,
// and CreatedAt when it persists an actual assignment; previews use the same
// schema but are explicitly marked Preview.
type RoutingDecision struct {
	ID                  string                     `json:"id,omitempty"`
	JobID               string                     `json:"job_id,omitempty"`
	Preview             bool                       `json:"preview,omitempty"`
	Requirements        Requirements               `json:"requirements"`
	EstimatedVRAMBytes  uint64                     `json:"estimated_vram_bytes,omitempty"`
	Candidates          []RoutingCandidateDecision `json:"candidates"`
	CandidateCount      int                        `json:"candidate_count"`
	CandidatesTruncated int                        `json:"candidates_truncated,omitempty"`
	SelectedNodeID      string                     `json:"selected_node_id,omitempty"`
	SelectedNodeName    string                     `json:"selected_node_name,omitempty"`
	CreatedAt           time.Time                  `json:"created_at,omitempty"`
}

const MaximumRoutingDecisionCandidates = 128

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
	candidates, _ := rankWithDecision(nodes, requirements, estimatedVRAM, time.Now().UTC())
	return candidates
}

// ExplainRouting returns the same ranking used by RankWithEstimate together
// with stable reason codes and score components for every bounded candidate.
func ExplainRouting(nodes []Node, requirements Requirements, estimatedVRAM uint64) RoutingDecision {
	_, decision := rankWithDecision(nodes, requirements, estimatedVRAM, time.Now().UTC())
	boundRoutingDecision(&decision)
	return decision
}

func rankWithDecision(nodes []Node, requirements Requirements, estimatedVRAM uint64, now time.Time) ([]Candidate, RoutingDecision) {
	candidates := make([]Candidate, 0, len(nodes))
	decisions := make([]RoutingCandidateDecision, 0, min(len(nodes), MaximumRoutingDecisionCandidates))
	for _, node := range nodes {
		age := now.Sub(node.LastSeen)
		if age < 0 {
			age = 0
		}
		candidateDecision := RoutingCandidateDecision{NodeID: node.ID, NodeName: node.Name, EvidenceAgeMS: age.Milliseconds()}
		reasons := make([]string, 0, 2)
		if !node.Connected {
			reasons = append(reasons, "worker_not_connected")
		}
		if age > NodeFreshnessWindow {
			reasons = append(reasons, "worker_telemetry_stale")
		}
		if !matchesNode(node, requirements) {
			reasons = append(reasons, routingConstraintReasons(node, requirements)...)
			if len(reasons) == 0 {
				reasons = append(reasons, "requirements_not_satisfied")
			}
		}
		capacity := node.Capabilities.MaxConcurrent
		if capacity <= 0 {
			capacity = 1
		}
		if node.Capabilities.Running >= capacity {
			reasons = appendUniqueReason(reasons, "worker_at_capacity")
		}
		if len(reasons) > 0 {
			candidateDecision.RejectionReasons = reasons
			decisions = append(decisions, candidateDecision)
			continue
		}
		candidateDecision.Eligible = true
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
		components := RoutingScoreComponents{
			ActiveLoad: busy * 60, QueueDepth: queue * 20, MemoryPressure: memoryPressure * 10,
			CPUPressure: cpuPressure * 10, GPUPressure: gpuPressure * 15, VRAMHeadroom: -vramHeadroom * 12,
		}
		if strings.EqualFold(requirements.Provider, "browser") && node.Capabilities.BrowserTabs > 0 {
			components.BrowserPressure = float64(node.Capabilities.BrowserBusy) / float64(node.Capabilities.BrowserTabs) * 40
		}
		if isAutomaticOllamaRequest(requirements) && hasLoadedAutomaticModel(node.Capabilities.Models, requirements) {
			// Loading a cold model is valid, but an already loaded compatible
			// model avoids a cold start when otherwise similar workers compete.
			components.LoadedModel = -6
		}
		if requirements.MinFreeVRAM == 0 && estimatedVRAM > 0 {
			if hasVRAM(node, estimatedVRAM) {
				components.EstimatedVRAMFit = -10
			} else {
				// CPU fallback remains valid but loses to a GPU with enough measured
				// headroom when the other load signals are similar.
				components.EstimatedVRAMFit = 8
			}
		}
		for index, preferred := range requirements.PreferredNodes {
			if preferred == node.ID || preferred == node.Name {
				// The first preference is strongest. Relay-injected session
				// affinity therefore wins ties without becoming a hard lock.
				components.PreferredNode = -math.Max(10, 30-float64(index)*3)
				break
			}
		}
		score := components.ActiveLoad + components.QueueDepth + components.MemoryPressure + components.CPUPressure +
			components.GPUPressure + components.VRAMHeadroom + components.BrowserPressure + components.LoadedModel +
			components.EstimatedVRAMFit + components.PreferredNode
		candidates = append(candidates, Candidate{Node: node, Score: score})
		candidateDecision.Score = score
		candidateDecision.ScoreComponents = components
		decisions = append(decisions, candidateDecision)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].Score-candidates[j].Score) > 0.0001 {
			return candidates[i].Score < candidates[j].Score
		}
		return candidates[i].Node.ID < candidates[j].Node.ID
	})
	sortRoutingCandidateDecisions(decisions)
	decision := RoutingDecision{
		Requirements: requirements, EstimatedVRAMBytes: estimatedVRAM, CandidateCount: len(decisions),
		Candidates: decisions, CreatedAt: now,
	}
	if len(candidates) > 0 {
		decision.SelectedNodeID = candidates[0].Node.ID
		decision.SelectedNodeName = candidates[0].Node.Name
	}
	return candidates, decision
}

func boundRoutingDecision(decision *RoutingDecision) {
	if decision == nil {
		return
	}
	if decision.CandidateCount < len(decision.Candidates) {
		decision.CandidateCount = len(decision.Candidates)
	}
	if len(decision.Candidates) > MaximumRoutingDecisionCandidates {
		decision.Candidates = decision.Candidates[:MaximumRoutingDecisionCandidates]
	}
	decision.CandidatesTruncated = decision.CandidateCount - len(decision.Candidates)
}

func sortRoutingCandidateDecisions(candidates []RoutingCandidateDecision) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Eligible != candidates[j].Eligible {
			return candidates[i].Eligible
		}
		if candidates[i].Eligible && math.Abs(candidates[i].Score-candidates[j].Score) > 0.0001 {
			return candidates[i].Score < candidates[j].Score
		}
		return candidates[i].NodeID < candidates[j].NodeID
	})
}

func appendUniqueReason(reasons []string, reason string) []string {
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func routingConstraintReasons(node Node, requirements Requirements) []string {
	capability := node.Capabilities
	reasons := []string{}
	explicitModel := strings.TrimSpace(requirements.Model) != "" && !strings.EqualFold(strings.TrimSpace(requirements.Model), "auto")
	explicitReasoning := strings.TrimSpace(requirements.Reasoning) != ""
	if requirements.Group != "" && !containsFold(capability.Groups, requirements.Group) {
		reasons = append(reasons, "group_scope_unavailable")
	}
	for _, tag := range requirements.RequiredTags {
		if !containsFold(capability.Tags, tag) {
			reasons = appendUniqueReason(reasons, "required_tag_unavailable")
		}
	}
	if requirements.Provider != "" && !containsFold(capability.Providers, requirements.Provider) {
		reasons = append(reasons, "provider_not_available")
	}
	if requirements.BrowserProfile != "" && !strings.EqualFold(requirements.Provider, "browser") {
		reasons = append(reasons, "browser_profile_requires_browser")
	}
	if strings.EqualFold(requirements.Provider, "browser") && len(capability.BrowserSessions) > 0 &&
		(requirements.BrowserProfile != "" || explicitModel || explicitReasoning || requirements.BrowserTabID > 0) {
		if _, ok := selectReadyBrowserSession(capability.BrowserSessions, requirements); !ok {
			reasons = append(reasons, "browser_session_not_ready")
		}
	} else if requirements.BrowserProfile != "" || requirements.BrowserTabID > 0 {
		reasons = append(reasons, "browser_session_evidence_unavailable")
	}
	if strings.EqualFold(requirements.Provider, "browser") && ((capability.BrowserTabs > 0 && capability.BrowserBusy >= capability.BrowserTabs) ||
		(capability.AutomaticTasks != nil && capability.BrowserTabs <= 0)) {
		reasons = append(reasons, "browser_slots_busy")
	}
	if explicitModel {
		if !selectedModelSupports(capability.Models, requirements) {
			reasons = append(reasons, "model_not_available")
		}
	} else if requirements.Task != "" {
		automaticTasksAreAuthoritative := strings.TrimSpace(requirements.Provider) != "" && (capability.AutomaticTasks != nil || isAutomaticOllamaRequest(requirements))
		modelsAreAuthoritative := hasProviderModelInventory(capability.Models, requirements.Provider)
		if automaticTasksAreAuthoritative && !providerTaskSupported(capability.AutomaticTasks, requirements.Provider, requirements.Task) {
			reasons = append(reasons, "task_not_verified")
		}
		if isAutomaticOllamaRequest(requirements) && !modelSupports(capability.Models, requirements) {
			reasons = appendUniqueReason(reasons, "model_not_available")
		}
		if modelsAreAuthoritative && !modelSupports(capability.Models, requirements) {
			reasons = appendUniqueReason(reasons, "task_not_verified")
		}
		if !modelsAreAuthoritative && (requirements.Vision || requirements.Embedding) && !modelSupports(capability.Models, requirements) {
			reasons = append(reasons, "capability_not_available")
		}
		if !automaticTasksAreAuthoritative && !modelsAreAuthoritative && !requirements.Vision && !requirements.Embedding &&
			!containsFold(capability.Tasks, requirements.Task) && !modelSupports(capability.Models, requirements) {
			reasons = appendUniqueReason(reasons, "task_not_verified")
		}
	} else if requirements.Vision || requirements.Embedding {
		if isAutomaticOllamaRequest(requirements) {
			if !modelSupports(capability.Models, requirements) {
				reasons = append(reasons, "capability_not_available")
			}
		} else if !modelFeature(capability.Models, requirements.Model, requirements.Provider, requirements.Vision, requirements.Embedding) {
			reasons = append(reasons, "capability_not_available")
		}
	}
	if requirements.MinFreeVRAM > 0 && !hasVRAM(node, requirements.MinFreeVRAM) {
		reasons = append(reasons, "insufficient_vram")
	}
	return reasons
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
	explicitModel := strings.TrimSpace(requirements.Model) != "" && !strings.EqualFold(strings.TrimSpace(requirements.Model), "auto")
	explicitReasoning := strings.TrimSpace(requirements.Reasoning) != ""
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
	if requirements.BrowserProfile != "" && !strings.EqualFold(requirements.Provider, "browser") {
		return false
	}
	if strings.EqualFold(requirements.Provider, "browser") && len(capability.BrowserSessions) > 0 && (requirements.BrowserProfile != "" || explicitModel || explicitReasoning || requirements.BrowserTabID > 0) {
		if _, ok := selectReadyBrowserSession(capability.BrowserSessions, requirements); !ok {
			return false
		}
	} else if requirements.BrowserProfile != "" || requirements.BrowserTabID > 0 {
		// A profile-specific request has never been safe to place from only the
		// node-wide browser counters. Older workers without session telemetry
		// remain compatible for model-only jobs through the global inventory.
		return false
	}
	if strings.EqualFold(requirements.Provider, "browser") && ((capability.BrowserTabs > 0 && capability.BrowserBusy >= capability.BrowserTabs) || (capability.AutomaticTasks != nil && capability.BrowserTabs <= 0)) {
		return false
	}
	if explicitModel {
		if !selectedModelSupports(capability.Models, requirements) {
			return false
		}
	} else if requirements.Task != "" {
		automaticTasksAreAuthoritative := strings.TrimSpace(requirements.Provider) != "" && (capability.AutomaticTasks != nil || isAutomaticOllamaRequest(requirements))
		if automaticTasksAreAuthoritative && !providerTaskSupported(capability.AutomaticTasks, requirements.Provider, requirements.Task) {
			return false
		}
		if isAutomaticOllamaRequest(requirements) && !modelSupports(capability.Models, requirements) {
			// Automatic Ollama placement always requires a currently available,
			// provider-verified compatible model. An AutomaticTasks entry alone
			// describes route intent, not executable model evidence.
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
	} else if requirements.Vision || requirements.Embedding {
		if isAutomaticOllamaRequest(requirements) {
			if !modelSupports(capability.Models, requirements) {
				return false
			}
		} else if !modelFeature(capability.Models, requirements.Model, requirements.Provider, requirements.Vision, requirements.Embedding) {
			return false
		}
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

func modelIfExplicit(model string, explicit bool) string {
	if !explicit {
		return ""
	}
	return model
}

func selectReadyBrowserSession(sessions []BrowserSessionCapability, requirements Requirements) (BrowserSessionCapability, bool) {
	wantedModel := strings.TrimSpace(requirements.Model)
	if strings.EqualFold(wantedModel, "auto") {
		wantedModel = ""
	}
	wantedReasoning := strings.TrimSpace(requirements.Reasoning)
	wantedSessionKey := cleanBrowserSessionKey(requirements.BrowserSessionKey)
	sessionKeySupported := false
	sessionKeyMatches := 0
	for _, session := range sessions {
		// The opaque selector intentionally has the same owner/session digest
		// across providers. Count routing evidence only inside the requested
		// browser profile, otherwise a legitimate ChatGPT and Gemini session
		// would look like a duplicate claim for either one.
		if session.TabID <= 0 || requirements.BrowserProfile != "" &&
			!strings.EqualFold(strings.TrimSpace(session.Profile), strings.TrimSpace(requirements.BrowserProfile)) {
			continue
		}
		if session.SessionKeySupported {
			sessionKeySupported = true
		}
		if wantedSessionKey != "" && session.SessionKey == wantedSessionKey {
			sessionKeyMatches++
		}
	}
	// Two tabs claiming the same logical browser session is ambiguous routing
	// evidence. The extension normally resolves this in favor of the recorded
	// tab; a compromised or inconsistent heartbeat must fail closed here.
	if sessionKeyMatches > 1 {
		return BrowserSessionCapability{}, false
	}
	authoritativeSessionMatch := wantedSessionKey != "" && sessionKeySupported && sessionKeyMatches == 1
	var selected BrowserSessionCapability
	selectedScore := -1
	requiredTabMatchesSelection := false
	if requirements.BrowserTabID > 0 {
		for _, session := range sessions {
			if session.TabID == requirements.BrowserTabID && browserSessionSelectionMatches(session, requirements, wantedModel, wantedReasoning) &&
				(!authoritativeSessionMatch || session.SessionKey == wantedSessionKey) &&
				!(sessionKeySupported && wantedSessionKey != "" && requirements.BrowserSessionRecovery && session.SessionKey != wantedSessionKey) {
				requiredTabMatchesSelection = true
				break
			}
		}
	}
	for _, session := range sessions {
		state := strings.TrimSpace(session.State)
		keyMatch := authoritativeSessionMatch && session.SessionKey == wantedSessionKey
		if authoritativeSessionMatch && !keyMatch {
			continue
		}
		exactTab := requirements.BrowserTabID > 0 && session.TabID == requirements.BrowserTabID &&
			!(sessionKeySupported && wantedSessionKey != "" && requirements.BrowserSessionRecovery && session.SessionKey != wantedSessionKey)
		allowRecoveryTab := requirements.BrowserSessionRecovery && (requirements.BrowserTabID == 0 || !requiredTabMatchesSelection)
		if requirements.BrowserProfile != "" && !strings.EqualFold(strings.TrimSpace(session.Profile), strings.TrimSpace(requirements.BrowserProfile)) {
			continue
		}
		if wantedModel != "" && !browserModelEqual(session.CurrentModel, wantedModel) && !containsBrowserModel(session.ModelChoices, wantedModel) {
			continue
		}
		if wantedReasoning != "" && !browserModelEqual(session.CurrentReasoning, wantedReasoning) && !containsBrowserModel(session.ReasoningLevels, wantedReasoning) {
			continue
		}
		if requirements.BrowserTabID > 0 && !exactTab && !allowRecoveryTab && !keyMatch {
			continue
		}
		freshLauncher := !keyMatch && requirements.BrowserTabID == 0 && session.CanCreateFreshChat && (requirements.BrowserFreshChat || session.DefaultFreshChat)
		switch {
		case keyMatch:
			if !strings.EqualFold(state, "waiting") && !strings.EqualFold(state, "session_bound") {
				continue
			}
		case exactTab:
			if !strings.EqualFold(state, "waiting") && !strings.EqualFold(state, "session_bound") {
				continue
			}
		case allowRecoveryTab:
			if !strings.EqualFold(state, "waiting") && !(freshLauncher && strings.EqualFold(state, "session_bound")) {
				continue
			}
		case strings.EqualFold(state, "waiting"):
			if (requirements.BrowserFreshChat || session.DefaultFreshChat) && !freshLauncher {
				continue
			}
		case freshLauncher && strings.EqualFold(state, "session_bound"):
			// A bound tab is only a launcher. The extension creates and proves a
			// separate fresh conversation before any prompt is sent.
		default:
			continue
		}
		score := 0
		if keyMatch {
			score += 1000
		}
		if exactTab {
			score += 100
		}
		if strings.EqualFold(state, "waiting") {
			score += 10
		}
		if wantedModel != "" && browserModelEqual(session.CurrentModel, wantedModel) {
			score += 2
		}
		if wantedReasoning != "" && browserModelEqual(session.CurrentReasoning, wantedReasoning) {
			score++
		}
		if selectedScore < score || selectedScore == score && (selected.TabID == 0 || session.TabID < selected.TabID) {
			selected, selectedScore = session, score
		}
	}
	return selected, selectedScore >= 0
}

func browserSessionSelectionMatches(session BrowserSessionCapability, requirements Requirements, wantedModel, wantedReasoning string) bool {
	if requirements.BrowserProfile != "" && !strings.EqualFold(strings.TrimSpace(session.Profile), strings.TrimSpace(requirements.BrowserProfile)) {
		return false
	}
	if wantedModel != "" && !browserModelEqual(session.CurrentModel, wantedModel) && !containsBrowserModel(session.ModelChoices, wantedModel) {
		return false
	}
	if wantedReasoning != "" && !browserModelEqual(session.CurrentReasoning, wantedReasoning) && !containsBrowserModel(session.ReasoningLevels, wantedReasoning) {
		return false
	}
	return true
}

func modelSupports(models []ModelCapability, requirements Requirements) bool {
	for _, model := range models {
		if isAutomaticOllamaRequest(requirements) && (!model.Available || !model.CapabilitiesVerified) {
			continue
		}
		if modelMatchesProvider(model, requirements.Provider) && (requirements.Task == "" || containsFold(model.Tasks, requirements.Task)) && (!requirements.Vision || model.Vision) && (!requirements.Embedding || model.Embedding) {
			return true
		}
	}
	return false
}

func isAutomaticOllamaRequest(requirements Requirements) bool {
	return strings.EqualFold(strings.TrimSpace(requirements.Provider), "ollama") &&
		(strings.TrimSpace(requirements.Model) == "" || strings.EqualFold(strings.TrimSpace(requirements.Model), "auto"))
}

func hasLoadedAutomaticModel(models []ModelCapability, requirements Requirements) bool {
	if !isAutomaticOllamaRequest(requirements) {
		return false
	}
	for _, model := range models {
		if !model.Loaded || !model.Available || !model.CapabilitiesVerified || !modelMatchesProvider(model, requirements.Provider) {
			continue
		}
		if requirements.Task != "" && !containsFold(model.Tasks, requirements.Task) {
			continue
		}
		if (!requirements.Vision || model.Vision) && (!requirements.Embedding || model.Embedding) {
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
		if !matches {
			continue
		}
		if strings.EqualFold(requirements.Provider, "ollama") && !model.CapabilitiesVerified && strings.EqualFold(strings.TrimSpace(model.CapabilitySource), "name_inference") {
			// A fixed Ollama model is an explicit operator choice. Availability
			// evidence may exist on daemons that cannot report capabilities; in
			// that case do not turn a name inference into an incompatibility.
			// Automatic selection follows the stricter modelSupports path.
			return model.Available
		}
		if (requirements.Task == "" || containsFold(model.Tasks, requirements.Task)) && (!requirements.Vision || model.Vision) && (!requirements.Embedding || model.Embedding) {
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
