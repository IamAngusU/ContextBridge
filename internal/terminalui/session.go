package terminalui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

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
	task    string
	phase   string
	detail  string
	percent int
	started time.Time
}

// Session renders an animated single-line status in a real terminal and
// concise transition logs when stdout is redirected to a service log.
type Session struct {
	out         io.Writer
	interactive bool
	mu          sync.Mutex
	partial     string
	status      string
	statusSince time.Time
	frame       int
	retries     int
	jobs        map[string]jobState
	node        string
	slots       int
	hardware    string
	done        chan struct{}
	closed      chan struct{}
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
	session := &Session{out: output, interactive: interactive, jobs: map[string]jobState{}, done: make(chan struct{}), closed: make(chan struct{})}
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
		s.retries = 0
		s.setStatusLocked("Idle", time.Now())
	case cluster.WorkerJobStarted:
		s.jobs[event.JobID] = jobState{task: event.Task, phase: "starting", started: time.Now()}
		s.writeEventLocked("→", fmt.Sprintf("Job %s · %s", shortID(event.JobID), empty(event.Task, "generation")))
		s.refreshJobStatusLocked()
	case cluster.WorkerJobProgress:
		job := s.jobs[event.JobID]
		job.phase = empty(event.Phase, "working")
		job.detail = event.Detail
		job.percent = event.Percent
		s.jobs[event.JobID] = job
		s.refreshJobStatusLocked()
	case cluster.WorkerJobCompleted:
		delete(s.jobs, event.JobID)
		s.writeEventLocked("✓", fmt.Sprintf("Job %s completed · %s", shortID(event.JobID), compactDuration(time.Duration(event.ComputeMS)*time.Millisecond)))
		s.refreshJobStatusLocked()
	case cluster.WorkerJobFailed:
		delete(s.jobs, event.JobID)
		s.writeEventLocked("!", fmt.Sprintf("Job %s needs attention · %s", shortID(event.JobID), compactError(event.Error)))
		s.refreshJobStatusLocked()
	}
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
	for _, job := range s.jobs {
		phase := empty(job.detail, empty(job.phase, "working"))
		if job.percent > 0 {
			phase += fmt.Sprintf(" · %d%%", job.percent)
		}
		label := fmt.Sprintf("%s · %s", empty(job.task, "generation"), phase)
		if len(s.jobs) > 1 {
			label += fmt.Sprintf(" · %d parallel jobs", len(s.jobs))
		}
		s.setStatusLocked(label, job.started)
		return
	}
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
	bar := pulseBar(s.frame, 14)
	fmt.Fprintf(s.out, "\r\x1b[2K  %s%s%s  [%s%s%s]  [%d/%d jobs]  %s%s%s  %s", ansiCyan, frames[s.frame%len(frames)], ansiReset, ansiCyan, bar, ansiReset, len(s.jobs), max(1, s.slots), ansiYellow, s.status, ansiReset, elapsed)
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
