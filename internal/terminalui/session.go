package terminalui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

const (
	ansiReset  = "\x1b[0m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiDim    = "\x1b[2m"
	ansiOrange = "\x1b[38;5;208m"
	ansiPurple = "\x1b[35m"
	ansiBlue   = "\x1b[34m"
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

type browserSelection struct {
	profile   string
	model     string
	reasoning string
}

// ServiceSnapshot is the read-only subset shown by an attached console. It
// reflects a service status response, not inferred worker or job events.
type ServiceSnapshot struct {
	Version          string
	Queued           int
	Completed        int
	BrowserConnected bool
	ActiveTabs       int
	BusyTabs         int
	Tabs             []cluster.BrowserSessionCapability
	JobsTotal        uint64
	JobsFailed       uint64
	GPU              string
	GPUUtilization   int
}

// Session renders an animated single-line status in a real terminal and
// concise transition logs when stdout is redirected to a service log.
type Session struct {
	out               io.Writer
	console           *os.File
	interactive       bool
	style             string
	section           string
	nextSection       string
	mu                sync.Mutex
	partial           string
	status            string
	statusSince       time.Time
	frame             int
	retries           int
	jobs              map[string]jobState
	node              string
	nodeID            string
	slots             int
	hardware          string
	capabilities      cluster.Capabilities
	browserSelections map[int]browserSelection
	width             int
	widthFn           func() int
	done              chan struct{}
	closed            chan struct{}
	observing         bool
	observedOnline    bool
	observed          ServiceSnapshot
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
	session := &Session{out: output, console: output, interactive: interactive, style: style, jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, width: terminalWidth(output), widthFn: func() int { return terminalWidth(output) }, done: make(chan struct{}), closed: make(chan struct{})}
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
		if s.panelEnabledLocked() && s.panelWidthLocked() >= 58 {
			width := s.panelWidthLocked()
			fmt.Fprint(s.out, "\n", panelBorder(width), "\n")
			fmt.Fprintln(s.out, panelRow("ContextBridge  "+cleanTerminalLabel(version, 24), width))
			fmt.Fprintln(s.out, panelRow(cleanTerminalLabel(components, 90), width))
			fmt.Fprintln(s.out, panelRow("by IamAngusU", width))
			fmt.Fprintln(s.out, panelRow("https://github.com/IamAngusU/ContextBridge", width))
			fmt.Fprint(s.out, panelBorder(width), "\n\n")
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
		if s.status == "" {
			s.setStatusLocked("Connecting to relay", time.Now())
		}
	case cluster.WorkerRetrying:
		s.retries = event.Attempt
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
		s.recordBrowserSelectionsLocked(event.Capabilities.BrowserSessions)
		s.retries = 0
		s.setStatusLocked("Idle", time.Now())
	case cluster.WorkerCapabilities:
		s.capabilities = event.Capabilities
		if event.Slots > 0 {
			s.slots = event.Slots
		}
		s.hardware = capabilityLabel(event.Capabilities)
		s.recordBrowserSelectionsLocked(event.Capabilities.BrowserSessions)
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
		message := fmt.Sprintf("Job %s completed · %s", shortID(event.JobID), compactDuration(time.Duration(event.ComputeMS)*time.Millisecond))
		var detail string
		if event.ReportedProvider == "browser" {
			if event.ReportedModel != "" {
				message += " · Tab meldet: " + cleanTerminalLabel(event.ReportedModel, 80)
			} else {
				detail = "model metadata unavailable · browser selection unverified"
			}
			if event.ReportedReasoning != "" {
				message += " · Denkstufe: " + cleanTerminalLabel(event.ReportedReasoning, 40)
			}
		} else if event.ReportedModel != "" {
			message += " · verwendet: " + cleanTerminalLabel(event.ReportedProvider, 30) + " · " + cleanTerminalLabel(event.ReportedModel, 80)
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
			s.writeEventWithDetailsLocked("✓", fmt.Sprintf("Job %s completed · %s", shortID(event.JobID), compactDuration(time.Duration(event.ComputeMS)*time.Millisecond)), details)
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
	if !wasOnline || previous.BrowserConnected != snapshot.BrowserConnected {
		s.nextSection = "AI TABS"
		if snapshot.BrowserConnected {
			s.writeEventLocked("✓", fmt.Sprintf("Browser connected · %d tab(s)", snapshot.ActiveTabs))
		} else {
			s.writeEventLocked("◇", "No browser extension connected")
		}
	}
	if snapshot.BrowserConnected {
		s.recordBrowserSelectionsLocked(snapshot.Tabs)
	} else if len(s.browserSelections) > 0 {
		s.browserSelections = map[int]browserSelection{}
	}
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
		parts = append(parts, "Modell angefragt: "+cleanTerminalLabel(event.Model, 80))
	}
	if event.Reasoning != "" {
		parts = append(parts, "Denkstufe angefragt: "+cleanTerminalLabel(event.Reasoning, 40))
	}
	return strings.Join(parts, " · ")
}

func (s *Session) recordBrowserSelectionsLocked(sessions []cluster.BrowserSessionCapability) {
	current := make(map[int]browserSelection, len(sessions))
	for _, tab := range sessions {
		if tab.TabID <= 0 {
			continue
		}
		selection := browserSelection{
			profile:   cleanTerminalLabel(empty(tab.Profile, "browser"), 30),
			model:     cleanTerminalLabel(tab.CurrentModel, 80),
			reasoning: cleanTerminalLabel(tab.CurrentReasoning, 40),
		}
		// An empty reading is not evidence that the user changed the model or
		// reasoning level. ChatGPT briefly removes controls while rerendering;
		// logging each missing/returning label creates duplicate tab lines.
		if previous, ok := s.browserSelections[tab.TabID]; ok && previous.profile == selection.profile {
			if selection.model == "" {
				selection.model = previous.model
			}
			if selection.reasoning == "" {
				selection.reasoning = previous.reasoning
			}
		}
		current[tab.TabID] = selection
	}
	ids := make([]int, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if s.browserSelections[id] != current[id] {
			selection := current[id]
			label := fmt.Sprintf("Tab %d", id)
			if _, previouslySeen := s.browserSelections[id]; previouslySeen {
				label += " aktualisiert"
			}
			parts := []string{selection.profile}
			if selection.model != "" {
				parts = append(parts, "Modell: "+selection.model)
			} else {
				parts = append(parts, "Modell noch nicht erkannt")
			}
			if selection.reasoning != "" {
				parts = append(parts, "Denkstufe: "+selection.reasoning)
			}
			s.nextSection = "AI TABS"
			s.writeEventLocked("◇", fmt.Sprintf("%s · %s", label, strings.Join(parts, " · ")))
		}
	}
	for id := range s.browserSelections {
		if _, ok := current[id]; !ok {
			s.nextSection = "AI TABS"
			s.writeEventLocked("◇", fmt.Sprintf("Tab %d getrennt", id))
		}
	}
	s.browserSelections = current
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
		s.clearStatusLocked()
	}
	if strings.TrimSpace(s.partial) != "" {
		s.writeEventLocked("·", strings.TrimSpace(s.partial))
		s.partial = ""
	}
	s.mu.Unlock()
	<-s.closed
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
			if s.status != "Idle" || s.frame%8 == 0 {
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
	return max(12, min(78, width-2))
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

func (s *Session) drawStatusLocked() {
	if !s.interactive || s.status == "" {
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
		browser := "browser offline"
		if s.observed.BrowserConnected {
			browser = fmt.Sprintf("browser %d/%d busy", s.observed.BusyTabs, s.observed.ActiveTabs)
		}
		line = fmt.Sprintf("  %s◇%s [service %s] [queue %d] [%s] [jobs %d · failed %d]", ansiGreen, ansiReset,
			cleanTerminalLabel(s.observed.Version, 30), s.observed.Queued, browser, s.observed.JobsTotal, s.observed.JobsFailed)
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
		"chatgpt": {"◉GPT", ansiGreen}, "gemini": {"✦GEM", ansiPurple}, "local": {"▣LOC", ansiCyan},
	}
	modeLabels := []struct{ key, label, color string }{
		{"text", "TXT", ansiGreen}, {"vision", "VIS", ansiCyan}, {"image", "IMG", ansiYellow},
		{"audio", "AUD", ansiPurple}, {"music", "MUS", ansiPurple}, {"video", "VID", ansiRed},
		{"files", "FIL", ansiBlue}, {"embedding", "EMB", ansiCyan},
	}
	if compact {
		sourceLabels = map[string]struct{ label, color string }{
			"chatgpt": {"◉G", ansiGreen}, "gemini": {"✦G", ansiPurple}, "local": {"▣L", ansiCyan},
		}
		for index, short := range []string{"TX", "VI", "IM", "AU", "MU", "VD", "FI", "EM"} {
			modeLabels[index].label = short
		}
	}
	parts := []string{}
	for _, source := range []string{"chatgpt", "gemini", "local"} {
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
		state := "GPU bereit"
		if gpu.Utilization > 0 {
			state = "GPU aktiv"
		}
		parts = append(parts, fmt.Sprintf("%s · %s · %d%% · %s VRAM frei", cleanTerminalLabel(gpu.Name, 40), state, gpu.Utilization, humanBytes(gpu.MemoryFree)))
	} else {
		parts = append(parts, "Zero-GPU")
	}
	if capability.MemoryTotal > 0 {
		parts = append(parts, humanBytes(capability.MemoryFree)+" RAM frei")
	}
	return strings.Join(parts, " · ")
}

func compactGPUState(capability cluster.Capabilities) string {
	if len(capability.GPUs) == 0 {
		return ansiDim + "0GPU" + ansiReset
	}
	if capability.GPUs[0].Utilization > 0 {
		return ansiGreen + "GPU+" + ansiReset
	}
	return ansiYellow + "GPU~" + ansiReset
}

func humanBytes(value uint64) string {
	const gib = uint64(1 << 30)
	if value >= gib {
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gib))
	}
	return fmt.Sprintf("%.0f MiB", float64(value)/float64(1<<20))
}
