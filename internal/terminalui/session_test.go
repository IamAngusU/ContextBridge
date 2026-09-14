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
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	got := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	for _, label := range []string{
		"by IamAngusU", "https://github.com/IamAngusU/ContextBridge",
		"+-- CONNECTION", "+-- AI TABS", "+-- [ChatGPT]", "+-- HISTORY", "+-> requested · browser · chatgpt",
		"+-> route · browser", "+-> model · GPT-5.6 Sol",
	} {
		if !strings.Contains(got, label) {
			t.Fatalf("panel output is missing %q: %q", label, got)
		}
	}
	if strings.Count(got, "+-- [ChatGPT]") == 0 || !strings.Contains(got, "[JOBS]") {
		t.Fatalf("provider group or history is missing: %q", got)
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

func TestAttachedConsoleOnlyLogsObservedChanges(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 90 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}}
	snapshot := ServiceSnapshot{Version: "v0.test", BrowserConnected: true, ActiveTabs: 1,
		Tabs: []cluster.BrowserSessionCapability{{TabID: 7, Profile: "chatgpt", CurrentModel: "GPT-5.6 Sol"}}}
	session.ObserveService(snapshot)
	session.ObserveService(snapshot)
	snapshot.Completed, snapshot.JobsTotal = 1, 1
	session.ObserveService(snapshot)
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	got := frames[len(frames)-1]
	if len(session.history) != 4 || strings.Count(got, "Attached to running service") < 1 ||
		!strings.Contains(got, "Completed total 1 (+1 since last check)") || !strings.Contains(got, "[ACTIVITY]") {
		t.Fatalf("attached console emitted duplicate or missing events: %q", got)
	}
	if !strings.Contains(got, "Warteschlange 0 · Browser 0/1 belegt") {
		t.Fatalf("attached console live status is missing: %q", got)
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

func TestBrowserTabsGroupProvidersAndPutWorkingBeforeIdle(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, browserSelections: map[int]browserSelection{}}
	tabs := []cluster.BrowserSessionCapability{
		{TabID: 80, Profile: "gemini", State: "waiting", CurrentModel: "Flash"},
		{TabID: 3, Profile: "chatgpt", State: "waiting", CurrentModel: "GPT-5.6 Sol"},
		{TabID: 6, Profile: "custom", State: "working"},
		{TabID: 5, Profile: "gemini", State: "working", CurrentModel: "Pro"},
		{TabID: 4, Profile: "chatgpt", State: "working", CurrentModel: "GPT-5.6 Sol"},
	}
	session.recordBrowserSelectionsLocked(tabs)
	got := output.String()
	previous := -1
	for _, label := range []string{"Tab 4 · chatgpt", "Tab 3 · chatgpt", "Tab 5 · gemini", "Tab 80 · gemini", "Tab 6 · custom"} {
		index := strings.Index(got, label)
		if index <= previous {
			t.Fatalf("provider/state ordering is wrong at %q: %s", label, got)
		}
		previous = index
	}
	if !strings.Contains(got, "Tab 4 · chatgpt · Modell: GPT-5.6 Sol · läuft") ||
		!strings.Contains(got, "Tab 3 · chatgpt · Modell: GPT-5.6 Sol · idle") {
		t.Fatalf("tab activity is not distinguished: %s", got)
	}
	session.recordBrowserSelectionsLocked(tabs)
	if output.String() != got {
		t.Fatalf("unchanged browser states were logged again: %s", output.String())
	}
	tabs[1].State = ""
	session.recordBrowserSelectionsLocked(tabs)
	if output.String() != got {
		t.Fatalf("a transient missing state created a false tab transition: %s", output.String())
	}
	tabs[1].State = "working"
	session.recordBrowserSelectionsLocked(tabs)
	if strings.Count(output.String(), "Tab 3 aktualisiert") != 1 || !strings.Contains(output.String(), "Tab 3 aktualisiert · chatgpt · Modell: GPT-5.6 Sol · läuft") {
		t.Fatalf("real state transition was not logged once: %s", output.String())
	}
}

func TestLocalProviderRemainsVisibleWithoutLoadedModel(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 90 },
		localModels: map[string]localModelSelection{}}
	models := []cluster.ModelCapability{
		{Provider: "ollama", Name: "ready-model"},
		{Provider: "browser", Name: "GPT-5.6 Sol", Loaded: true},
		{Provider: "ollama", Name: "loaded-model"},
	}
	session.Banner("v0.test", "worker")
	session.recordLocalModelsLocked(models, []string{"ollama"})
	if !strings.Contains(output.String(), "erreichbar · kein Modell geladen") {
		t.Fatalf("online provider without loaded models should be visible: %s", output.String())
	}
	models[2].Loaded = true
	session.recordLocalModelsLocked(models, []string{"ollama"})
	got := output.String()
	if !strings.Contains(got, "+-- [Lokal · ollama]") || !strings.Contains(got, "loaded-model · geladen") ||
		strings.Index(got, "loaded-model · geladen") > strings.Index(got, "ready-model · bereit · nicht geladen") {
		t.Fatalf("loaded local model not shown: %s", got)
	}
	previous := len(session.history)
	session.recordLocalModelsLocked(models, []string{"ollama"})
	if len(session.history) != previous {
		t.Fatalf("unchanged local model state was repeated: %s", output.String())
	}
	models[2].Loaded = false
	session.recordLocalModelsLocked(models, []string{"ollama"})
	if !strings.Contains(output.String(), "ollama erreichbar · kein Modell geladen") {
		t.Fatalf("unload transition should be reported once: %s", output.String())
	}
}

func TestPanelLiveGroupsResizeAndHistoryStaySeparate(t *testing.T) {
	var output bytes.Buffer
	width := 150
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return width }, heightFn: func() int { return 30 },
		browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
	session.Banner("v0.test", "worker")
	session.recordBrowserSelectionsLocked([]cluster.BrowserSessionCapability{
		{TabID: 11, Profile: "gemini", State: "waiting", CurrentModel: "Flash"},
		{TabID: 12, Profile: "chatgpt", State: "waiting", CurrentModel: "GPT-5.6 Sol"},
		{TabID: 13, Profile: "chatgpt", State: "working", CurrentModel: "GPT-5.6 Sol"},
	})
	session.recordLocalModelsLocked(nil, []string{"ollama"})
	width = 70
	session.renderPanelLocked()
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	latest := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	historyIndex := strings.Index(latest, "+-- HISTORY")
	if historyIndex < 0 {
		t.Fatalf("session history is missing: %s", latest)
	}
	live := latest[:historyIndex]
	for _, label := range []string{"+-- [ChatGPT]", "Tab 13", "Tab 12", "+-- [Gemini]", "Tab 11", "+-- [Lokal · ollama]", "erreichbar · kein Modell geladen"} {
		if !strings.Contains(live, label) {
			t.Fatalf("live area missing %q: %s", label, latest)
		}
	}
	if strings.Index(live, "Tab 13") > strings.Index(live, "Tab 12") || strings.Index(live, "+-- [ChatGPT]") > strings.Index(live, "+-- [Gemini]") {
		t.Fatalf("provider/activity order is wrong: %s", latest)
	}
	for _, row := range strings.Split(latest, "\n") {
		if utf8.RuneCountInString(row) > width {
			t.Fatalf("resized panel row exceeds %d columns: %q", width, row)
		}
	}
	if !strings.Contains(latest[historyIndex:], "[AI TABS]") {
		t.Fatalf("history lost tab transitions: %s", latest)
	}
}

func TestPanelClearsStaleSelectionsWhenServiceIsUnavailable(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 120 }, heightFn: func() int { return 32 },
		browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
	session.Banner("v0.test", "console")
	session.ObserveService(ServiceSnapshot{Version: "v0.test", BrowserConnected: true, ActiveTabs: 1,
		Tabs: []cluster.BrowserSessionCapability{{TabID: 77, Profile: "chatgpt", CurrentModel: "GPT-5.6 Sol"}}, LocalProviders: []string{"ollama"}})
	session.ObserveServiceUnavailable("service offline")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	live := strings.Split(frames[len(frames)-1], "+-- HISTORY")[0]
	if strings.Contains(live, "Tab 77") || strings.Contains(live, "[Lokal · ollama]") || !strings.Contains(live, "Dienst offline") {
		t.Fatalf("offline live panel retained stale selections: %s", live)
	}
}
