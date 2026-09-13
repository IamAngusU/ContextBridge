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
