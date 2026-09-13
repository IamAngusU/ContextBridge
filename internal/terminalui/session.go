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

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

const (
	ansiReset  = "\x1b[0m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiDim    = "\x1b[2m"
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

// Session renders an animated single-line status in a real terminal and
// concise transition logs when stdout is redirected to a service log.
type Session struct {
	out               io.Writer
	interactive       bool
	mu                sync.Mutex
	partial           string
	status            string
	statusSince       time.Time
	frame             int
	retries           int
	jobs              map[string]jobState
	node              string
	slots             int
	hardware          string
	browserSelections map[int]string
	width             int
	done              chan struct{}
	closed            chan struct{}
}

func New(output *os.File) *Session {
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
	session := &Session{out: output, interactive: interactive, jobs: map[string]jobState{}, browserSelections: map[int]string{}, width: terminalWidth(output), done: make(chan struct{}), closed: make(chan struct{})}
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
		s.node, s.slots = event.NodeName, event.Slots
		s.hardware = capabilityLabel(event.Capabilities)
		message := fmt.Sprintf("Relay connected · %s · %d slot", event.NodeName, event.Slots)
		if event.Slots != 1 {
			message += "s"
		}
		if s.retries > 0 {
			message += fmt.Sprintf(" · recovered after %d retries", s.retries)
		}
		s.writeEventLocked("✓", message)
		s.recordBrowserSelectionsLocked(event.Capabilities.BrowserSessions)
		s.retries = 0
		s.setStatusLocked("Idle", time.Now())
	case cluster.WorkerCapabilities:
		s.hardware = capabilityLabel(event.Capabilities)
		s.recordBrowserSelectionsLocked(event.Capabilities.BrowserSessions)
	case cluster.WorkerJobStarted:
		selection := requestedSelection(event)
		s.jobs[event.JobID] = jobState{task: event.Task, phase: "starting", selection: selection, started: time.Now()}
		message := fmt.Sprintf("Job %s · %s", shortID(event.JobID), empty(event.Task, "generation"))
		if selection != "" {
			message += " · " + selection
		}
		s.writeEventLocked("→", message)
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
		if event.ReportedProvider == "browser" {
			if event.ReportedModel != "" {
				message += " · Tab meldet: " + cleanTerminalLabel(event.ReportedModel, 80)
			}
			if event.ReportedReasoning != "" {
				message += " · Denkstufe: " + cleanTerminalLabel(event.ReportedReasoning, 40)
			}
		} else if event.ReportedModel != "" {
			message += " · verwendet: " + cleanTerminalLabel(event.ReportedProvider, 30) + " · " + cleanTerminalLabel(event.ReportedModel, 80)
		}
		s.writeEventLocked("✓", message)
		s.refreshJobStatusLocked()
	case cluster.WorkerJobFailed:
		delete(s.jobs, event.JobID)
		s.writeEventLocked("!", fmt.Sprintf("Job %s needs attention · %s", shortID(event.JobID), compactError(event.Error)))
		s.refreshJobStatusLocked()
	}
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
	current := make(map[int]string, len(sessions))
	for _, tab := range sessions {
		if tab.TabID <= 0 {
			continue
		}
		parts := []string{cleanTerminalLabel(empty(tab.Profile, "browser"), 30)}
		if tab.CurrentModel != "" {
			parts = append(parts, "Modell: "+cleanTerminalLabel(tab.CurrentModel, 80))
		} else {
			parts = append(parts, "Modell noch nicht erkannt")
		}
		if tab.CurrentReasoning != "" {
			parts = append(parts, "Denkstufe: "+cleanTerminalLabel(tab.CurrentReasoning, 40))
		}
		current[tab.TabID] = strings.Join(parts, " · ")
	}
	ids := make([]int, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if s.browserSelections[id] != current[id] {
			s.writeEventLocked("◇", fmt.Sprintf("Tab %d · %s", id, current[id]))
		}
	}
	for id := range s.browserSelections {
		if _, ok := current[id]; !ok {
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
			s.drawStatusLocked()
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
	if s.interactive {
		s.clearStatusLocked()
		fmt.Fprintf(s.out, "  %s  %s\n", coloredSymbol(symbol), message)
		s.drawStatusLocked()
		return
	}
	fmt.Fprintf(s.out, "%s  %s  %s\n", time.Now().Format("2006/01/02 15:04:05"), symbol, message)
}

func (s *Session) drawStatusLocked() {
	if !s.interactive || s.status == "" {
		return
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	elapsed := compactDuration(time.Since(s.statusSince))
	if s.status == "Idle" {
		pool := fmt.Sprintf("0/%d jobs", max(1, s.slots))
		fmt.Fprintf(s.out, "\r\x1b[2K  %s◇%s  [%sIdle%s %s]  [%s%s%s]  [%s]%s", ansiGreen, ansiReset, ansiGreen, ansiReset, elapsed, ansiDim, pool, ansiReset, empty(s.node, "local"), optionalBracket(s.hardware))
		return
	}
	barWidth := 14
	if s.width > 0 && s.width < 100 {
		barWidth = 8
	}
	bar := pulseBar(s.frame, barWidth)
	status := s.status
	// Reserve space for the spinner, pulse bar, slot count, colors and elapsed
	// time. Keeping the visible text within the console width prevents wrapping.
	statusLimit := 0
	if s.width > 0 {
		fixed := barWidth + 30 + len(fmt.Sprintf("%d/%d", len(s.jobs), max(1, s.slots))) + len(elapsed)
		statusLimit = max(24, s.width-fixed)
	}
	if len(s.jobs) > 0 {
		status, elapsed = s.visibleJobStatusLocked(statusLimit)
	} else if statusLimit > 0 {
		status = truncateRunes(status, statusLimit)
	}
	fmt.Fprintf(s.out, "\r\x1b[2K  %s%s%s  [%s%s%s]  [%d/%d jobs]  %s%s%s  %s", ansiCyan, frames[s.frame%len(frames)], ansiReset, ansiCyan, bar, ansiReset, len(s.jobs), max(1, s.slots), ansiYellow, status, ansiReset, elapsed)
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
	return value.Round(time.Second).String()
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
		parts = append(parts, gpu.Name+" · "+humanBytes(gpu.MemoryFree)+" VRAM free")
	}
	if capability.MemoryTotal > 0 {
		parts = append(parts, humanBytes(capability.MemoryFree)+" RAM free")
	}
	return strings.Join(parts, " · ")
}

func humanBytes(value uint64) string {
	const gib = uint64(1 << 30)
	if value >= gib {
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gib))
	}
	return fmt.Sprintf("%.0f MiB", float64(value)/float64(1<<20))
}
