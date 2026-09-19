package terminalui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
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
	for _, section := range []string{"CONNECTION", "AI TABS", "STATUS", "HISTORY"} {
		if !strings.Contains(got, "\n  |\n  +-- "+section) {
			t.Fatalf("panel section %s needs a readable vertical gap: %q", section, got)
		}
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

func TestNarrowPanelStartsConsistentlyWithBannerMetadata(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 40 }, heightFn: func() int { return 20 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}}
	session.Banner("v0.narrow", "worker")
	if !session.panelStarted || session.bannerVersion != "v0.narrow" || session.bannerComponents != "worker" {
		t.Fatalf("narrow panel mixed classic and panel startup: started=%t version=%q components=%q output=%q", session.panelStarted, session.bannerVersion, session.bannerComponents, output.String())
	}
	if strings.Contains(output.String(), "\n  ContextBridge  v0.narrow\n") {
		t.Fatalf("narrow panel emitted a classic banner before the panel: %q", output.String())
	}
}

func TestVeryShortPanelNeverExceedsViewportAndKeepsCommand(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 80 }, heightFn: func() int { return 6 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}}
	session.EnableCommands()
	session.Banner("v0.short", "console")
	session.SetCommandInput("models 1")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	latest := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	rows := strings.Split(strings.TrimSuffix(latest, "\n"), "\n")
	if len(rows) > 8 {
		t.Fatalf("short panel rendered %d rows into an eight-row viewport: %q", len(rows), latest)
	}
	if !strings.Contains(latest, "+-- STATUS") || !strings.Contains(latest, "+-- COMMAND") || !strings.Contains(latest, "cb › models 1") {
		t.Fatalf("short panel lost authoritative status or command input: %q", latest)
	}
}

func TestMultiGPUSummaryUsesBestSingleDeviceInsteadOfSummingVRAM(t *testing.T) {
	label := capabilityLabel(cluster.Capabilities{GPUs: []cluster.GPUCapability{
		{Name: "GPU A", MemoryFree: 3 << 30},
		{Name: "GPU B", MemoryFree: 5 << 30},
	}})
	if !strings.Contains(label, "beste GPU 5.0 GiB VRAM frei") || strings.Contains(label, "8.0 GiB") {
		t.Fatalf("multi-GPU summary implies cross-device VRAM pooling: %q", label)
	}
}

func TestAttachedConsoleOnlyLogsObservedChanges(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 140 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}}
	snapshot := ServiceSnapshot{Version: "v0.test", BrowserConnected: true, ActiveTabs: 1,
		Tabs:           []cluster.BrowserSessionCapability{{TabID: 7, Profile: "chatgpt", CurrentModel: "GPT-5.6 Sol"}},
		LocalProviders: []string{"ollama"},
		LocalModels:    []cluster.ModelCapability{{Name: "local-text", Provider: "ollama", Loaded: true, Tasks: []string{"generation"}}}}
	session.ObserveService(snapshot)
	session.ObserveService(snapshot)
	snapshot.Completed, snapshot.JobsTotal = 1, 1
	session.ObserveService(snapshot)
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	got := frames[len(frames)-1]
	if len(session.history) != 5 || strings.Count(got, "Attached to running service") < 1 ||
		!strings.Contains(got, "Completed total 1 (+1 since last check)") || !strings.Contains(got, "[ACTIVITY]") {
		t.Fatalf("attached console emitted duplicate or missing events: %q", got)
	}
	if !strings.Contains(got, "Warteschlange 0 · Browser 0/1 belegt") {
		t.Fatalf("attached console live status is missing: %q", got)
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(got, "")
	for _, indicator := range []string{"◉GPT", "▣LOC", "TXT", "VIS", "IMG", "AUD", "MUS", "VID", "FIL", "EMB"} {
		if !strings.Contains(plain, indicator) {
			t.Fatalf("attached console lost stable indicator %q: %q", indicator, plain)
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
	if session.LiveCommandEditor() {
		t.Fatal("narrow classic fallback would disable terminal echo without an input row")
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
	if session.LiveCommandEditor() {
		t.Fatal("redirected output cannot own a live command editor")
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
	frames := strings.Split(got, "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	if !strings.Contains(plain, "+-- [Lokal · ollama]") || !strings.Contains(plain, "loaded-model · geladen") ||
		strings.Index(plain, "loaded-model · geladen") > strings.Index(plain, "ready-model · bereit · nicht geladen") {
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
	if !strings.Contains(latest[historyIndex:], "[LOCAL MODELS]") {
		t.Fatalf("history did not retain the newest transition: %s", latest)
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

func TestPanelShowsDetectedPortableResourceWithoutClaimingItIsRunning(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 140 }, heightFn: func() int { return 42 },
		browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
	session.Banner("v0.test", "console")
	session.ObserveService(ServiceSnapshot{
		Version:      "v0.test",
		APIProviders: []string{"deepseek"},
		APIModels:    []cluster.ModelCapability{{Provider: "deepseek", Name: "v4.1", Available: true}},
		ResourcePacks: []resourcepacks.Pack{{Manifest: resourcepacks.Manifest{
			ID: "example.modelkit", Name: "ModelKit", Kind: "model-runtime",
			Endpoints: []resourcepacks.Endpoint{{ID: "ollama", Type: "ollama"}},
		}}},
	})
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	if !strings.Contains(plain, "[API · deepseek]") || !strings.Contains(plain, "v4.1 · per API verfügbar") || !strings.Contains(plain, "HOT-PLUG RESOURCES") || !strings.Contains(plain, "ModelKit · example.modelkit · model-runtime · 1 Endpunkt(e)") || strings.Contains(plain, "ModelKit · online") {
		t.Fatalf("portable resource panel is missing or overclaims runtime state: %s", plain)
	}
}

func TestPanelShowsRelayPoolColorsAndMovingJobCue(t *testing.T) {
	var output bytes.Buffer
	capabilities := cluster.Capabilities{GPUs: []cluster.GPUCapability{{Name: "RTX", Utilization: 94}}}
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 140 }, heightFn: func() int { return 44 },
		status: "Idle", statusSince: time.Now(), nodeID: "node_5a18aecdcb05db6887c355031ad5ca35", capabilities: capabilities,
		hardware: capabilityLabel(capabilities), browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
	session.Banner("v0.test", "worker")
	session.SetRelayTarget("https://relay.example.test:8443/secret-path")
	session.ObservePool([]PoolNode{{ID: session.nodeID, Name: "this-pc", Connected: true, Slots: 4}, {ID: "node_other", Name: "other-pc", Connected: false, Slots: 2}}, nil)
	latest := strings.Split(output.String(), "\x1b[H\x1b[2J")
	frame := latest[len(latest)-1]
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frame, "")
	if !strings.Contains(plain, "Relay relay.example.test:8443 · 1/2 Worker online") || !strings.Contains(plain, "this-pc#d5ca35 · idle · 0/4 Jobs · dieser PC") || strings.Contains(plain, "secret-path") {
		t.Fatalf("relay/node view is missing or leaked URL path: %s", plain)
	}
	if !strings.Contains(frame, ansiGreen+"Idle"+ansiReset) || !strings.Contains(frame, ansiRed+"94%"+ansiReset) || !strings.Contains(frame, ansiRed+"offline"+ansiReset) {
		t.Fatalf("important panel states lost their colors: %q", frame)
	}
	session.jobs["job"] = jobState{phase: "generating", started: time.Now()}
	session.status = "Working"
	session.renderPanelLocked()
	latest = strings.Split(output.String(), "\x1b[H\x1b[2J")
	if !strings.Contains(latest[len(latest)-1], ansiCyan+travelBar(session.frame, 16)+ansiReset) || travelBar(0, 16) == travelBar(4, 16) {
		t.Fatal("working panel has no left-to-right activity cue")
	}
}

func TestPanelHistoryIsNewestFirstAndBoxed(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 100 }, heightFn: func() int { return 35 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}}
	session.Banner("v0.test", "console")
	session.nextSection = "TEST"
	session.writeEventLocked("◇", "older-event")
	session.nextSection = "TEST"
	session.writeEventWithDetailsLocked("✓", "newer-event", []string{"newer-detail"})
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	history := plain[strings.Index(plain, "+-- HISTORY"):]
	if strings.Index(history, "newer-event") < 0 || strings.Index(history, "older-event") < 0 || strings.Index(history, "newer-event") > strings.Index(history, "older-event") {
		t.Fatalf("history is not newest first: %s", history)
	}
	if strings.Index(history, "newer-detail") < strings.Index(history, "newer-event") || !strings.Contains(history, "older-event\n  +") {
		t.Fatalf("history hierarchy or closing ASCII border is missing: %s", history)
	}
}

func TestPanelCommandsPreserveInputAndToggleNodeDetails(t *testing.T) {
	var output bytes.Buffer
	selected := false
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 130 }, heightFn: func() int { return 48 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{},
		nodeDetails: map[string]nodeDetailVisibility{}, selectionActiveFn: func() bool { return selected }}
	session.EnableCommands()
	session.Banner("v0.test", "console")
	session.SetRelayTarget("https://relay.example.test")
	session.ObservePool([]PoolNode{{ID: "node_abc123", Name: "rack", Connected: true, Slots: 4,
		GPUs:   []cluster.GPUCapability{{Name: "RTX A", Utilization: 91, MemoryFree: 2 << 30, MemoryTotal: 8 << 30}},
		Models: []cluster.ModelCapability{{Name: "vision-model", Provider: "ollama", Loaded: true, Vision: true}}}}, nil)
	session.SetCommandInput("hel")
	if !strings.Contains(output.String(), "cb › hel▌") {
		t.Fatalf("typed command is not rendered persistently: %s", output.String())
	}
	beforeSelection := output.Len()
	selected = true
	session.SetCommandInput("help")
	if output.Len() != beforeSelection {
		t.Fatal("panel redrew while terminal text was selected")
	}
	selected = false
	session.SetCommandInput("help")
	if session.HandleCommand("gpus 1") || session.HandleCommand("models 1") {
		t.Fatal("detail commands unexpectedly requested exit")
	}
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	latest := frames[len(frames)-1]
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(latest, "")
	if !strings.Contains(plain, "System-GPU 1 · RTX A · 91%") || !strings.Contains(plain, "Worker-Modell · vision-model · geladen · ollama · vision") {
		t.Fatalf("per-node GPU/model toggles did not reveal safe details: %s", plain)
	}
	if !strings.Contains(latest, ansiRed+"91%"+ansiReset) || !strings.Contains(plain, "+-- COMMAND") {
		t.Fatalf("detail color or command box missing: %s", latest)
	}
	if !strings.Contains(plain, "exit = nur diese Ansicht schließen") || !strings.Contains(plain, backgroundServiceStopCommand()) {
		t.Fatalf("command footer does not distinguish view exit from the exact managed-service stop command: %s", plain)
	}
	session.nextSection = "TEST"
	session.writeEventLocked("◇", "clear-me")
	session.HandleCommand("clear")
	if len(session.history) != 0 || session.historyTotal != 0 || !strings.Contains(output.String(), "Dienst und Jobs laufen weiter") {
		t.Fatalf("clear did not limit itself to console history: %#v", session.history)
	}
	if !session.HandleCommand("exit") {
		t.Fatal("exit must close only the console caller")
	}
}

func TestBackgroundServiceStopCommandIsCrossPlatformControlClient(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin", "plan9"} {
		if got := backgroundServiceStopCommandForOS(goos); got != "contextbridge stop" {
			t.Errorf("stop command for %s = %q, want contextbridge stop", goos, got)
		}
	}
}

func TestPanelCommandFooterKeepsExactStopCommandAtOrdinaryWidths(t *testing.T) {
	for _, item := range []struct {
		goos  string
		width int
	}{
		{"windows", 80},
		{"linux", 80},
		{"darwin", 80},
	} {
		command := backgroundServiceStopCommandForOS(item.goos)
		row := "  |   " + command
		if got := clipANSIColumns(row, item.width-2); !strings.Contains(got, command) {
			t.Errorf("%s stop command is clipped at %d columns: %q", item.goos, item.width, got)
		}
	}
}

func TestCompactPanelPreservesLatestCommandFeedback(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 80 }, heightFn: func() int { return 8 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}}
	session.EnableCommands()
	session.Banner("v0.test", "console")
	session.HandleCommand("definitely-unknown")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	if !strings.Contains(plain, "Unbekannter Befehl: definitely-unknown") {
		t.Fatalf("compact panel hid command feedback: %s", plain)
	}
}

func TestHelpCommandRendersReadableRowsInsteadOfOneClippedLine(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 80 }, heightFn: func() int { return 40 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}}
	session.EnableCommands()
	session.Banner("v0.test", "console")
	session.HandleCommand("help")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	for _, want := range []string{
		"help | ?         Diese Hilfe anzeigen",
		"clear | cls      Nur den sichtbaren Sitzungsverlauf leeren",
		"details all|N    GPU- und Modelldetails einer Node umschalten",
		"gpus all|N       GPU-Details einer Node umschalten",
		"models all|N     Modelldetails einer Node umschalten",
		"exit | quit | q   Nur diese Ansicht schließen; Dienst läuft weiter",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("multi-line help is missing %q:\n%s", want, plain)
		}
	}
	if got := strings.Count(plain, "  | help | ?"); got != 1 {
		t.Fatalf("help output was duplicated or flattened: count=%d\n%s", got, plain)
	}
}

func TestForegroundServiceCommandsCannotAccidentallyCloseTheirOwner(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 120 }, heightFn: func() int { return 30 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}}
	session.EnableServiceCommands()
	session.Banner("v0.test", "foreground worker")
	if !session.LiveCommandEditor() {
		t.Fatal("foreground panel did not expose its command editor")
	}
	if session.HandleCommand("exit") {
		t.Fatal("exit in a foreground service requested process shutdown")
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "Vordergrunddienst bleibt aktiv") || !strings.Contains(plain, "Ctrl+C stoppt ihn") || !strings.Contains(plain, "contextbridge console") ||
		!strings.Contains(plain, "Ctrl+C = diesen Vordergrunddienst stoppen") {
		t.Fatalf("foreground exit did not explain the safe lifecycle: %s", plain)
	}
	if strings.Contains(plain, backgroundServiceStopCommand()) {
		t.Fatalf("standalone worker falsely advertised a local bridge stop endpoint: %s", plain)
	}
	session.EnableServiceStopCommand()
	plain = regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, backgroundServiceStopCommand()) {
		t.Fatalf("run-style service did not advertise its authenticated stop command: %s", plain)
	}
	session.EnableCommands()
	if !session.HandleCommand("exit") {
		t.Fatal("an attached console must still be able to close its own view")
	}
}

func TestMultiGPUCompactStatusUsesEveryDevice(t *testing.T) {
	capabilities := cluster.Capabilities{GPUs: []cluster.GPUCapability{
		{Name: "GPU A", Utilization: 0, MemoryFree: 4 << 30},
		{Name: "GPU B", Utilization: 91, MemoryFree: 6 << 30},
	}}
	plainState := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(compactGPUState(capabilities), "")
	if plainState != "2GPU+" {
		t.Fatalf("compact multi-GPU state ignored an active device: %q", plainState)
	}
	label := capabilityLabel(capabilities)
	if !strings.Contains(label, "2 GPUs") || !strings.Contains(label, "max 91%") || !strings.Contains(label, "beste GPU 6.0 GiB VRAM frei") || strings.Contains(label, "10.0 GiB") {
		t.Fatalf("multi-GPU capability summary is incomplete: %q", label)
	}
}

func TestNodeDetailCommandsRejectNodesOutsideVisiblePanel(t *testing.T) {
	nodes := make([]PoolNode, 9)
	for index := range nodes {
		nodes[index] = PoolNode{ID: fmt.Sprintf("node-%02d", index+1), Name: fmt.Sprintf("node-%02d", index+1), Connected: true, Slots: 1}
	}
	session := &Session{poolNodes: nodes, nodeDetails: map[string]nodeDetailVisibility{}}
	if got := session.toggleNodeDetailsLocked("gpus", "9"); !strings.Contains(got, "nur im Dashboard") {
		t.Fatalf("hidden node index did not explain its visibility limit: %q", got)
	}
	if len(session.nodeDetails) != 0 {
		t.Fatalf("hidden node command changed invisible state: %#v", session.nodeDetails)
	}
	if got := session.toggleNodeDetailsLocked("models", "all"); !strings.Contains(got, "1 weitere Node(s)") {
		t.Fatalf("all command did not disclose hidden nodes: %q", got)
	}
	if len(session.nodeDetails) != maximumVisiblePoolNodes {
		t.Fatalf("all command toggled %d nodes, want %d visible nodes", len(session.nodeDetails), maximumVisiblePoolNodes)
	}
	if _, ok := session.nodeDetails["node-09"]; ok {
		t.Fatal("all command silently toggled a node that cannot be rendered")
	}
}

func TestPanelPerNodeDetailsDistinguishZeroGPUMultiGPUAndModelLoadState(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 170 }, heightFn: func() int { return 80 },
		jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{},
		nodeDetails: map[string]nodeDetailVisibility{}}
	session.EnableCommands()
	session.Banner("v0.test", "console")
	session.SetRelayTarget("https://relay.example.test")
	session.ObservePool([]PoolNode{
		{ID: "node-rack", Name: "rack", Connected: true, Slots: 4,
			GPUs: []cluster.GPUCapability{
				{Name: "GPU A", Utilization: 2, MemoryFree: 3 << 30, MemoryTotal: 8 << 30},
				{Name: "GPU B", Utilization: 92, MemoryFree: 5 << 30, MemoryTotal: 12 << 30},
			},
			Models: []cluster.ModelCapability{
				{Name: "loaded-model", Provider: "ollama", Loaded: true, Tasks: []string{"generation"}},
				{Name: "cold-model", Provider: "ollama", Loaded: false, Vision: true, Tasks: []string{"vision"}},
			}},
		{ID: "node-cpu", Name: "cpu-only", Connected: true, Slots: 2},
	}, nil)

	session.HandleCommand("details 2")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	latest := frames[len(frames)-1]
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(latest, "")
	for _, want := range []string{
		"System-GPU 1 · GPU A · 2% · 3.0 GiB/8.0 GiB frei",
		"System-GPU 2 · GPU B · 92% · 5.0 GiB/12.0 GiB frei",
		"Worker-Modell · loaded-model · geladen · ollama · generation",
		"Worker-Modell · cold-model · bereit · nicht geladen · ollama · vision",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rack details are missing %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, "System-GPU · Zero-GPU") {
		t.Fatalf("details from the untoggled zero-GPU node leaked into the rack view: %s", plain)
	}
	if !strings.Contains(latest, ansiGreen+"geladen"+ansiReset) || !strings.Contains(latest, ansiDim+"bereit · nicht geladen"+ansiReset) || !strings.Contains(latest, ansiRed+"92%"+ansiReset) {
		t.Fatalf("GPU/model state colors are not authoritative: %q", latest)
	}

	session.HandleCommand("gpus 1")
	frames = strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain = regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	if !strings.Contains(plain, "System-GPU · Zero-GPU") {
		t.Fatalf("zero-GPU node did not expose its per-device state: %s", plain)
	}
}

func TestLocalModelRowsColorLoadedAndUnloadedModels(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 130 }, heightFn: func() int { return 45 },
		status: "Idle", statusSince: time.Now(), jobs: map[string]jobState{}, browserSelections: map[int]browserSelection{}, localModels: map[string]localModelSelection{}}
	session.Banner("v0.test", "worker")
	session.recordLocalModelsLocked([]cluster.ModelCapability{
		{Name: "warm", Provider: "ollama", Loaded: true, Tasks: []string{"generation"}},
		{Name: "cold", Provider: "ollama", Loaded: false, Tasks: []string{"vision"}},
	}, []string{"ollama"})
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	latest := frames[len(frames)-1]
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(latest, "")
	if !strings.Contains(plain, "warm · geladen") || !strings.Contains(plain, "cold · bereit · nicht geladen") || strings.Index(plain, "warm · geladen") > strings.Index(plain, "cold · bereit") {
		t.Fatalf("local model rows are missing or not loaded-first: %s", plain)
	}
	if !strings.Contains(latest, ansiGreen+"geladen"+ansiReset) || !strings.Contains(latest, ansiDim+"bereit · nicht geladen"+ansiReset) {
		t.Fatalf("loaded and unloaded local models are not color-distinguished: %q", latest)
	}
}
