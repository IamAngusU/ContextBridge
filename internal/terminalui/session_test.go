package terminalui

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestCompactDurationKeepsDaysAndHours(t *testing.T) {
	for _, item := range []struct {
		duration time.Duration
		want     string
	}{
		{time.Minute + 23*time.Second, "1m23s"},
		{3*time.Hour + 5*time.Minute, "3h5m"},
		{2*24*time.Hour + 4*time.Hour, "2d4h"},
	} {
		if got := compactDuration(item.duration); got != item.want {
			t.Fatalf("compactDuration(%s) = %q, want %q", item.duration, got, item.want)
		}
	}
}

func TestPanelBannerAndEventHierarchy(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 90 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}}
	session.Banner("v0.test", "2 components · local bridge · worker")
	capabilities := cluster.Capabilities{BrowserSessions: []cluster.BrowserSessionCapability{{TabID: 42, Profile: "chatgpt", CurrentModel: "GPT-5.6 Sol"}}}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerConnected, NodeName: "test-pc", Slots: 2, Capabilities: capabilities})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobStarted, JobID: "job-42", Task: "generation", Provider: "browser", Profile: "chatgpt", Model: "GPT-5.6 Sol"})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobCompleted, JobID: "job-42", ComputeMS: 1500, ReportedProvider: "browser", ReportedModel: "GPT-5.6 Sol"})
	got := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
	for _, label := range []string{
		"by IamAngusU", "https://github.com/IamAngusU/ContextBridge",
		"+-- CONNECTION", "+-- AI TABS", "+-- JOBS", "+-> requested · browser · chatgpt",
		"+-> route · browser", "+-> model · GPT-5.6 Sol",
	} {
		if !strings.Contains(got, label) {
			t.Fatalf("panel output is missing %q: %q", label, got)
		}
	}
	if strings.Count(got, "+-- JOBS") != 1 {
		t.Fatalf("job section was printed more than once: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "  +") || strings.HasPrefix(line, "  |") {
			plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(line, "")
			if utf8.RuneCountInString(plain) > 88 {
				t.Fatalf("panel line overflows: %q", line)
			}
		}
	}
}

func TestPanelFallsBackWhenTerminalTooNarrow(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 20 }}
	session.Banner("v0.test", "local bridge")
	session.writeEventLocked("✓", "connected")
	if strings.Contains(output.String(), "+--") || !strings.Contains(output.String(), "ContextBridge  v0.test") {
		t.Fatalf("narrow terminal did not use the classic fallback: %q", output.String())
	}
}

func TestPanelDoesNotChangeRedirectedLogShape(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, style: "panel"}
	session.Banner("v0.test", "worker")
	session.writeEventLocked("✓", "connected")
	if !strings.HasPrefix(output.String(), "ContextBridge v0.test · worker\n") || strings.Contains(output.String(), "+--") {
		t.Fatalf("redirected panel output changed: %q", output.String())
	}
}

func TestClassicInteractiveOutputIsPreserved(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "classic"}
	session.Banner("v0.test", "worker")
	session.writeEventWithDetailLocked("✓", "completed", "model metadata unavailable")
	got := output.String()
	if !strings.Contains(got, "\n  ContextBridge  v0.test\n  worker\n\n") ||
		!strings.Contains(got, "  "+ansiGreen+"✓"+ansiReset+"  completed\n") ||
		!strings.Contains(got, "     "+ansiYellow+"└─"+ansiReset+" model metadata unavailable\n") ||
		strings.Contains(got, "+--") {
		t.Fatalf("classic interactive output changed: %q", got)
	}
}

func TestStatusFitsCurrentWidthAfterZoom(t *testing.T) {
	var output bytes.Buffer
	width := 160
	cap := cluster.Capabilities{GPUs: []cluster.GPUCapability{{Name: "RTX 3080", Utilization: 12}}, MaxConcurrent: 4,
		BrowserSessions: []cluster.BrowserSessionCapability{{TabID: 1, Profile: "chatgpt"}, {TabID: 2, Profile: "gemini"}},
		Tasks:           []string{"generation", "vision"}}
	session := &Session{out: &output, interactive: true, status: "Idle", statusSince: time.Now().Add(-49 * time.Hour),
		node: "Angus-PC", nodeID: "node_5a18aecdcb05db6887c355031ad5ca35", slots: 4, capabilities: cap,
		widthFn: func() int { return width }}
	session.drawStatusLocked()
	width = 68
	session.drawStatusLocked()
	last := output.String()[strings.LastIndex(output.String(), "\x1b[2K")+len("\x1b[2K"):]
	visible := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(last, "")
	if got := utf8.RuneCountInString(visible); got > width-2 {
		t.Fatalf("zoomed status uses %d columns in a %d-column terminal: %q", got, width, last)
	}
	for _, mode := range []string{"TX", "VI", "IM", "AU", "MU", "VD", "FI", "EM"} {
		if !strings.Contains(last, mode) {
			t.Fatalf("zoomed status lost the %s indicator: %q", mode, last)
		}
	}
	if !strings.Contains(output.String(), "#") || !strings.Contains(output.String(), "2d1h") {
		t.Fatalf("status is missing the discriminator or day/hour duration: %q", output.String())
	}
}

func TestIndicatorsKeepFixedModeOrderAndGrayUnknowns(t *testing.T) {
	cap := cluster.Capabilities{Sources: []string{"gemini", "chatgpt"}, Modes: []string{"vision", "text", "music"}}
	label := indicatorLabel(cap)
	for _, mode := range []string{"TXT", "VIS", "IMG", "AUD", "MUS", "VID", "FIL", "EMB"} {
		if !strings.Contains(label, mode) {
			t.Fatalf("missing mode %s: %q", mode, label)
		}
	}
	if strings.Index(label, "◉GPT") > strings.Index(label, "✦GEM") || strings.Index(label, "TXT") > strings.Index(label, "VIS") || !strings.Contains(label, ansiDim+"IMG") {
		t.Fatalf("indicator order/colors are unstable: %q", label)
	}
}

func TestNonInteractiveSessionDeduplicatesRetryNoise(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	session := New(writer)
	for attempt := 1; attempt <= 10; attempt++ {
		session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerRetrying, Attempt: attempt, RetryIn: time.Second, Error: `dial tcp: lookup angusu.de: no such host`})
	}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerConnected, NodeName: "test-pc", Slots: 1})
	session.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw)
	if strings.Count(output, "Relay unavailable") != 2 {
		t.Fatalf("retry log was not deduplicated: %s", output)
	}
	if strings.Contains(output, "lookup angusu.de") || !strings.Contains(output, "recovered after 10 retries") {
		t.Fatalf("connection lifecycle was not summarized: %s", output)
	}
}

func TestProgressPreviewKeepsLatestTextIteration(t *testing.T) {
	actual := progressPreview("  first\nsecond   third  ", 12)
	if actual != "…econd third" {
		t.Fatalf("unexpected progress preview %q", actual)
	}
	if actual := progressPreview("short answer", 80); actual != "short answer" {
		t.Fatalf("short progress text changed: %q", actual)
	}
}

func TestWorkerConsoleSeparatesRequestedAndReportedModel(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	session := New(writer)
	capabilities := cluster.Capabilities{BrowserSessions: []cluster.BrowserSessionCapability{{TabID: 42, Profile: "gemini", CurrentModel: "Pro Erweitert"}}}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerConnected, NodeName: "test-pc", Slots: 2, Capabilities: capabilities})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobStarted, JobID: "job-123456789", Task: "generation", Provider: "browser", Profile: "gemini", Model: "3.1 Pro", Reasoning: "high"})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobCompleted, JobID: "job-123456789", ComputeMS: 1000, ReportedProvider: "browser", ReportedModel: "Pro Erweitert"})
	session.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw)
	if strings.Count(output, "Tab 42 · gemini · Modell: Pro Erweitert") != 1 {
		t.Fatalf("tab model update should be deduplicated: %s", output)
	}
	if !strings.Contains(output, "Modell angefragt: 3.1 Pro · Denkstufe angefragt: high") {
		t.Fatalf("requested selection missing: %s", output)
	}
	if !strings.Contains(output, "Tab meldet: Pro Erweitert") {
		t.Fatalf("reported selection missing: %s", output)
	}
}

func TestCompletedBrowserJobKeepsMetadataGapBelowSuccess(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, jobs: map[string]jobState{}}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobCompleted, JobID: "job-123456789", ComputeMS: 1500, ReportedProvider: "browser"})
	got := output.String()
	if !strings.Contains(got, "✓  Job job-123456789 completed · 1.5s\n     └─ model metadata unavailable · browser selection unverified\n") {
		t.Fatalf("success and observational metadata must be distinct: %q", got)
	}
	if strings.Contains(got, "needs attention") || strings.Contains(got, "browser_model_unavailable") {
		t.Fatalf("missing model metadata must not become a job failure: %q", got)
	}
}

func TestWorkerConsoleDoesNotRepeatTabWhenReasoningTemporarilyDisappears(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	session := New(writer)
	capabilities := func(model, reasoning string) cluster.Capabilities {
		return cluster.Capabilities{BrowserSessions: []cluster.BrowserSessionCapability{{
			TabID: 1593324977, Profile: "chatgpt", CurrentModel: model, CurrentReasoning: reasoning,
		}}}
	}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerConnected, NodeName: "test-pc", Slots: 1, Capabilities: capabilities("", "Sehr hoch")})
	for range 4 {
		session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities("", "")})
		session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities("", "Sehr hoch")})
	}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities("GPT-5.6 Sol", "")})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities("", "Sofort")})
	session.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw)
	if strings.Count(output, "Tab 1593324977") != 3 || strings.Count(output, "Tab 1593324977 aktualisiert") != 2 {
		t.Fatalf("temporary empty readings should not create tab events: %s", output)
	}
	if !strings.Contains(output, "Modell: GPT-5.6 Sol · Denkstufe: Sofort") {
		t.Fatalf("real model and reasoning changes were not reported: %s", output)
	}
}
