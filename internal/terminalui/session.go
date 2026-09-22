package terminalui

import (
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
)

const (
	ansiReset               = "\x1b[0m"
	ansiCyan                = "\x1b[36m"
	ansiGreen               = "\x1b[32m"
	ansiYellow              = "\x1b[33m"
	ansiRed                 = "\x1b[31m"
	ansiDim                 = "\x1b[2m"
	ansiOrange              = "\x1b[38;5;208m"
	ansiPurple              = "\x1b[35m"
	ansiBlue                = "\x1b[34m"
	maximumVisiblePoolNodes = 8
)

type jobState struct {
	task      string
	phase     string
	detail    string
	percent   int
	sequence  uint64
	preview   string
	selection string
	started   time.Time
}

type adapterSelection struct {
	profile   string
	model     string
	reasoning string
	state     string
}

type localModelSelection struct {
	provider string
	name     string
	loaded   bool
}

type nodeDetailVisibility struct {
	gpus   bool
	models bool
}

// PoolNode is deliberately limited to public display metadata. Keys, tokens,
// addresses and job payloads from the relay never enter terminal history.
type PoolNode struct {
	ID, Name       string
	Connected      bool
	Running, Slots int
	GPUs           []cluster.GPUCapability
	Models         []cluster.ModelCapability
}

// ServiceSnapshot is the read-only subset shown by an attached console. It
// reflects a service status response, not inferred worker or job events.
type ServiceSnapshot struct {
	Version          string
	Queued           int
	Completed        int
	ActiveJobs       int
	AdapterConnected bool
	ActiveEndpoints  int
	BusyEndpoints    int
	Endpoints        []cluster.AdapterSessionCapability
	LocalModels      []cluster.ModelCapability
	LocalProviders   []string
	APIModels        []cluster.ModelCapability
	APIProviders     []string
	ResourcePacks    []resourcepacks.Pack
	JobsTotal        uint64
	JobsFailed       uint64
	GPU              string
	GPUUtilization   int
}

// Session renders an animated single-line status in a real terminal and
// concise transition logs when stdout is redirected to a service log.
type Session struct {
	out                io.Writer
	console            *os.File
	interactive        bool
	style              string
	section            string
	nextSection        string
	mu                 sync.Mutex
	partial            string
	status             string
	statusSince        time.Time
	frame              int
	retries            int
	jobs               map[string]jobState
	node               string
	nodeID             string
	slots              int
	hardware           string
	capabilities       cluster.Capabilities
	adapterSelections  map[int]adapterSelection
	localModels        map[string]localModelSelection
	localProviders     []string
	serviceLines       []string
	connectionLine     string
	relayHost          string
	poolNodes          []PoolNode
	poolKnown          bool
	poolError          bool
	nodeDetails        map[string]nodeDetailVisibility
	bannerVersion      string
	bannerComponents   string
	history            []historyEntry
	historyTotal       int
	panelStarted       bool
	width              int
	widthFn            func() int
	heightFn           func() int
	done               chan struct{}
	closed             chan struct{}
	observing          bool
	observedOnline     bool
	observed           ServiceSnapshot
	commandEnabled     bool
	commandClosesView  bool
	serviceStopCommand string
	commandInput       string
	commandNotice      string
	selectionActiveFn  func() bool
}

func (s *Session) SetRelayTarget(rawURL string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relayHost = cleanTerminalLabel(parsed.Host, 100)
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

// ObservePool updates only a bounded, read-only view of the relay's node list.
func (s *Session) ObservePool(nodes []PoolNode, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.poolKnown, s.poolError = true, err != nil
	if err == nil {
		s.poolNodes = append([]PoolNode(nil), nodes...)
	} else {
		s.poolNodes = nil
	}
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

// EnableCommands adds a persistent input row to the panel console. It is kept
// separate from session history so UI help and typing never become service
// events or leak into redirected logs.
func (s *Session) EnableCommands() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commandEnabled = true
	s.commandClosesView = true
	s.serviceStopCommand = backgroundServiceStopCommand()
	if s.nodeDetails == nil {
		s.nodeDetails = map[string]nodeDetailVisibility{}
	}
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

// EnableServiceStopCommand advertises the authenticated local lifecycle
// command only in sessions backed by a local bridge HTTP service. Standalone
// worker/relay processes intentionally do not claim that this command can stop
// them.
func (s *Session) EnableServiceStopCommand() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serviceStopCommand = backgroundServiceStopCommand()
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

// EnableServiceCommands exposes the same display-only commands in a terminal
// that owns a foreground run/worker process. Unlike an attached console, an
// exit command must not silently stop that process: Ctrl+C remains the explicit
// service-stop action, while `contextbridge console` is the detachable view.
func (s *Session) EnableServiceCommands() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commandEnabled = true
	s.commandClosesView = false
	if s.nodeDetails == nil {
		s.nodeDetails = map[string]nodeDetailVisibility{}
	}
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

// LiveCommandEditor reports whether this session actually owns an in-place
// command row. Callers must not disable terminal echo merely because the
// configured style says panel: redirected, color-disabled, and initially
// narrow terminals deliberately use the normal line editor instead.
func (s *Session) LiveCommandEditor() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commandEnabled && s.panelStarted && s.interactive && s.style == "panel"
}

// SetCommandInput updates the console-owned line editor. The value is bounded
// and stripped of terminal control characters before it reaches the renderer.
func (s *Session) SetCommandInput(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commandInput = cleanTerminalLabel(value, 512)
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

// HandleCommand applies read-only console commands. It returns true only when
// the caller should close this view; it never stops the ContextBridge service.
func (s *Session) HandleCommand(command string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commandInput = ""
	fields := strings.Fields(command)
	if len(fields) == 0 {
		s.commandNotice = s.commandHelpLocked()
		s.renderCommandResultLocked()
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "exit", "quit", "q", ":q":
		if s.commandClosesView {
			return true
		}
		s.commandNotice = "Foreground service remains active · Ctrl+C stops it; `contextbridge console` is detachable."
	case "help", "?":
		s.commandNotice = s.commandHelpLocked()
	case "clear", "cls":
		s.history = nil
		s.historyTotal = 0
		if s.interactive && !s.panelStarted {
			fmt.Fprint(s.out, "\x1b[2J\x1b[H")
		}
		s.commandNotice = "Session history cleared; service and jobs continue running."
	case "details", "gpus", "models":
		if len(fields) != 2 {
			s.commandNotice = fields[0] + " expects all or a displayed node number, for example " + fields[0] + " 1"
			break
		}
		s.commandNotice = s.toggleNodeDetailsLocked(strings.ToLower(fields[0]), fields[1])
	default:
		s.commandNotice = "Unknown command: " + cleanTerminalLabel(fields[0], 40) + " · help lists all commands"
	}
	s.renderCommandResultLocked()
	return false
}

func (s *Session) commandHelpLocked() string {
	lines := []string{
		"help | ?         Show this help",
		"clear | cls      Clear only the visible session history",
		"details all|N    Toggle GPU and model details for a node",
		"gpus all|N       Toggle GPU details for a node",
		"models all|N     Toggle model details for a node",
	}
	if s.commandClosesView {
		lines = append(lines, "exit | quit | q   Close only this view; service continues running")
	} else {
		lines = append(lines, "Ctrl+C            Stop this foreground service")
	}
	return strings.Join(lines, "\n")
}

func (s *Session) commandHintLocked() string {
	commands := "help · clear · details all|N · gpus all|N · models all|N"
	if s.commandClosesView {
		return commands + " · exit"
	}
	return commands + " · Ctrl+C stops the foreground service"
}

func (s *Session) commandNoticeLinesLocked() []string {
	notice := s.commandNotice
	if notice == "" {
		notice = s.commandHintLocked()
	}
	parts := strings.Split(notice, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		if line := cleanTerminalLabel(part, 0); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return []string{s.commandHintLocked()}
	}
	return lines
}

func (s *Session) commandLifecycleHintLocked() string {
	if s.commandClosesView {
		return "exit = close only this view; service and jobs continue running"
	}
	return "exit = service remains active; Ctrl+C = stop this foreground service"
}

func backgroundServiceStopCommand() string {
	return "contextbridge stop"
}

func backgroundServiceStopCommandForOS(_ string) string {
	return backgroundServiceStopCommand()
}

func (s *Session) commandServiceStopHintLocked() string {
	return "Stop the background service (in CMD/shell)"
}

func (s *Session) renderCommandResultLocked() {
	if s.panelStarted {
		s.renderPanelLocked()
		return
	}
	if s.interactive {
		s.clearStatusLocked()
		for _, line := range s.commandNoticeLinesLocked() {
			fmt.Fprintln(s.out, "  "+line)
		}
		s.drawStatusLocked()
	}
}

func (s *Session) toggleNodeDetailsLocked(kind, target string) string {
	nodes := s.orderedPoolNodesLocked()
	if len(nodes) == 0 {
		return "No pool nodes available."
	}
	visibleCount := min(len(nodes), maximumVisiblePoolNodes)
	visibleNodes := nodes[:visibleCount]
	selected := []PoolNode{}
	hiddenCount := len(nodes) - visibleCount
	if strings.EqualFold(target, "all") {
		selected = visibleNodes
	} else if index, err := strconv.Atoi(target); err == nil && index >= 1 && index <= len(nodes) {
		if index > visibleCount {
			return fmt.Sprintf("Node %d is shown only in the dashboard; the terminal displays at most %d nodes.", index, maximumVisiblePoolNodes)
		}
		selected = append(selected, visibleNodes[index-1])
	} else {
		needle := strings.TrimPrefix(strings.ToLower(target), "#")
		for _, node := range visibleNodes {
			if strings.EqualFold(node.ID, target) || strings.EqualFold(node.Name, target) || strings.EqualFold(cluster.NodeDiscriminator(node.ID), needle) {
				selected = append(selected, node)
			}
		}
		if len(selected) == 0 {
			return "Node not found: " + cleanTerminalLabel(target, 40)
		}
		if len(selected) > 1 {
			return "Node name is ambiguous; use the displayed number."
		}
	}
	if s.nodeDetails == nil {
		s.nodeDetails = map[string]nodeDetailVisibility{}
	}
	allVisible := true
	for _, node := range selected {
		visibility := s.nodeDetails[nodeDetailKey(node)]
		switch kind {
		case "gpus":
			allVisible = allVisible && visibility.gpus
		case "models":
			allVisible = allVisible && visibility.models
		default:
			allVisible = allVisible && visibility.gpus && visibility.models
		}
	}
	show := !allVisible
	for _, node := range selected {
		key := nodeDetailKey(node)
		visibility := s.nodeDetails[key]
		switch kind {
		case "gpus":
			visibility.gpus = show
		case "models":
			visibility.models = show
		default:
			visibility.gpus, visibility.models = show, show
		}
		s.nodeDetails[key] = visibility
	}
	label := "Details"
	if kind == "gpus" {
		label = "GPU details"
	} else if kind == "models" {
		label = "Model details"
	}
	state := "hidden"
	if show {
		state = "shown"
	}
	notice := fmt.Sprintf("%s for %d node(s) %s.", label, len(selected), state)
	if strings.EqualFold(target, "all") && hiddenCount > 0 {
		notice += fmt.Sprintf(" %d additional node(s) are shown only in the dashboard.", hiddenCount)
	}
	return notice
}

func nodeDetailKey(node PoolNode) string {
	if node.ID != "" {
		return node.ID
	}
	return strings.ToLower(node.Name)
}

func (s *Session) orderedPoolNodesLocked() []PoolNode {
	nodes := append([]PoolNode(nil), s.poolNodes...)
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Connected != nodes[j].Connected {
			return nodes[i].Connected
		}
		if nodes[i].Running != nodes[j].Running {
			return nodes[i].Running > nodes[j].Running
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	return nodes
}

type historyEntry struct {
	when    time.Time
	section string
	symbol  string
	message string
	details []string
}

func New(output *os.File) *Session {
	return NewWithStyle(output, "classic")
}

// NewWithStyle selects the interactive presentation. Redirected logs keep
// their stable, timestamped format regardless of the configured style.
func NewWithStyle(output *os.File, style string) *Session {
	interactive := false
	if info, err := output.Stat(); err == nil {
		interactive = info.Mode()&os.ModeCharDevice != 0
	}
	if interactive && !enableVirtualTerminal(output) {
		interactive = false
	}
	if os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != "" {
		interactive = false
	}
	session := &Session{out: output, console: output, interactive: interactive, style: style, jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}, nodeDetails: map[string]nodeDetailVisibility{}, width: terminalWidth(output), widthFn: func() int { return terminalWidth(output) }, heightFn: func() int { return terminalHeight(output) }, done: make(chan struct{}), closed: make(chan struct{})}
	if interactive {
		go session.animate()
	} else {
		close(session.closed)
	}
	return session
}

func (s *Session) Banner(version, components string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interactive {
		if s.panelEnabledLocked() {
			s.bannerVersion, s.bannerComponents = version, components
			s.renderPanelLocked()
			return
		}
		fmt.Fprintf(s.out, "\n  ContextBridge  %s\n  %s\n\n", version, components)
		return
	}
	fmt.Fprintf(s.out, "ContextBridge %s · %s\n", version, components)
}

func (s *Session) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial += string(data)
	for {
		index := strings.IndexByte(s.partial, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSpace(s.partial[:index])
		s.partial = s.partial[index+1:]
		if line != "" {
			s.recordServiceLineLocked(line)
			s.nextSection = "SERVICE"
			s.writeEventLocked("·", line)
		}
	}
	return len(data), nil
}

func (s *Session) HandleWorker(event cluster.WorkerEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch event.Kind {
	case cluster.WorkerConnecting:
		if s.panelStarted {
			s.connectionLine = "Connecting to relay…"
		}
		if s.status == "" {
			s.setStatusLocked("Connecting to relay", time.Now())
		}
	case cluster.WorkerRetrying:
		s.retries = event.Attempt
		if s.panelStarted {
			s.connectionLine = "Relay unavailable · retrying automatically"
			s.adapterSelections = map[int]adapterSelection{}
			s.localProviders = nil
			s.localModels = map[string]localModelSelection{}
			s.capabilities = cluster.Capabilities{}
			s.hardware = ""
			if event.Attempt == 1 || event.Attempt%10 == 0 {
				s.nextSection = "CONNECTION"
				s.writeEventLocked("↻", fmt.Sprintf("Relay unavailable · attempt %d · %s", event.Attempt, compactError(event.Error)))
			}
		}
		s.setStatusLocked(fmt.Sprintf("Waiting for network · retry %d in %s", event.Attempt, compactDuration(event.RetryIn)), time.Now())
		if !s.interactive && (event.Attempt == 1 || event.Attempt%10 == 0) {
			s.writeEventLocked("↻", fmt.Sprintf("Relay unavailable; retrying automatically (%s)", compactError(event.Error)))
		}
	case cluster.WorkerConnected:
		s.node, s.nodeID, s.slots = cleanTerminalLabel(event.NodeName, 100), event.NodeID, event.Slots
		s.capabilities = event.Capabilities
		s.hardware = capabilityLabel(event.Capabilities)
		message := fmt.Sprintf("Relay connected · %s · %d slot", nodeLabel(s.node, s.nodeID, false), event.Slots)
		if event.Slots != 1 {
			message += "s"
		}
		if s.retries > 0 {
			message += fmt.Sprintf(" · recovered after %d retries", s.retries)
		}
		s.nextSection = "CONNECTION"
		s.writeEventLocked("✓", message)
		s.recordAdapterSelectionsLocked(event.Capabilities.AdapterSessions)
		s.recordLocalModelsLocked(event.Capabilities.Models, event.Capabilities.Providers)
		s.retries = 0
		s.setStatusLocked("Idle", time.Now())
	case cluster.WorkerCapabilities:
		s.capabilities = event.Capabilities
		if event.Slots > 0 {
			s.slots = event.Slots
		}
		s.hardware = capabilityLabel(event.Capabilities)
		s.recordAdapterSelectionsLocked(event.Capabilities.AdapterSessions)
		s.recordLocalModelsLocked(event.Capabilities.Models, event.Capabilities.Providers)
	case cluster.WorkerJobStarted:
		selection := requestedSelection(event)
		s.jobs[event.JobID] = jobState{task: event.Task, phase: "starting", selection: selection, started: time.Now()}
		message := fmt.Sprintf("Job %s · %s", shortID(event.JobID), empty(event.Task, "generation"))
		if selection != "" {
			message += " · " + selection
		}
		s.nextSection = "JOBS"
		if s.panelEnabledLocked() && selection != "" {
			s.writeEventWithDetailsLocked("→", fmt.Sprintf("Job %s · %s", shortID(event.JobID), empty(event.Task, "generation")), []string{"requested · " + selection})
		} else {
			s.writeEventLocked("→", message)
		}
		s.refreshJobStatusLocked()
	case cluster.WorkerJobProgress:
		job := s.jobs[event.JobID]
		job.phase = empty(event.Phase, "working")
		job.detail = event.Detail
		job.percent = event.Percent
		job.sequence = event.Sequence
		if preview := progressPreview(event.Text, 80); preview != "" {
			job.preview = preview
		}
		s.jobs[event.JobID] = job
		s.refreshJobStatusLocked()
	case cluster.WorkerJobCompleted:
		delete(s.jobs, event.JobID)
		message := fmt.Sprintf("Job %s completed · %s", shortID(event.JobID), compactDuration(millisecondsDuration(event.ComputeMS)))
		var detail string
		if event.ReportedProvider == "adapter" {
			if event.ReportedModel != "" {
				message += " · Endpoint reports: " + cleanTerminalLabel(event.ReportedModel, 80)
			} else {
				detail = "model metadata unavailable · adapter selection unverified"
			}
			if event.ReportedReasoning != "" {
				message += " · reasoning: " + cleanTerminalLabel(event.ReportedReasoning, 40)
			}
		} else if event.ReportedModel != "" {
			message += " · used: " + cleanTerminalLabel(event.ReportedProvider, 30) + " · " + cleanTerminalLabel(event.ReportedModel, 80)
		}
		s.nextSection = "JOBS"
		if s.panelEnabledLocked() {
			details := []string{}
			if event.ReportedProvider != "" {
				details = append(details, "route · "+cleanTerminalLabel(event.ReportedProvider, 30))
			}
			if event.ReportedModel != "" {
				details = append(details, "model · "+cleanTerminalLabel(event.ReportedModel, 80))
			}
			if event.ReportedReasoning != "" {
				details = append(details, "reasoning · "+cleanTerminalLabel(event.ReportedReasoning, 40))
			}
			if detail != "" {
				details = append(details, detail)
			}
			s.writeEventWithDetailsLocked("✓", fmt.Sprintf("Job %s completed · %s", shortID(event.JobID), compactDuration(millisecondsDuration(event.ComputeMS))), details)
		} else {
			s.writeEventWithDetailLocked("✓", message, detail)
		}
		s.refreshJobStatusLocked()
	case cluster.WorkerJobFailed:
		delete(s.jobs, event.JobID)
		s.nextSection = "JOBS"
		s.writeEventLocked("!", fmt.Sprintf("Job %s needs attention · %s", shortID(event.JobID), compactError(event.Error)))
		s.refreshJobStatusLocked()
	}
}

// ObserveService updates a read-only console without starting another bridge.
// Only changes that can be established from consecutive snapshots are logged.
func (s *Session) ObserveService(snapshot ServiceSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, wasOnline := s.observed, s.observedOnline
	s.observing, s.observedOnline = true, true
	s.observed = snapshot
	if !wasOnline {
		s.nextSection = "CONNECTION"
		s.writeEventLocked("✓", "Attached to running service · "+cleanTerminalLabel(snapshot.Version, 30)+" · read-only")
	}
	if !wasOnline || previous.AdapterConnected != snapshot.AdapterConnected {
		s.nextSection = "ADAPTER ENDPOINTS"
		if snapshot.AdapterConnected {
			s.writeEventLocked("✓", fmt.Sprintf("Adapter connected · %d endpoint(s)", snapshot.ActiveEndpoints))
		} else {
			s.writeEventLocked("◇", "No adapter process connected")
		}
	}
	if snapshot.AdapterConnected {
		s.recordAdapterSelectionsLocked(snapshot.Endpoints)
	} else if len(s.adapterSelections) > 0 {
		s.adapterSelections = map[int]adapterSelection{}
	}
	s.recordLocalModelsLocked(snapshot.LocalModels, snapshot.LocalProviders)
	if wasOnline {
		if snapshot.Completed > previous.Completed {
			s.nextSection = "ACTIVITY"
			s.writeEventLocked("✓", fmt.Sprintf("Completed total %d (+%d since last check)", snapshot.Completed, snapshot.Completed-previous.Completed))
		}
		if snapshot.JobsFailed > previous.JobsFailed {
			s.nextSection = "ACTIVITY"
			s.writeEventLocked("!", fmt.Sprintf("Failed total %d (+%d since last check)", snapshot.JobsFailed, snapshot.JobsFailed-previous.JobsFailed))
		}
	}
	if s.status == "" || s.status == "Offline" {
		s.setStatusLocked("Idle", time.Now())
	} else {
		s.drawStatusLocked()
	}
}

func (s *Session) ObserveServiceUnavailable(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.observing || s.observedOnline {
		s.nextSection = "CONNECTION"
		s.writeEventLocked("!", "Service unavailable · "+cleanTerminalLabel(reason, 120))
	}
	s.observing, s.observedOnline = true, false
	if s.panelStarted {
		s.connectionLine = "Service unavailable · retrying automatically"
		s.adapterSelections = map[int]adapterSelection{}
		s.localProviders = nil
		s.localModels = map[string]localModelSelection{}
	}
	s.setStatusLocked("Offline", time.Now())
}

func requestedSelection(event cluster.WorkerEvent) string {
	parts := []string{}
	if event.Provider != "" {
		parts = append(parts, cleanTerminalLabel(event.Provider, 30))
	}
	if event.Profile != "" {
		parts = append(parts, cleanTerminalLabel(event.Profile, 30))
	}
	if event.Model != "" {
		parts = append(parts, "model requested: "+cleanTerminalLabel(event.Model, 80))
	}
	if event.Reasoning != "" {
		parts = append(parts, "reasoning requested: "+cleanTerminalLabel(event.Reasoning, 40))
	}
	return strings.Join(parts, " · ")
}

func (s *Session) recordAdapterSelectionsLocked(sessions []cluster.AdapterSessionCapability) {
	current := make(map[int]adapterSelection, len(sessions))
	for _, endpoint := range sessions {
		if endpoint.EndpointID <= 0 {
			continue
		}
		selection := adapterSelection{
			profile:   cleanTerminalLabel(empty(endpoint.Profile, "adapter"), 30),
			model:     cleanTerminalLabel(endpoint.CurrentModel, 80),
			reasoning: cleanTerminalLabel(endpoint.CurrentReasoning, 40),
			state:     cleanTerminalLabel(endpoint.State, 40),
		}
		// An empty reading is not evidence that the user changed the model or
		// reasoning level. An adapter may briefly omit controls while refreshing;
		// logging each missing/returning label creates duplicate endpoint lines.
		if previous, ok := s.adapterSelections[endpoint.EndpointID]; ok && previous.profile == selection.profile {
			if selection.model == "" {
				selection.model = previous.model
			}
			if selection.reasoning == "" {
				selection.reasoning = previous.reasoning
			}
			if selection.state == "" {
				selection.state = previous.state
			}
		}
		current[endpoint.EndpointID] = selection
	}
	ids := make([]int, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := current[ids[i]], current[ids[j]]
		if providerRank(left.profile) != providerRank(right.profile) {
			return providerRank(left.profile) < providerRank(right.profile)
		}
		if !strings.EqualFold(left.profile, right.profile) {
			return strings.ToLower(left.profile) < strings.ToLower(right.profile)
		}
		if adapterStateRank(left.state) != adapterStateRank(right.state) {
			return adapterStateRank(left.state) < adapterStateRank(right.state)
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		if s.adapterSelections[id] != current[id] {
			selection := current[id]
			label := fmt.Sprintf("Endpoint %d", id)
			if _, previouslySeen := s.adapterSelections[id]; previouslySeen {
				label += " updated"
			}
			parts := []string{selection.profile}
			if selection.model != "" {
				parts = append(parts, "model: "+selection.model)
			} else {
				parts = append(parts, "model not detected yet")
			}
			if selection.reasoning != "" {
				parts = append(parts, "reasoning: "+selection.reasoning)
			}
			parts = append(parts, adapterStateLabel(selection.state))
			s.nextSection = "ADAPTER ENDPOINTS"
			s.writeEventLocked("◇", fmt.Sprintf("%s · %s", label, strings.Join(parts, " · ")))
		}
	}
	for id := range s.adapterSelections {
		if _, ok := current[id]; !ok {
			s.nextSection = "ADAPTER ENDPOINTS"
			s.writeEventLocked("◇", fmt.Sprintf("Endpoint %d disconnected", id))
		}
	}
	s.adapterSelections = current
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

func providerRank(profile string) int {
	if strings.TrimSpace(profile) == "" {
		return 1
	}
	return 0
}

func adapterStateRank(state string) int {
	switch strings.ToLower(state) {
	case "working":
		return 0
	case "waiting":
		return 1
	case "rate_limited":
		return 3
	default:
		return 2
	}
}

func adapterStateLabel(state string) string {
	switch strings.ToLower(state) {
	case "working":
		return "running"
	case "waiting":
		return "idle"
	case "rate_limited":
		return "cooling down"
	default:
		return "status unknown"
	}
}

func (s *Session) recordLocalModelsLocked(models []cluster.ModelCapability, providers []string) {
	current := map[string]localModelSelection{}
	loaded := false
	online := map[string]string{}
	for _, provider := range providers {
		provider = cleanTerminalLabel(provider, 40)
		if provider != "" && !strings.EqualFold(provider, "adapter") {
			online[strings.ToLower(provider)] = provider
		}
	}
	for _, model := range models {
		provider := cleanTerminalLabel(model.Provider, 40)
		name := cleanTerminalLabel(model.Name, 100)
		if provider == "" || strings.EqualFold(provider, "adapter") || name == "" {
			continue
		}
		key := strings.ToLower(provider + "\x00" + name)
		selection := localModelSelection{provider: provider, name: name, loaded: model.Loaded || current[key].loaded}
		current[key] = selection
		loaded = loaded || selection.loaded
		if selection.loaded {
			online[strings.ToLower(provider)] = provider
		}
	}
	providerNames := make([]string, 0, len(online))
	for _, provider := range online {
		providerNames = append(providerNames, provider)
	}
	sort.Slice(providerNames, func(i, j int) bool { return strings.ToLower(providerNames[i]) < strings.ToLower(providerNames[j]) })
	previousProviders := strings.Join(s.localProviders, "\x00")
	s.localProviders = providerNames
	if !loaded {
		wasLoaded := false
		for _, previous := range s.localModels {
			wasLoaded = wasLoaded || previous.loaded
		}
		if wasLoaded || previousProviders != strings.Join(providerNames, "\x00") {
			s.nextSection = "LOCAL MODELS"
			if len(providerNames) == 0 {
				s.writeEventLocked("◇", "No local provider available")
			} else {
				s.writeEventLocked("◇", strings.Join(providerNames, ", ")+" available · no model loaded")
			}
		}
		s.localModels = current
		if s.panelStarted {
			s.renderPanelLocked()
		}
		return
	}
	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := current[keys[i]], current[keys[j]]
		if !strings.EqualFold(left.provider, right.provider) {
			return strings.ToLower(left.provider) < strings.ToLower(right.provider)
		}
		if left.loaded != right.loaded {
			return left.loaded
		}
		return strings.ToLower(left.name) < strings.ToLower(right.name)
	})
	for _, key := range keys {
		selection := current[key]
		if s.localModels[key] == selection {
			continue
		}
		state := "ready · not loaded"
		if selection.loaded {
			state = "loaded"
		}
		s.nextSection = "LOCAL MODELS"
		s.writeEventLocked("◇", fmt.Sprintf("%s · %s · %s", selection.provider, selection.name, state))
	}
	for key, previous := range s.localModels {
		if _, ok := current[key]; !ok {
			s.nextSection = "LOCAL MODELS"
			s.writeEventLocked("◇", fmt.Sprintf("%s · %s no longer available", previous.provider, previous.name))
		}
	}
	s.localModels = current
	if s.panelStarted {
		s.renderPanelLocked()
	}
}

func cleanTerminalLabel(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	return truncateRunes(value, limit)
}

func (s *Session) Close() {
	s.mu.Lock()
	if s.interactive {
		close(s.done)
	}
	if strings.TrimSpace(s.partial) != "" {
		s.writeEventLocked("·", strings.TrimSpace(s.partial))
		s.partial = ""
	}
	s.mu.Unlock()
	<-s.closed
	s.mu.Lock()
	if s.panelStarted {
		fmt.Fprint(s.out, "\x1b[?25h\x1b[?1049l")
		s.panelStarted = false
	} else if s.interactive {
		s.clearStatusLocked()
	}
	s.mu.Unlock()
}

func (s *Session) animate() {
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	defer close(s.closed)
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			s.frame++
			panelWorking := len(s.jobs) > 0 || (s.observing && s.observedOnline && (s.observed.ActiveJobs > 0 || s.observed.BusyEndpoints > 0))
			if (s.panelStarted && (panelWorking && s.frame%2 == 0 || !panelWorking && s.frame%8 == 0)) || (!s.panelStarted && (s.status != "Idle" || s.frame%8 == 0)) {
				s.drawStatusLocked()
			}
			s.mu.Unlock()
		}
	}
}

func (s *Session) refreshJobStatusLocked() {
	if len(s.jobs) == 0 {
		s.setStatusLocked("Idle", time.Now())
		return
	}
	started := time.Now()
	for _, job := range s.jobs {
		if job.started.Before(started) {
			started = job.started
		}
	}
	s.setStatusLocked("Working", started)
}

func (s *Session) setStatusLocked(message string, started time.Time) {
	s.status = message
	s.statusSince = started
	if s.interactive {
		s.drawStatusLocked()
	}
}

func (s *Session) writeEventLocked(symbol, message string) {
	s.writeEventWithDetailLocked(symbol, message, "")
}

func (s *Session) writeEventWithDetailLocked(symbol, message, detail string) {
	if detail == "" {
		s.writeEventWithDetailsLocked(symbol, message, nil)
		return
	}
	s.writeEventWithDetailsLocked(symbol, message, []string{detail})
}

func (s *Session) writeEventWithDetailsLocked(symbol, message string, details []string) {
	if s.interactive {
		if !s.panelStarted && s.panelEnabledLocked() {
			s.renderPanelLocked()
		}
		if s.panelStarted {
			if s.nextSection == "CONNECTION" {
				s.connectionLine = message
			}
			s.addHistoryLocked(symbol, message, details)
			s.nextSection = ""
			s.renderPanelLocked()
			return
		}
		s.clearStatusLocked()
		if s.panelEnabledLocked() {
			s.writePanelSectionLocked()
			width := s.panelWidthLocked()
			writePanelText(s.out, "  | "+coloredSymbol(symbol)+"  ", "  |    ", message, max(12, width-8))
			for _, detail := range details {
				writePanelText(s.out, "  |   "+ansiYellow+"+->"+ansiReset+" ", "  |       ", detail, max(8, width-12))
			}
		} else {
			if s.style == "panel" {
				s.section = ""
			}
			fmt.Fprintf(s.out, "  %s  %s\n", coloredSymbol(symbol), message)
			for _, detail := range details {
				fmt.Fprintf(s.out, "     %s└─%s %s\n", ansiYellow, ansiReset, detail)
			}
		}
		s.drawStatusLocked()
		return
	}
	fmt.Fprintf(s.out, "%s  %s  %s\n", time.Now().Format("2006/01/02 15:04:05"), symbol, message)
	for _, detail := range details {
		fmt.Fprintf(s.out, "     └─ %s\n", detail)
	}
}

func (s *Session) panelEnabledLocked() bool {
	return s.interactive && s.style == "panel" && s.panelWidthLocked() >= 24
}

func (s *Session) panelWidthLocked() int {
	if s.widthFn != nil {
		if width := s.widthFn(); width > 0 {
			s.width = width
		}
	}
	width := s.width
	if width <= 0 {
		width = 80
	}
	return max(12, min(180, width-2))
}

func panelBorder(width int) string {
	return "  +" + strings.Repeat("-", max(0, width-4)) + "+"
}

func panelRow(value string, width int) string {
	value = truncateRunes(value, max(1, width-6))
	return "  | " + value + strings.Repeat(" ", max(0, width-6-utf8.RuneCountInString(value))) + " |"
}

func (s *Session) writePanelSectionLocked() {
	section := s.nextSection
	s.nextSection = ""
	if section == "" {
		section = empty(s.section, "EVENTS")
	}
	if section == s.section {
		return
	}
	s.section = section
	width := s.panelWidthLocked()
	label := "+-- " + section + " "
	fmt.Fprintln(s.out, "  "+label+strings.Repeat("-", max(0, width-3-len(label)))+"+")
}

func writePanelText(out io.Writer, firstPrefix, nextPrefix, value string, width int) {
	value = strings.Join(strings.Fields(cleanTerminalLabel(value, 0)), " ")
	remaining := []rune(value)
	prefix := firstPrefix
	for len(remaining) > width {
		cut := width
		for index := width; index > width/2; index-- {
			if remaining[index] == ' ' {
				cut = index
				break
			}
		}
		fmt.Fprintln(out, prefix+strings.TrimSpace(string(remaining[:cut])))
		remaining = remaining[cut:]
		for len(remaining) > 0 && remaining[0] == ' ' {
			remaining = remaining[1:]
		}
		prefix = nextPrefix
	}
	if len(remaining) > 0 || value == "" {
		fmt.Fprintln(out, prefix+string(remaining))
	}
}

func (s *Session) recordServiceLineLocked(line string) {
	key := line
	if index := strings.IndexAny(key, ": "); index >= 0 {
		key = key[:index]
	}
	for index, previous := range s.serviceLines {
		if strings.HasPrefix(previous, key) {
			s.serviceLines[index] = line
			return
		}
	}
	s.serviceLines = append(s.serviceLines, line)
	if len(s.serviceLines) > 8 {
		s.serviceLines = s.serviceLines[len(s.serviceLines)-8:]
	}
}

func (s *Session) addHistoryLocked(symbol, message string, details []string) {
	copyDetails := append([]string(nil), details...)
	s.history = append(s.history, historyEntry{when: time.Now(), section: s.nextSection, symbol: symbol, message: message, details: copyDetails})
	s.historyTotal++
	if len(s.history) > 10000 {
		s.history = s.history[len(s.history)-10000:]
	}
}

func panelSection(label string, width int) string {
	prefix := "  +-- " + label + " "
	return prefix + strings.Repeat("-", max(0, width-utf8.RuneCountInString(prefix)-1)) + "+"
}

func nodeGPURows(node PoolNode, line func(string) string) []string {
	suffix := ""
	if !node.Connected {
		suffix = " · last reported"
	}
	if len(node.GPUs) == 0 {
		return []string{"  |    System GPU · Zero-GPU" + suffix}
	}
	rows := []string{}
	for index, gpu := range node.GPUs {
		if index == 8 {
			rows = append(rows, "  |    "+line(fmt.Sprintf("… %d additional system GPUs", len(node.GPUs)-index)))
			break
		}
		parts := []string{fmt.Sprintf("System GPU %d", index+1), cleanTerminalLabel(gpu.Name, 50), fmt.Sprintf("%d%%", gpu.Utilization)}
		if gpu.MemoryTotal > 0 {
			parts = append(parts, humanBytes(gpu.MemoryFree)+"/"+humanBytes(gpu.MemoryTotal)+" free")
		}
		if gpu.Temperature > 0 {
			parts = append(parts, fmt.Sprintf("%d°C", gpu.Temperature))
		}
		rows = append(rows, "  |    "+colorGPUPercent(line(strings.Join(parts, " · ")+suffix), gpu.Utilization))
	}
	return rows
}

func nodeModelRows(node PoolNode, line func(string) string) []string {
	suffix := ""
	if !node.Connected {
		suffix = " · last reported"
	}
	if len(node.Models) == 0 {
		return []string{"  |    Worker models · none reported" + suffix}
	}
	models := append([]cluster.ModelCapability(nil), node.Models...)
	sort.Slice(models, func(i, j int) bool {
		if models[i].Loaded != models[j].Loaded {
			return models[i].Loaded
		}
		if !strings.EqualFold(models[i].Provider, models[j].Provider) {
			return strings.ToLower(models[i].Provider) < strings.ToLower(models[j].Provider)
		}
		return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name)
	})
	rows := []string{}
	for index, model := range models {
		if index == 12 {
			rows = append(rows, "  |    "+line(fmt.Sprintf("… %d additional worker models", len(models)-index)))
			break
		}
		state := "ready · not loaded"
		if model.Loaded {
			state = "loaded"
		}
		parts := []string{"Worker model", cleanTerminalLabel(model.Name, 70), state}
		if model.Provider != "" {
			parts = append(parts, cleanTerminalLabel(model.Provider, 30))
		}
		modes := append([]string(nil), model.Tasks...)
		if model.Vision && !containsIndicator(modes, "vision") {
			modes = append(modes, "vision")
		}
		if model.Embedding && !containsIndicator(modes, "embedding") {
			modes = append(modes, "embedding")
		}
		if len(modes) > 0 {
			parts = append(parts, strings.Join(modes, ","))
		}
		rows = append(rows, "  |    "+colorModelLoadState(line(strings.Join(parts, " · ")+suffix), model.Loaded))
	}
	return rows
}

func colorModelLoadState(value string, loaded bool) string {
	if loaded {
		return colorDelimitedModelState(value, "loaded", ansiGreen)
	}
	for _, state := range []string{"ready · not loaded", "ready"} {
		if colored := colorDelimitedModelState(value, state, ansiDim); colored != value {
			return colored
		}
	}
	return value
}

func colorDelimitedModelState(value, state, color string) string {
	needle := " · " + state
	index := strings.LastIndex(value, needle)
	if index < 0 {
		return value
	}
	stateIndex := index + len(needle) - len(state)
	return value[:stateIndex] + color + state + ansiReset + value[stateIndex+len(state):]
}

func localModelPanelRow(value string, loaded bool) string {
	marker := ansiDim + "◇" + ansiReset
	if loaded {
		marker = ansiGreen + "◇" + ansiReset
	}
	return "  | " + marker + "  " + colorModelLoadState(value, loaded)
}

func (s *Session) panelStatusLocked() string {
	if s.observing {
		if !s.observedOnline {
			return "◇ Offline · service offline · retrying connection"
		}
		state := "Idle"
		if s.observed.ActiveJobs > 0 || s.observed.BusyEndpoints > 0 {
			state = "Working"
		}
		return fmt.Sprintf("◇ %s · service %s · queue %d · adapters %d/%d busy · jobs %d · errors %d", state, s.observed.Version,
			s.observed.Queued, s.observed.BusyEndpoints, s.observed.ActiveEndpoints, s.observed.JobsTotal, s.observed.JobsFailed)
	}
	if s.status == "" {
		return "◇ Starting…"
	}
	status := s.status
	if len(s.jobs) > 0 {
		status, _ = s.visibleJobStatusLocked(70)
	}
	slots := max(1, s.slots)
	return fmt.Sprintf("◇ %s %s · %d/%d jobs · %d%% · %s", status, compactDuration(time.Since(s.statusSince)),
		len(s.jobs), slots, min(100, len(s.jobs)*100/slots), nodeLabel(s.node, s.nodeID, false))
}

// The panel uses the alternate screen so old snapshots never become logs on
// zoom or resize. Session events remain in memory and are shown below HISTORY.
func (s *Session) renderPanelLocked() {
	if !s.interactive || s.style != "panel" {
		return
	}
	selectionActive := false
	if s.selectionActiveFn != nil {
		selectionActive = s.selectionActiveFn()
	} else if s.console != nil {
		selectionActive = consoleSelectionActive(s.console)
	}
	if s.panelStarted && selectionActive {
		return
	}
	width := s.panelWidthLocked()
	height := 40
	if s.heightFn != nil {
		if actual := s.heightFn(); actual > 0 {
			height = actual
		}
	}
	height = max(8, height)
	spacious := height >= 26
	gap := func(rows []string) []string {
		if spacious {
			return append(rows, "  |")
		}
		return rows
	}
	line := func(value string) string { return truncateRunes(value, max(1, width-2)) }
	rows := []string{
		panelBorder(width),
		panelRow("ContextBridge  "+cleanTerminalLabel(s.bannerVersion, 24), width),
		panelRow(cleanTerminalLabel(s.bannerComponents, 120), width),
		panelRow("by IamAngusU · https://github.com/IamAngusU/ContextBridge", width),
		panelBorder(width),
		"",
		panelSection("SERVICE", width),
	}
	if len(s.serviceLines) == 0 {
		rows = append(rows, "  | ·  Loading service status…")
	} else {
		for _, value := range s.serviceLines {
			rows = append(rows, "  | ·  "+line(value))
		}
	}
	rows = gap(rows)
	rows = append(rows, panelSection("CONNECTION", width))
	connection := s.connectionLine
	if connection == "" {
		connection = "Checking connection…"
	}
	rows = append(rows, "  | ·  "+line(connection))
	rows[len(rows)-1] = colorPanelConnection(rows[len(rows)-1])
	rows = gap(rows)
	if s.relayHost != "" {
		rows = append(rows, panelSection("RELAY / NODES", width))
		if s.poolError {
			rows = append(rows, "  | ◇  "+line("Relay "+s.relayHost+" · pool status unavailable"))
		} else if !s.poolKnown {
			rows = append(rows, "  | ◇  "+line("Relay "+s.relayHost+" · loading pool status…"))
		} else {
			nodes := s.orderedPoolNodesLocked()
			online := 0
			for _, node := range nodes {
				if node.Connected {
					online++
				}
			}
			rows = append(rows, "  | "+ansiCyan+"◇"+ansiReset+"  "+line(fmt.Sprintf("Relay %s · %d/%d workers online", s.relayHost, online, len(nodes))))
			if len(nodes) == 0 {
				rows = append(rows, "  | ·  No workers connected")
			}
			for index, node := range nodes {
				if index == maximumVisiblePoolNodes {
					rows = append(rows, "  | ·  "+line(fmt.Sprintf("%d additional nodes in the dashboard", len(nodes)-index)))
					break
				}
				state := "offline"
				if node.Connected {
					state = "idle"
					if node.Running > 0 {
						state = "working"
					}
				}
				self := ""
				if node.ID != "" && node.ID == s.nodeID {
					self = " · this PC"
				}
				visibility := s.nodeDetails[nodeDetailKey(node)]
				detailState := "details off"
				if visibility.gpus || visibility.models {
					detailState = "details on"
				}
				rows = append(rows, "  | ·  "+colorPanelState(line(fmt.Sprintf("[%d] %s · %s · %d/%d jobs%s · %s", index+1, nodeLabel(node.Name, node.ID, false), state, node.Running, max(1, node.Slots), self, detailState)), state))
				if visibility.gpus {
					rows = append(rows, nodeGPURows(node, line)...)
				}
				if visibility.models {
					rows = append(rows, nodeModelRows(node, line)...)
				}
			}
		}
		rows = gap(rows)
	}
	rows = append(rows, panelSection("ADAPTER ENDPOINTS", width))
	rows = gap(rows)
	if len(s.adapterSelections) == 0 {
		rows = append(rows, "  | ·  No connected AI endpoints")
	} else {
		ids := make([]int, 0, len(s.adapterSelections))
		for id := range s.adapterSelections {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			left, right := s.adapterSelections[ids[i]], s.adapterSelections[ids[j]]
			if providerRank(left.profile) != providerRank(right.profile) {
				return providerRank(left.profile) < providerRank(right.profile)
			}
			if !strings.EqualFold(left.profile, right.profile) {
				return strings.ToLower(left.profile) < strings.ToLower(right.profile)
			}
			if adapterStateRank(left.state) != adapterStateRank(right.state) {
				return adapterStateRank(left.state) < adapterStateRank(right.state)
			}
			return ids[i] < ids[j]
		})
		lastProfile := ""
		for _, id := range ids {
			selection := s.adapterSelections[id]
			if !strings.EqualFold(lastProfile, selection.profile) {
				if lastProfile != "" {
					rows = gap(rows)
				}
				lastProfile = selection.profile
				label := selection.profile
				rows = append(rows, panelSection("["+label+"]", width))
			}
			parts := []string{fmt.Sprintf("Endpoint %d", id)}
			if selection.model != "" {
				parts = append(parts, "model: "+selection.model)
			} else {
				parts = append(parts, "model unknown")
			}
			if selection.reasoning != "" {
				parts = append(parts, "reasoning: "+selection.reasoning)
			}
			parts = append(parts, adapterStateLabel(selection.state))
			label := line(strings.Join(parts, " · "))
			if selection.model == "" {
				label = strings.Replace(label, "model unknown", ansiDim+"model unknown"+ansiReset, 1)
			}
			rows = append(rows, "  | "+ansiCyan+"◇"+ansiReset+"  "+colorPanelState(label, adapterStateLabel(selection.state)))
		}
	}
	for _, provider := range s.localProviders {
		rows = gap(rows)
		rows = append(rows, panelSection("[Local · "+provider+"]", width))
		models := []localModelSelection{}
		anyLoaded := false
		for _, model := range s.localModels {
			if strings.EqualFold(model.provider, provider) {
				models = append(models, model)
				anyLoaded = anyLoaded || model.loaded
			}
		}
		sort.Slice(models, func(i, j int) bool {
			if models[i].loaded != models[j].loaded {
				return models[i].loaded
			}
			return strings.ToLower(models[i].name) < strings.ToLower(models[j].name)
		})
		if len(models) == 0 {
			rows = append(rows, "  | "+ansiDim+"◇  available · no model loaded"+ansiReset)
		} else {
			if !anyLoaded {
				rows = append(rows, "  | "+ansiDim+"◇  available · no model loaded"+ansiReset)
			}
			for _, model := range models {
				state := "ready · not loaded"
				if model.loaded {
					state = "loaded"
				}
				rows = append(rows, localModelPanelRow(line(model.name+" · "+state), model.loaded))
			}
		}
	}
	for _, provider := range s.observed.APIProviders {
		rows = gap(rows)
		rows = append(rows, panelSection("[API · "+provider+"]", width))
		models := make([]cluster.ModelCapability, 0)
		for _, model := range s.observed.APIModels {
			if strings.EqualFold(model.Provider, provider) {
				models = append(models, model)
			}
		}
		if len(models) == 0 {
			rows = append(rows, "  | "+ansiDim+"◇  available · model status unknown"+ansiReset)
			continue
		}
		sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name) })
		for _, model := range models {
			rows = append(rows, "  | "+ansiPurple+"◇"+ansiReset+"  "+line(model.Name+" · available via API"))
		}
	}
	if s.observing && len(s.observed.ResourcePacks) > 0 {
		rows = gap(rows)
		rows = append(rows, panelSection("HOT-PLUG RESOURCES", width))
		packs := append([]resourcepacks.Pack(nil), s.observed.ResourcePacks...)
		sort.Slice(packs, func(i, j int) bool { return strings.ToLower(packs[i].ID) < strings.ToLower(packs[j].ID) })
		for _, pack := range packs {
			name := pack.Name
			if pack.Version != "" {
				name += " " + pack.Version
			}
			if pack.Quarantined {
				name += " · quarantined"
			}
			detail := fmt.Sprintf("%s · %s · %d endpoint(s)", pack.ID, empty(pack.Kind, "resource-pack"), len(pack.Endpoints))
			rows = append(rows, "  | "+ansiOrange+"◆"+ansiReset+"  "+line(name+" · "+detail))
		}
	}
	rows = gap(rows)
	status := "  | " + colorPanelStatus(line(s.panelStatusLocked()), s)
	statusDetails := []string{}
	working := len(s.jobs) > 0 || (s.observing && s.observedOnline && (s.observed.ActiveJobs > 0 || s.observed.BusyEndpoints > 0))
	if working {
		statusDetails = append(statusDetails, "  | "+ansiCyan+travelBar(s.frame, 16)+ansiReset)
	}
	if s.observing {
		if s.observedOnline {
			observedCapabilities := cluster.Capabilities{
				Providers: append(append([]string(nil), s.observed.LocalProviders...), s.observed.APIProviders...),
				Models:    append(append([]cluster.ModelCapability(nil), s.observed.LocalModels...), s.observed.APIModels...),
			}
			if s.observed.AdapterConnected {
				observedCapabilities.AdapterSessions = append([]cluster.AdapterSessionCapability(nil), s.observed.Endpoints...)
			}
			indicators := indicatorLabel(observedCapabilities)
			if width < 130 {
				indicators = compactIndicatorLabel(observedCapabilities)
			}
			statusDetails = append(statusDetails, "  | "+indicators)
			statusDetails = append(statusDetails, "  | "+colorGPUPercent(fmt.Sprintf("GPU · %s · %d%%", empty(s.observed.GPU, "unknown"), s.observed.GPUUtilization), s.observed.GPUUtilization))
		}
	} else {
		indicators := indicatorLabel(s.capabilities)
		if width < 130 {
			indicators = compactIndicatorLabel(s.capabilities)
		}
		statusDetails = append(statusDetails, "  | "+indicators)
		if s.hardware != "" {
			hardware := s.hardware
			if len(s.capabilities.GPUs) > 0 {
				maximumUtilization := 0
				for _, gpu := range s.capabilities.GPUs {
					maximumUtilization = max(maximumUtilization, gpu.Utilization)
				}
				hardware = colorGPUPercent(hardware, maximumUtilization)
			}
			statusDetails = append(statusDetails, "  | "+hardware)
		}
	}
	commandNoticeLines := []string{}
	commandReserve := 0
	if s.commandEnabled {
		commandNoticeLines = s.commandNoticeLinesLocked()
		commandReserve = len(commandNoticeLines) + 4 // heading, input, results, lifecycle, border
		if s.serviceStopCommand != "" {
			commandReserve += 2 // stop label and exact command
		}
	}
	tailReserve := 5 + len(statusDetails) + commandReserve // status plus a boxed history row
	if spacious {
		tailReserve++
	}
	maxLiveRows := max(1, height-tailReserve)
	// Spacing is decorative. Preserve actual live state first when the window
	// is short or the pool grows; only then truncate content if necessary.
	for len(rows) > maxLiveRows {
		gapIndex := -1
		for index := len(rows) - 1; index >= 0; index-- {
			if rows[index] == "  |" {
				gapIndex = index
				break
			}
		}
		if gapIndex < 0 {
			break
		}
		rows = append(rows[:gapIndex], rows[gapIndex+1:]...)
	}
	if len(rows) > maxLiveRows {
		hidden := len(rows) - maxLiveRows + 1
		rows = append(rows[:maxLiveRows-1], fmt.Sprintf("  | … %d additional live rows", hidden))
	}
	rows = append(rows, panelSection("STATUS", width), status)
	rows = append(rows, statusDetails...)
	rows = gap(rows)
	availableHistory := max(1, height-len(rows)-2-commandReserve)
	rows = append(rows, panelSection(fmt.Sprintf("HISTORY · session · %d events", s.historyTotal), width))
	historyRows := []string{}
	// Newest first keeps the current event next to live state. Details remain
	// directly below their parent event so the hierarchy is never inverted.
	for index := len(s.history) - 1; index >= 0; index-- {
		entry := s.history[index]
		label := entry.when.Format("15:04:05") + " " + entry.symbol + " "
		if entry.section != "" {
			label += "[" + entry.section + "] "
		}
		historyLine := line(label + entry.message)
		block := []string{"  | " + strings.Replace(historyLine, " "+entry.symbol+" ", " "+coloredSymbol(entry.symbol)+" ", 1)}
		for _, detail := range entry.details {
			block = append(block, "  |   +-> "+line(detail))
		}
		remaining := availableHistory - len(historyRows)
		if remaining <= 0 {
			break
		}
		if len(block) > remaining {
			block = block[:remaining]
		}
		historyRows = append(historyRows, block...)
	}
	if len(historyRows) == 0 {
		historyRows = append(historyRows, "  | ·  No events yet")
	}
	rows = append(rows, historyRows...)
	rows = append(rows, panelBorder(width))
	if s.commandEnabled {
		rows = append(rows,
			panelSection("COMMAND", width),
			"  | "+line("cb › "+s.commandInput+"▌"),
		)
		for _, noticeLine := range commandNoticeLines {
			rows = append(rows, "  | "+ansiDim+line(noticeLine)+ansiReset)
		}
		rows = append(rows, "  | "+ansiDim+line(s.commandLifecycleHintLocked())+ansiReset)
		if s.serviceStopCommand != "" {
			rows = append(rows,
				"  | "+ansiDim+line(s.commandServiceStopHintLocked())+ansiReset,
				"  |   "+ansiDim+line(s.serviceStopCommand)+ansiReset,
			)
		}
		rows = append(rows, panelBorder(width))
	}
	// A heavily zoomed or split terminal can be only a handful of rows tall.
	// Keep the authoritative status and command line visible without letting a
	// nominally "in-place" frame spill into scrollback and multiply snapshots.
	if len(rows) > height {
		compactRows := []string{
			panelBorder(width),
			panelRow("ContextBridge  "+cleanTerminalLabel(s.bannerVersion, 24), width),
			panelSection("STATUS", width),
			status,
		}
		if s.commandEnabled {
			compactRows = append(compactRows,
				panelSection("COMMAND", width),
				"  | "+line("cb › "+s.commandInput+"▌"),
			)
			notices := commandNoticeLines
			noticeCapacity := max(1, height-len(compactRows)-1)
			if len(notices) > noticeCapacity && noticeCapacity == 1 {
				notices = []string{s.commandHintLocked()}
			}
			for index, noticeLine := range notices {
				if index == noticeCapacity {
					break
				}
				compactRows = append(compactRows, "  | "+ansiDim+line(noticeLine)+ansiReset)
			}
			compactRows = append(compactRows, panelBorder(width))
		} else {
			if len(statusDetails) > 0 {
				compactRows = append(compactRows, statusDetails[0])
			}
			compactRows = append(compactRows, panelSection(fmt.Sprintf("HISTORY · %d", s.historyTotal), width))
			if len(historyRows) > 0 {
				compactRows = append(compactRows, historyRows[0])
			}
			compactRows = append(compactRows, panelBorder(width))
		}
		rows = compactRows
	}
	for index, value := range rows {
		rows[index] = clipANSIColumns(value, max(1, width))
	}
	if !s.panelStarted {
		fmt.Fprint(s.out, "\x1b[?1049h\x1b[?25l")
		s.panelStarted = true
	}
	fmt.Fprint(s.out, "\x1b[H\x1b[2J", strings.Join(rows, "\n"))
}

func (s *Session) drawStatusLocked() {
	if !s.interactive || s.status == "" {
		return
	}
	if s.panelStarted {
		s.renderPanelLocked()
		return
	}
	if s.widthFn != nil {
		if width := s.widthFn(); width > 0 {
			s.width = width
		}
	}
	if s.observing {
		s.drawObservedStatusLocked()
		return
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	elapsed := compactDuration(time.Since(s.statusSince))
	slots := max(1, s.slots)
	load := min(100, len(s.jobs)*100/slots)
	identity := nodeLabel(s.node, s.nodeID, true)
	compact := s.width > 0 && s.width < 110
	indicators := indicatorLabel(s.capabilities)
	if compact {
		indicators = compactIndicatorLabel(s.capabilities)
	}
	var line string
	if s.status == "Idle" {
		if compact {
			line = fmt.Sprintf("  %s◇%s %s %d/%d·%d%% %s %s %s", ansiGreen, ansiReset, elapsed, len(s.jobs), slots, load, compactGPUState(s.capabilities), indicators, identity)
		} else {
			line = fmt.Sprintf("  %s◇%s [%sIdle%s %s] [%s%d/%d jobs · %d%%%s] %s %s%s", ansiGreen, ansiReset, ansiGreen, ansiReset, elapsed, ansiDim, len(s.jobs), slots, load, ansiReset, indicators, identity, optionalBracket(s.hardware))
		}
	} else {
		barWidth := 14
		if s.width > 0 && s.width < 100 {
			barWidth = 8
		}
		bar := pulseBar(s.frame, barWidth)
		status := s.status
		if len(s.jobs) > 0 {
			status, elapsed = s.visibleJobStatusLocked(40)
		}
		if compact {
			line = fmt.Sprintf("  %s%s%s %d/%d·%d%% %s %s %s %s", ansiCyan, frames[s.frame%len(frames)], ansiReset, len(s.jobs), slots, load, compactGPUState(s.capabilities), indicators, ansiYellow+status+ansiReset, elapsed)
		} else {
			line = fmt.Sprintf("  %s%s%s [%s%s%s] [%d/%d jobs · %d%%] %s %s %s %s", ansiCyan, frames[s.frame%len(frames)], ansiReset, ansiCyan, bar, ansiReset, len(s.jobs), slots, load, indicators, ansiYellow+status+ansiReset, elapsed, identity)
		}
	}
	s.drawLineLocked(line)
}

func (s *Session) drawObservedStatusLocked() {
	var line string
	if !s.observedOnline {
		line = "  " + ansiYellow + "↻" + ansiReset + " [service offline] [retrying automatically]"
	} else {
		adapter := "adapter offline"
		if s.observed.AdapterConnected {
			adapter = fmt.Sprintf("adapter %d/%d busy", s.observed.BusyEndpoints, s.observed.ActiveEndpoints)
		}
		line = fmt.Sprintf("  %s◇%s [service %s] [queue %d] [%s] [jobs %d · failed %d]", ansiGreen, ansiReset,
			cleanTerminalLabel(s.observed.Version, 30), s.observed.Queued, adapter, s.observed.JobsTotal, s.observed.JobsFailed)
		if s.observed.GPU != "" {
			line += fmt.Sprintf(" [%s · %d%%]", cleanTerminalLabel(s.observed.GPU, 36), s.observed.GPUUtilization)
		}
	}
	s.drawLineLocked(line)
}

func (s *Session) drawLineLocked(line string) {
	if s.width > 0 {
		line = clipANSIColumns(line, max(1, s.width-2))
	}
	if s.console != nil && drawConsoleStatus(s.console, line) {
		return
	}
	s.clearStatusLocked()
	fmt.Fprint(s.out, line)
}

func (s *Session) visibleJobStatusLocked(limit int) (string, string) {
	ids := make([]string, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	index := (s.frame / 20) % len(ids)
	id := ids[index]
	job := s.jobs[id]
	phase := progressPreview(empty(job.detail, empty(job.phase, "working")), 16)
	parts := []string{}
	if len(ids) > 1 {
		parts = append(parts, fmt.Sprintf("job %d/%d", index+1, len(ids)), progressPreview(empty(job.task, "generation"), 16))
	}
	phaseIndex := len(parts)
	parts = append(parts, phase)
	if job.percent > 0 {
		parts[phaseIndex] += fmt.Sprintf(" %d%%", job.percent)
	}
	if job.sequence > 0 {
		parts = append(parts, fmt.Sprintf("iter %d", job.sequence))
	}
	if job.selection != "" {
		parts = append(parts, progressPreview(job.selection, 36))
	}
	status := strings.Join(parts, " · ")
	if job.preview != "" {
		previewLimit := 80
		if limit > 0 {
			previewLimit = limit - len([]rune(status)) - 3
		}
		if previewLimit >= 4 {
			status += " › " + progressPreview(job.preview, previewLimit)
		}
	}
	if limit > 0 {
		status = truncateRunes(status, limit)
	}
	return status, compactDuration(time.Since(job.started))
}

func coloredSymbol(symbol string) string {
	color := ansiCyan
	switch symbol {
	case "✓":
		color = ansiGreen
	case "!":
		color = ansiRed
	case "↻":
		color = ansiYellow
	}
	return color + symbol + ansiReset
}

func (s *Session) clearStatusLocked() {
	if s.interactive {
		if s.console != nil && clearConsoleStatus(s.console) {
			return
		}
		fmt.Fprint(s.out, "\r\x1b[2K")
	}
}

func pulseBar(frame, width int) string {
	position := frame % (width*2 - 2)
	if position >= width {
		position = width*2 - 2 - position
	}
	var result strings.Builder
	for index := 0; index < width; index++ {
		if index >= position && index < position+3 {
			result.WriteRune('━')
		} else {
			result.WriteRune('─')
		}
	}
	return result.String()
}

// travelBar is the panel's one-way activity cue. It wraps at the right edge
// instead of bouncing, so an active job is visibly moving left to right.
func travelBar(frame, width int) string {
	width = max(4, width)
	position := (frame / 2) % (width - 2)
	bar := []rune(strings.Repeat("─", width))
	for index := position; index < position+3; index++ {
		bar[index] = '━'
	}
	return string(bar)
}

func colorPanelState(value, state string) string {
	color := ansiDim
	switch state {
	case "idle":
		color = ansiGreen
	case "working", "läuft":
		color = ansiCyan
	case "kühlt ab":
		color = ansiYellow
	case "offline":
		color = ansiRed
	}
	return strings.Replace(value, " · "+state, " · "+color+state+ansiReset, 1)
}

func colorPanelConnection(value string) string {
	for _, word := range []string{"Relay connected", "Relay verbunden", "Attached to running service"} {
		if strings.Contains(value, word) {
			return strings.Replace(value, word, ansiGreen+word+ansiReset, 1)
		}
	}
	for _, word := range []string{"nicht erreichbar", "unavailable", "rejected"} {
		if strings.Contains(value, word) {
			return strings.Replace(value, word, ansiRed+word+ansiReset, 1)
		}
	}
	return value
}

func colorPanelStatus(value string, s *Session) string {
	state, color := "Idle", ansiGreen
	if s.observing {
		if !s.observedOnline {
			state, color = "Offline", ansiRed
		} else if s.observed.ActiveJobs > 0 || s.observed.BusyEndpoints > 0 {
			state, color = "Working", ansiCyan
		}
	} else if len(s.jobs) > 0 || (s.status != "" && s.status != "Idle") {
		state, color = s.status, ansiCyan
		if s.status == "Offline" || s.status == "Waiting for network" {
			color = ansiYellow
		}
	}
	value = strings.Replace(value, "◇", color+"◇"+ansiReset, 1)
	if state != "" {
		value = strings.Replace(value, state, color+state+ansiReset, 1)
	}
	return value
}

func colorGPUPercent(value string, utilization int) string {
	color := ansiGreen
	if utilization >= 90 {
		color = ansiRed
	} else if utilization >= 70 {
		color = ansiYellow
	}
	percentage := fmt.Sprintf("%d%%", utilization)
	value = strings.Replace(value, percentage, color+percentage+ansiReset, 1)
	if strings.Contains(value, "GPU aktiv") {
		value = strings.Replace(value, "GPU aktiv", color+"GPU aktiv"+ansiReset, 1)
	} else if strings.Contains(value, "GPU ready") {
		value = strings.Replace(value, "GPU ready", ansiDim+"GPU ready"+ansiReset, 1)
	} else if strings.Contains(value, "Zero-GPU") {
		value = strings.Replace(value, "Zero-GPU", ansiDim+"Zero-GPU"+ansiReset, 1)
	}
	return value
}

func compactDuration(value time.Duration) string {
	if value < time.Second {
		return fmt.Sprintf("%dms", max(0, value.Milliseconds()))
	}
	if value < time.Minute {
		return fmt.Sprintf("%.1fs", value.Seconds())
	}
	seconds := int64(value.Round(time.Second) / time.Second)
	if seconds < 3600 {
		return fmt.Sprintf("%dm%ds", seconds/60, seconds%60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%dh%dm", seconds/3600, seconds%3600/60)
	}
	return fmt.Sprintf("%dd%dh", seconds/86400, seconds%86400/3600)
}

func millisecondsDuration(value uint64) time.Duration {
	maximum := uint64(math.MaxInt64 / int64(time.Millisecond))
	if value > maximum {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(value) * time.Millisecond
}

func nodeLabel(name, id string, colored bool) string {
	name = empty(cleanTerminalLabel(name, 100), "local")
	discriminator := cluster.NodeDiscriminator(id)
	if discriminator == "" {
		if colored {
			return ansiDim + name + ansiReset
		}
		return name
	}
	if colored {
		return ansiDim + name + ansiOrange + "#" + ansiDim + discriminator + ansiReset
	}
	return name + "#" + discriminator
}

func indicatorLabel(cap cluster.Capabilities) string {
	return buildIndicatorLabel(cap, false)
}

func compactIndicatorLabel(cap cluster.Capabilities) string {
	return buildIndicatorLabel(cap, true)
}

func buildIndicatorLabel(cap cluster.Capabilities, compact bool) string {
	sources := cap.Sources
	if len(sources) == 0 {
		sources = cluster.IndicatorSources(cap)
	}
	modes := cap.Modes
	if len(modes) == 0 {
		modes = cluster.IndicatorModes(cap)
	}
	sourceLabels := map[string]struct{ label, color string }{
		"adapter": {"◆ADP", ansiGreen}, "local": {"▣LOC", ansiCyan},
	}
	modeLabels := []struct{ key, label, color string }{
		{"text", "TXT", ansiGreen}, {"vision", "VIS", ansiCyan}, {"image", "IMG", ansiYellow},
		{"audio", "AUD", ansiPurple}, {"music", "MUS", ansiPurple}, {"video", "VID", ansiRed},
		{"files", "FIL", ansiBlue}, {"embedding", "EMB", ansiCyan},
	}
	if compact {
		sourceLabels = map[string]struct{ label, color string }{
			"adapter": {"◆A", ansiGreen}, "local": {"▣L", ansiCyan},
		}
		for index, short := range []string{"TX", "VI", "IM", "AU", "MU", "VD", "FI", "EM"} {
			modeLabels[index].label = short
		}
	}
	parts := []string{}
	for _, source := range []string{"adapter", "local"} {
		if containsIndicator(sources, source) {
			item := sourceLabels[source]
			parts = append(parts, item.color+item.label+ansiReset)
		}
	}
	for _, mode := range modeLabels {
		color := ansiDim
		if containsIndicator(modes, mode.key) {
			color = mode.color
		}
		parts = append(parts, color+mode.label+ansiReset)
	}
	return strings.Join(parts, " ")
}

func containsIndicator(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// clipANSIColumns keeps a transient status within the current viewport after
// font zoom or terminal resizing. ANSI color codes consume no columns.
func clipANSIColumns(value string, limit int) string {
	var result strings.Builder
	columns := 0
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			end := index + 2
			for end < len(value) && value[end] != 'm' {
				end++
			}
			if end < len(value) {
				result.WriteString(value[index : end+1])
				index = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		width := 1
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			width = 0
		} else if r >= 0x1100 && (r <= 0x115F || r >= 0x2329 && r <= 0x232A || r >= 0x2E80 && r <= 0xA4CF || r >= 0xAC00 && r <= 0xD7A3 || r >= 0xF900 && r <= 0xFAFF || r >= 0xFE10 && r <= 0xFE19 || r >= 0xFE30 && r <= 0xFE6F || r >= 0xFF00 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6 || r >= 0x1F300 && r <= 0x1FAFF) {
			width = 2
		}
		if columns+width > limit {
			break
		}
		result.WriteRune(r)
		columns += width
		index += size
	}
	result.WriteString(ansiReset)
	return result.String()
}

func compactError(value string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "no such host") || strings.Contains(value, "server misbehaving") {
		return "DNS/network not ready"
	}
	if len(value) > 160 {
		return value[:157] + "..."
	}
	return value
}

func shortID(value string) string {
	if len(value) <= 20 {
		return value
	}
	return value[:12] + "…" + value[len(value)-5:]
}

func progressPreview(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	characters := []rune(value)
	if limit <= 0 || len(characters) <= limit {
		return value
	}
	return "…" + string(characters[len(characters)-limit+1:])
}

func truncateRunes(value string, limit int) string {
	characters := []rune(value)
	if limit <= 0 || len(characters) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(characters[:limit-1]) + "…"
}

func empty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func optionalBracket(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "  [" + value + "]"
}

func capabilityLabel(capability cluster.Capabilities) string {
	parts := []string{}
	if len(capability.GPUs) > 0 {
		gpu := capability.GPUs[0]
		maximumUtilization := gpu.Utilization
		bestFree := gpu.MemoryFree
		for _, candidate := range capability.GPUs[1:] {
			maximumUtilization = max(maximumUtilization, candidate.Utilization)
			bestFree = max(bestFree, candidate.MemoryFree)
		}
		state := "GPU ready"
		if maximumUtilization > 0 {
			state = "GPU aktiv"
		}
		if len(capability.GPUs) == 1 {
			parts = append(parts, fmt.Sprintf("%s · %s · %d%% · %s VRAM free", cleanTerminalLabel(gpu.Name, 40), state, maximumUtilization, humanBytes(bestFree)))
		} else {
			parts = append(parts, fmt.Sprintf("%d GPUs · %s · max %d%% · best GPU %s VRAM free", len(capability.GPUs), state, maximumUtilization, humanBytes(bestFree)))
		}
	} else {
		parts = append(parts, "Zero-GPU")
	}
	if capability.MemoryTotal > 0 {
		parts = append(parts, humanBytes(capability.MemoryFree)+" RAM free")
	}
	return strings.Join(parts, " · ")
}

func compactGPUState(capability cluster.Capabilities) string {
	if len(capability.GPUs) == 0 {
		return ansiDim + "0GPU" + ansiReset
	}
	active := false
	for _, gpu := range capability.GPUs {
		active = active || gpu.Utilization > 0
	}
	label := "GPU"
	if len(capability.GPUs) > 1 {
		label = fmt.Sprintf("%dGPU", len(capability.GPUs))
	}
	if active {
		return ansiGreen + label + "+" + ansiReset
	}
	return ansiYellow + label + "~" + ansiReset
}

func humanBytes(value uint64) string {
	const gib = uint64(1 << 30)
	if value >= gib {
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gib))
	}
	return fmt.Sprintf("%.0f MiB", float64(value)/float64(1<<20))
}
