package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkContextBridgeRelayQueueRoundTrip measures ContextBridge itself:
// authenticated loopback HTTP, strict JSON decoding, durable Bolt admission,
// compact readback, and cancellation. It deliberately has no worker or model,
// so provider latency cannot enter the number.
func BenchmarkContextBridgeRelayQueueRoundTrip(b *testing.B) {
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(b.TempDir(), "relay.db"),
		AdminToken:    "admin_012345678901234567890123456789012345",
		AllowedTasks:  []string{"generation"},
		MaxQueuedJobs: 10000,
	}, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = relay.Close() })
	server := httptest.NewServer(relay.Handler())
	b.Cleanup(server.Close)
	producer, _, err := relay.store.CreateToken("producer", "benchmark-producer", nil, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	client := server.Client()
	payload := json.RawMessage(`{"prompt":"Reply exactly with CB-BENCH-OK.","output":{"mode":"text","max_bytes":4096}}`)
	request := SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: payload, MaxAttempts: 1}
	body, err := json.Marshal(request)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(payload)), "payload_B")
	for index := 0; index < b.N; index++ {
		var job Job
		benchmarkJSONRequest(b, client, http.MethodPost, server.URL+"/v1/cluster/jobs?compact=1", producer, body, http.StatusAccepted, &job)
		if job.ID == "" || job.Status != JobQueued {
			b.Fatalf("invalid admitted job: %#v", job)
		}
		var readback Job
		benchmarkJSONRequest(b, client, http.MethodGet, server.URL+"/v1/cluster/jobs/"+job.ID+"?compact=1", producer, nil, http.StatusOK, &readback)
		if readback.ID != job.ID || readback.Status != JobQueued {
			b.Fatalf("invalid compact readback: %#v", readback)
		}
		var cancelled Job
		benchmarkJSONRequest(b, client, http.MethodDelete, server.URL+"/v1/cluster/jobs/"+job.ID, producer, nil, http.StatusOK, &cancelled)
		if cancelled.Status != JobCancelled {
			b.Fatalf("job was not cancelled: %#v", cancelled)
		}
	}
}

// BenchmarkContextBridgeE2EESmallTextRoundTrip isolates the complete producer
// -> worker -> producer cryptographic path for a small job and result. It does
// not contact a relay, browser, local runtime, or model.
func BenchmarkContextBridgeE2EESmallTextRoundTrip(b *testing.B) {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		b.Fatal(err)
	}
	payload := []byte(`{"prompt":"Reply exactly with CB-BENCH-OK."}`)
	result := []byte(`{"output":{"mode":"text","text":"CB-BENCH-OK"}}`)
	aad := []byte("contextbridge-benchmark-job")
	resultAAD := []byte("contextbridge-benchmark-result")

	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(payload)+len(result)), "cleartext_B")
	for index := 0; index < b.N; index++ {
		envelope, producerShared, sealErr := SealFor(publicKey, payload, aad)
		if sealErr != nil {
			b.Fatal(sealErr)
		}
		opened, workerShared, openErr := OpenWith(privateKey, envelope, aad)
		if openErr != nil || !bytes.Equal(opened, payload) {
			b.Fatalf("worker open failed: %v", openErr)
		}
		sealedResult, sealResultErr := SealResponse(workerShared, result, resultAAD)
		if sealResultErr != nil {
			b.Fatal(sealResultErr)
		}
		openedResult, openResultErr := OpenResponse(producerShared, sealedResult, resultAAD)
		if openResultErr != nil || !bytes.Equal(openedResult, result) {
			b.Fatalf("producer open failed: %v", openResultErr)
		}
	}
}

// BenchmarkContextBridgeIdleDispatchTick measures one already-warmed idle
// scheduler scan. The production loop sleeps for DispatchEvery between scans;
// this benchmark intentionally removes that sleep so ns/op can be converted
// into the scheduler's CPU duty cycle without waiting in real time.
func BenchmarkContextBridgeIdleDispatchTick(b *testing.B) {
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(b.TempDir(), "relay.db"),
		AdminToken:    "admin_012345678901234567890123456789012345",
		AllowedTasks:  []string{"generation"},
		DispatchEvery: 250 * time.Millisecond,
	}, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = relay.Close() })
	// Consume the initial maintenance and retention work outside the timed
	// section. Subsequent idle ticks use the adaptive maintenance cadence.
	relay.dispatch()

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		relay.dispatch()
	}
}

// BenchmarkContextBridgeE2EE8MiBPayload measures only the bounded encryption
// and decryption of an exact-maximum visual payload. Use -benchtime=3x (or a
// similarly fixed count) when publishing a machine-specific result.
func BenchmarkContextBridgeE2EE8MiBPayload(b *testing.B) {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		b.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x5a}, 8<<20)
	aad := []byte("contextbridge-benchmark-8mib")

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		envelope, _, sealErr := SealFor(publicKey, payload, aad)
		if sealErr != nil {
			b.Fatal(sealErr)
		}
		opened, _, openErr := OpenWith(privateKey, envelope, aad)
		if openErr != nil || len(opened) != len(payload) {
			b.Fatalf("maximum payload open failed: %v", openErr)
		}
	}
}

func benchmarkJSONRequest(b *testing.B, client *http.Client, method, url, token string, body []byte, wantStatus int, target interface{}) {
	b.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(body))
	if err != nil {
		b.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		b.Fatalf("%s %s returned %d, want %d: %s", method, url, response.StatusCode, wantStatus, raw)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		b.Fatal(fmt.Errorf("decode %s %s: %w", method, url, err))
	}
}
