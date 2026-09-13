package terminalui

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

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
