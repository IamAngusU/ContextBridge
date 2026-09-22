package terminalui

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
)

func TestSessionLabelsStayEnglishAcrossHostLocales(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANG", "de_DE.UTF-8")

	session := &Session{
		observing:      true,
		observedOnline: true,
		observed:       ServiceSnapshot{Version: "v0.test", ActiveEndpoints: 2},
	}
	joined := strings.Join([]string{
		session.commandHelpLocked(),
		session.panelStatusLocked(),
		requestedSelection(cluster.WorkerEvent{Provider: "adapter", Model: "model-one", Reasoning: "high"}),
		adapterStateLabel("working"),
		adapterStateLabel("rate_limited"),
	}, "\n")
	for _, want := range []string{"Show this help", "service v0.test", "queue 0", "model requested:", "reasoning requested:", "running", "cooling down"} {
		if !strings.Contains(joined, want) {
			t.Errorf("session status is missing English label %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"Modell", "Denkstufe", "Dienst", "Warteschlange", "läuft", "kühlt ab"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("session status leaked German label %q:\n%s", unwanted, joined)
		}
	}
}

func TestMillisecondsDurationSaturatesInsteadOfWrapping(t *testing.T) {
	if got := millisecondsDuration(math.MaxUint64); got != time.Duration(math.MaxInt64) {
		t.Fatalf("duration = %v, want saturation", got)
	}
	if got := millisecondsDuration(1250); got != 1250*time.Millisecond {
		t.Fatalf("duration = %v, want 1.25s", got)
	}
}

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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}}
	session.Banner("v0.test", "2 components · local bridge · worker")
	capabilities := cluster.Capabilities{AdapterSessions: []cluster.AdapterSessionCapability{{EndpointID: 42, Profile: "profile-one", CurrentModel: "Adapter Model A"}}}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerConnected, NodeName: "test-pc", Slots: 2, Capabilities: capabilities})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobStarted, JobID: "job-42", Task: "generation", Provider: "adapter", Profile: "profile-one", Model: "Adapter Model A"})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobCompleted, JobID: "job-42", ComputeMS: 1500, ReportedProvider: "adapter", ReportedModel: "Adapter Model A"})
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	got := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	for _, label := range []string{
		"by IamAngusU", "https://github.com/IamAngusU/ContextBridge",
		"+-- CONNECTION", "+-- ADAPTER ENDPOINTS", "+-- [profile-one]", "+-- HISTORY", "+-> requested · adapter · profile-one",
		"+-> route · adapter", "+-> model · Adapter Model A",
	} {
		if !strings.Contains(got, label) {
			t.Fatalf("panel output is missing %q: %q", label, got)
		}
	}
	if strings.Count(got, "+-- [profile-one]") == 0 || !strings.Contains(got, "[JOBS]") {
		t.Fatalf("provider group or history is missing: %q", got)
	}
	for _, section := range []string{"CONNECTION", "ADAPTER ENDPOINTS", "STATUS", "HISTORY"} {
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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}}
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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}}
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
	if !strings.Contains(label, "best GPU 5.0 GiB VRAM free") || strings.Contains(label, "8.0 GiB") {
		t.Fatalf("multi-GPU summary implies cross-device VRAM pooling: %q", label)
	}
}

func TestAttachedConsoleOnlyLogsObservedChanges(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 140 },
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}}
	snapshot := ServiceSnapshot{Version: "v0.test", AdapterConnected: true, ActiveEndpoints: 1,
		Endpoints:      []cluster.AdapterSessionCapability{{EndpointID: 7, Profile: "profile-one", CurrentModel: "Adapter Model A"}},
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
	if !strings.Contains(got, "queue 0 · adapters 0/1 busy") {
		t.Fatalf("attached console live status is missing: %q", got)
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(got, "")
	for _, indicator := range []string{"◆ADP", "▣LOC", "TXT", "VIS", "IMG", "AUD", "MUS", "VID", "FIL", "EMB"} {
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
		AdapterSessions: []cluster.AdapterSessionCapability{{EndpointID: 1, Profile: "profile-one"}, {EndpointID: 2, Profile: "profile-two"}},
		Tasks:           []string{"generation", "vision"}}
	session := &Session{out: &output, interactive: true, status: "Idle", statusSince: time.Now().Add(-49 * time.Hour),
		node: "Test-Workstation", nodeID: "node_00000000000000000000000000abcdef", slots: 4, capabilities: cap,
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
	cap := cluster.Capabilities{Sources: []string{"profile-two", "profile-one"}, Modes: []string{"vision", "text", "music"}}
	label := indicatorLabel(cap)
	for _, mode := range []string{"TXT", "VIS", "IMG", "AUD", "MUS", "VID", "FIL", "EMB"} {
		if !strings.Contains(label, mode) {
			t.Fatalf("missing mode %s: %q", mode, label)
		}
	}
	if strings.Index(label, "◆ADP") > strings.Index(label, "◆ADP") || strings.Index(label, "TXT") > strings.Index(label, "VIS") || !strings.Contains(label, ansiDim+"IMG") {
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
		session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerRetrying, Attempt: attempt, RetryIn: time.Second, Error: `dial tcp: lookup relay.example.test: no such host`})
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
	if strings.Contains(output, "lookup relay.example.test") || !strings.Contains(output, "recovered after 10 retries") {
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
	capabilities := cluster.Capabilities{AdapterSessions: []cluster.AdapterSessionCapability{{EndpointID: 42, Profile: "profile-two", CurrentModel: "remote-model-selected"}}}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerConnected, NodeName: "test-pc", Slots: 2, Capabilities: capabilities})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobStarted, JobID: "job-123456789", Task: "generation", Provider: "adapter", Profile: "profile-two", Model: "remote-model-pro", Reasoning: "high"})
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobCompleted, JobID: "job-123456789", ComputeMS: 1000, ReportedProvider: "adapter", ReportedModel: "remote-model-selected"})
	session.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw)
	if strings.Count(output, "Endpoint 42 · profile-two · model: remote-model-selected") != 1 {
		t.Fatalf("endpoint model update should be deduplicated: %s", output)
	}
	if !strings.Contains(output, "model requested: remote-model-pro · reasoning requested: high") {
		t.Fatalf("requested selection missing: %s", output)
	}
	if !strings.Contains(output, "Endpoint reports: remote-model-selected") {
		t.Fatalf("reported selection missing: %s", output)
	}
}

func TestCompletedAdapterJobKeepsMetadataGapBelowSuccess(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, jobs: map[string]jobState{}}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerJobCompleted, JobID: "job-123456789", ComputeMS: 1500, ReportedProvider: "adapter"})
	got := output.String()
	if !strings.Contains(got, "✓  Job job-123456789 completed · 1.5s\n     └─ model metadata unavailable · adapter selection unverified\n") {
		t.Fatalf("success and observational metadata must be distinct: %q", got)
	}
	if strings.Contains(got, "needs attention") || strings.Contains(got, "adapter_model_unavailable") {
		t.Fatalf("missing model metadata must not become a job failure: %q", got)
	}
}

func TestWorkerConsoleDoesNotRepeatEndpointWhenReasoningTemporarilyDisappears(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	session := New(writer)
	capabilities := func(model, reasoning string) cluster.Capabilities {
		return cluster.Capabilities{AdapterSessions: []cluster.AdapterSessionCapability{{
			EndpointID: 4242, Profile: "profile-one", CurrentModel: model, CurrentReasoning: reasoning,
		}}}
	}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerConnected, NodeName: "test-pc", Slots: 1, Capabilities: capabilities("", "Sehr hoch")})
	for range 4 {
		session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities("", "")})
		session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities("", "Sehr hoch")})
	}
	session.HandleWorker(cluster.WorkerEvent{Kind: cluster.WorkerCapabilities, Capabilities: capabilities("Adapter Model A", "")})
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
	if strings.Count(output, "Endpoint 4242") != 3 || strings.Count(output, "Endpoint 4242 updated") != 2 {
		t.Fatalf("temporary empty readings should not create endpoint events: %s", output)
	}
	if !strings.Contains(output, "model: Adapter Model A · reasoning: Sofort") {
		t.Fatalf("real model and reasoning changes were not reported: %s", output)
	}
}

func TestAdapterEndpointsGroupProvidersAndPutWorkingBeforeIdle(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, adapterSelections: map[int]adapterSelection{}}
	endpoints := []cluster.AdapterSessionCapability{
		{EndpointID: 80, Profile: "profile-two", State: "waiting", CurrentModel: "Flash"},
		{EndpointID: 3, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A"},
		{EndpointID: 6, Profile: "custom", State: "working"},
		{EndpointID: 5, Profile: "profile-two", State: "working", CurrentModel: "Pro"},
		{EndpointID: 4, Profile: "profile-one", State: "working", CurrentModel: "Adapter Model A"},
	}
	session.recordAdapterSelectionsLocked(endpoints)
	got := output.String()
	previous := -1
	for _, label := range []string{"Endpoint 6 · custom", "Endpoint 4 · profile-one", "Endpoint 3 · profile-one", "Endpoint 5 · profile-two", "Endpoint 80 · profile-two"} {
		index := strings.Index(got, label)
		if index <= previous {
			t.Fatalf("provider/state ordering is wrong at %q: %s", label, got)
		}
		previous = index
	}
	if !strings.Contains(got, "Endpoint 4 · profile-one · model: Adapter Model A · running") ||
		!strings.Contains(got, "Endpoint 3 · profile-one · model: Adapter Model A · idle") {
		t.Fatalf("endpoint activity is not distinguished: %s", got)
	}
	session.recordAdapterSelectionsLocked(endpoints)
	if output.String() != got {
		t.Fatalf("unchanged adapter states were logged again: %s", output.String())
	}
	endpoints[1].State = ""
	session.recordAdapterSelectionsLocked(endpoints)
	if output.String() != got {
		t.Fatalf("a transient missing state created a false endpoint transition: %s", output.String())
	}
	endpoints[1].State = "working"
	session.recordAdapterSelectionsLocked(endpoints)
	if strings.Count(output.String(), "Endpoint 3 updated") != 1 || !strings.Contains(output.String(), "Endpoint 3 updated · profile-one · model: Adapter Model A · running") {
		t.Fatalf("real state transition was not logged once: %s", output.String())
	}
}

func TestLocalProviderRemainsVisibleWithoutLoadedModel(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 90 },
		localModels: map[string]localModelSelection{}}
	models := []cluster.ModelCapability{
		{Provider: "ollama", Name: "ready-model"},
		{Provider: "adapter", Name: "Adapter Model A", Loaded: true},
		{Provider: "ollama", Name: "loaded-model"},
	}
	session.Banner("v0.test", "worker")
	session.recordLocalModelsLocked(models, []string{"ollama"})
	if !strings.Contains(output.String(), "available · no model loaded") {
		t.Fatalf("online provider without loaded models should be visible: %s", output.String())
	}
	models[2].Loaded = true
	session.recordLocalModelsLocked(models, []string{"ollama"})
	got := output.String()
	frames := strings.Split(got, "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	if !strings.Contains(plain, "+-- [Local · ollama]") || !strings.Contains(plain, "loaded-model · loaded") ||
		strings.Index(plain, "loaded-model · loaded") > strings.Index(plain, "ready-model · ready · not loaded") {
		t.Fatalf("loaded local model not shown: %s", got)
	}
	previous := len(session.history)
	session.recordLocalModelsLocked(models, []string{"ollama"})
	if len(session.history) != previous {
		t.Fatalf("unchanged local model state was repeated: %s", output.String())
	}
	models[2].Loaded = false
	session.recordLocalModelsLocked(models, []string{"ollama"})
	if !strings.Contains(output.String(), "ollama available · no model loaded") {
		t.Fatalf("unload transition should be reported once: %s", output.String())
	}
}

func TestPanelLiveGroupsResizeAndHistoryStaySeparate(t *testing.T) {
	var output bytes.Buffer
	width := 150
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return width }, heightFn: func() int { return 30 },
		adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
	session.Banner("v0.test", "worker")
	session.recordAdapterSelectionsLocked([]cluster.AdapterSessionCapability{
		{EndpointID: 11, Profile: "profile-two", State: "waiting", CurrentModel: "Flash"},
		{EndpointID: 12, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A"},
		{EndpointID: 13, Profile: "profile-one", State: "working", CurrentModel: "Adapter Model A"},
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
	for _, label := range []string{"+-- [profile-one]", "Endpoint 13", "Endpoint 12", "+-- [profile-two]", "Endpoint 11", "+-- [Local · ollama]", "available · no model loaded"} {
		if !strings.Contains(live, label) {
			t.Fatalf("live area missing %q: %s", label, latest)
		}
	}
	if strings.Index(live, "Endpoint 13") > strings.Index(live, "Endpoint 12") || strings.Index(live, "+-- [profile-one]") > strings.Index(live, "+-- [profile-two]") {
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
		adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
	session.Banner("v0.test", "console")
	session.ObserveService(ServiceSnapshot{Version: "v0.test", AdapterConnected: true, ActiveEndpoints: 1,
		Endpoints: []cluster.AdapterSessionCapability{{EndpointID: 77, Profile: "profile-one", CurrentModel: "Adapter Model A"}}, LocalProviders: []string{"ollama"}})
	session.ObserveServiceUnavailable("service offline")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	live := strings.Split(frames[len(frames)-1], "+-- HISTORY")[0]
	if strings.Contains(live, "Endpoint 77") || strings.Contains(live, "[Local · ollama]") || !strings.Contains(live, "service offline") {
		t.Fatalf("offline live panel retained stale selections: %s", live)
	}
}

func TestPanelShowsDetectedPortableResourceWithoutClaimingItIsRunning(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 140 }, heightFn: func() int { return 42 },
		adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
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
	if !strings.Contains(plain, "[API · deepseek]") || !strings.Contains(plain, "v4.1 · available via API") || !strings.Contains(plain, "HOT-PLUG RESOURCES") || !strings.Contains(plain, "ModelKit · example.modelkit · model-runtime · 1 endpoint(s)") || strings.Contains(plain, "ModelKit · online") {
		t.Fatalf("portable resource panel is missing or overclaims runtime state: %s", plain)
	}
}

func TestPanelShowsRelayPoolColorsAndMovingJobCue(t *testing.T) {
	var output bytes.Buffer
	capabilities := cluster.Capabilities{GPUs: []cluster.GPUCapability{{Name: "RTX", Utilization: 94}}}
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 140 }, heightFn: func() int { return 44 },
		status: "Idle", statusSince: time.Now(), nodeID: "node_00000000000000000000000000abcdef", capabilities: capabilities,
		hardware: capabilityLabel(capabilities), adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}, jobs: map[string]jobState{}}
	session.Banner("v0.test", "worker")
	session.SetRelayTarget("https://relay.example.test:8443/secret-path")
	session.ObservePool([]PoolNode{{ID: session.nodeID, Name: "this-pc", Connected: true, Slots: 4}, {ID: "node_other", Name: "other-pc", Connected: false, Slots: 2}}, nil)
	latest := strings.Split(output.String(), "\x1b[H\x1b[2J")
	frame := latest[len(latest)-1]
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frame, "")
	if !strings.Contains(plain, "Relay relay.example.test:8443 · 1/2 workers online") || !strings.Contains(plain, "this-pc#abcdef · idle · 0/4 jobs · this PC") || strings.Contains(plain, "secret-path") {
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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}}
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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{},
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
	if !strings.Contains(plain, "System GPU 1 · RTX A · 91%") || !strings.Contains(plain, "Worker model · vision-model · loaded · ollama · vision") {
		t.Fatalf("per-node GPU/model toggles did not reveal safe details: %s", plain)
	}
	if !strings.Contains(latest, ansiRed+"91%"+ansiReset) || !strings.Contains(plain, "+-- COMMAND") {
		t.Fatalf("detail color or command box missing: %s", latest)
	}
	if !strings.Contains(plain, "exit = close only this view") || !strings.Contains(plain, backgroundServiceStopCommand()) {
		t.Fatalf("command footer does not distinguish view exit from the exact managed-service stop command: %s", plain)
	}
	session.nextSection = "TEST"
	session.writeEventLocked("◇", "clear-me")
	session.HandleCommand("clear")
	if len(session.history) != 0 || session.historyTotal != 0 || !strings.Contains(output.String(), "service and jobs continue running") {
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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}}
	session.EnableCommands()
	session.Banner("v0.test", "console")
	session.HandleCommand("definitely-unknown")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	if !strings.Contains(plain, "Unknown command: definitely-unknown") {
		t.Fatalf("compact panel hid command feedback: %s", plain)
	}
}

func TestHelpCommandRendersReadableRowsInsteadOfOneClippedLine(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 80 }, heightFn: func() int { return 40 },
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}}
	session.EnableCommands()
	session.Banner("v0.test", "console")
	session.HandleCommand("help")
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	for _, want := range []string{
		"help | ?         Show this help",
		"clear | cls      Clear only the visible session history",
		"details all|N    Toggle GPU and model details for a node",
		"gpus all|N       Toggle GPU details for a node",
		"models all|N     Toggle model details for a node",
		"exit | quit | q   Close only this view; service continues running",
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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}}
	session.EnableServiceCommands()
	session.Banner("v0.test", "foreground worker")
	if !session.LiveCommandEditor() {
		t.Fatal("foreground panel did not expose its command editor")
	}
	if session.HandleCommand("exit") {
		t.Fatal("exit in a foreground service requested process shutdown")
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "Foreground service remains active") || !strings.Contains(plain, "Ctrl+C stops it") || !strings.Contains(plain, "contextbridge console") ||
		!strings.Contains(plain, "Ctrl+C = stop this foreground service") {
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
	if !strings.Contains(label, "2 GPUs") || !strings.Contains(label, "max 91%") || !strings.Contains(label, "best GPU 6.0 GiB VRAM free") || strings.Contains(label, "10.0 GiB") {
		t.Fatalf("multi-GPU capability summary is incomplete: %q", label)
	}
}

func TestNodeDetailCommandsRejectNodesOutsideVisiblePanel(t *testing.T) {
	nodes := make([]PoolNode, 9)
	for index := range nodes {
		nodes[index] = PoolNode{ID: fmt.Sprintf("node-%02d", index+1), Name: fmt.Sprintf("node-%02d", index+1), Connected: true, Slots: 1}
	}
	session := &Session{poolNodes: nodes, nodeDetails: map[string]nodeDetailVisibility{}}
	if got := session.toggleNodeDetailsLocked("gpus", "9"); !strings.Contains(got, "only in the dashboard") {
		t.Fatalf("hidden node index did not explain its visibility limit: %q", got)
	}
	if len(session.nodeDetails) != 0 {
		t.Fatalf("hidden node command changed invisible state: %#v", session.nodeDetails)
	}
	if got := session.toggleNodeDetailsLocked("models", "all"); !strings.Contains(got, "1 additional node(s)") {
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
		jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{},
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
		"System GPU 1 · GPU A · 2% · 3.0 GiB/8.0 GiB free",
		"System GPU 2 · GPU B · 92% · 5.0 GiB/12.0 GiB free",
		"Worker model · loaded-model · loaded · ollama · generation",
		"Worker model · cold-model · ready · not loaded · ollama · vision",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rack details are missing %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, "System GPU · Zero-GPU") {
		t.Fatalf("details from the untoggled zero-GPU node leaked into the rack view: %s", plain)
	}
	if !strings.Contains(latest, ansiGreen+"loaded"+ansiReset) || !strings.Contains(latest, ansiDim+"ready · not loaded"+ansiReset) || !strings.Contains(latest, ansiRed+"92%"+ansiReset) {
		t.Fatalf("GPU/model state colors are not authoritative: %q", latest)
	}

	session.HandleCommand("gpus 1")
	frames = strings.Split(output.String(), "\x1b[H\x1b[2J")
	plain = regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(frames[len(frames)-1], "")
	if !strings.Contains(plain, "System GPU · Zero-GPU") {
		t.Fatalf("zero-GPU node did not expose its per-device state: %s", plain)
	}
}

func TestLocalModelRowsColorLoadedAndUnloadedModels(t *testing.T) {
	var output bytes.Buffer
	session := &Session{out: &output, interactive: true, style: "panel", widthFn: func() int { return 130 }, heightFn: func() int { return 45 },
		status: "Idle", statusSince: time.Now(), jobs: map[string]jobState{}, adapterSelections: map[int]adapterSelection{}, localModels: map[string]localModelSelection{}}
	session.Banner("v0.test", "worker")
	session.recordLocalModelsLocked([]cluster.ModelCapability{
		{Name: "warm", Provider: "ollama", Loaded: true, Tasks: []string{"generation"}},
		{Name: "cold", Provider: "ollama", Loaded: false, Tasks: []string{"vision"}},
	}, []string{"ollama"})
	frames := strings.Split(output.String(), "\x1b[H\x1b[2J")
	latest := frames[len(frames)-1]
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(latest, "")
	if !strings.Contains(plain, "warm · loaded") || !strings.Contains(plain, "cold · ready · not loaded") || strings.Index(plain, "warm · loaded") > strings.Index(plain, "cold · ready") {
		t.Fatalf("local model rows are missing or not loaded-first: %s", plain)
	}
	if !strings.Contains(latest, ansiGreen+"loaded"+ansiReset) || !strings.Contains(latest, ansiDim+"ready · not loaded"+ansiReset) {
		t.Fatalf("loaded and unloaded local models are not color-distinguished: %q", latest)
	}
}
