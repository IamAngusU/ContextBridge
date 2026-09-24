package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateRelayURLRejectsDisguisedRemoteHTTP(t *testing.T) {
	for _, value := range []string{
		"http://localhost:32145@evil.example",
		"http://127.0.0.1:32145@evil.example",
		"http://evil.example:32145",
		"https://user:secret@example.com",
		"https://example.com?token=secret",
	} {
		if err := ValidateRelayURL(value); err == nil {
			t.Fatalf("unsafe relay URL %q was accepted", value)
		}
	}
	for _, value := range []string{
		"http://localhost:32145",
		"http://127.0.0.1:32145",
		"http://[::1]:32145",
		"https://relay.example.com/contextbridge",
	} {
		if err := ValidateRelayURL(value); err != nil {
			t.Fatalf("safe relay URL %q rejected: %v", value, err)
		}
	}
}

func TestValidateLocalWorkerURLKeepsLocalTokenOnLoopback(t *testing.T) {
	for _, value := range []string{
		"http://localhost:32145@evil.example",
		"http://127.0.0.1:32145@evil.example",
		"http://localhost.evil.example:32145",
		"http://192.0.2.10:32145",
		"http://[2001:db8::10]:32145",
		"https://worker.example.com:32145",
		"http://user:secret@localhost:32145",
		"http://localhost:32145?token=secret",
		"http://localhost:32145/#fragment",
		"file:///tmp/contextbridge.sock",
	} {
		if err := ValidateLocalWorkerURL(value); err == nil {
			t.Fatalf("unsafe local worker URL %q was accepted", value)
		}
	}
	for _, value := range []string{
		"http://localhost:32145",
		"http://127.0.0.1:32145",
		"http://127.1.2.3:32145/base",
		"http://[::1]:32145",
		"https://localhost:32145/contextbridge",
	} {
		if err := ValidateLocalWorkerURL(value); err != nil {
			t.Fatalf("safe local worker URL %q rejected: %v", value, err)
		}
	}
}

func TestSealedWorkerFailureExposesOnlyStableRelayMetadata(t *testing.T) {
	const secret = "DECRYPTED-PROMPT-MARKER-9f3d"
	job := Job{SealedPayload: &SealedEnvelope{Algorithm: sealedAlgorithm}}
	errorText, failureCode := relayVisibleWorkerFailure(job, errors.New(FailureAdapterTimeout+": provider echoed "+secret))
	wire, err := json.Marshal(WireMessage{Type: "result", JobID: "job-sealed", Attempt: 1, Error: errorText, FailureCode: failureCode})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte(secret)) || strings.Contains(errorText, secret) {
		t.Fatalf("sealed failure leaked provider detail: %s", wire)
	}
	if failureCode != FailureAdapterTimeout || errorText != sealedJobFailureMessage(FailureAdapterTimeout) {
		t.Fatalf("sealed failure lost bounded classification: error=%q code=%q", errorText, failureCode)
	}

	plainError, plainCode := relayVisibleWorkerFailure(Job{}, errors.New(FailureAdapterTimeout+": actionable local detail"))
	if plainCode != FailureAdapterTimeout || !strings.Contains(plainError, "actionable local detail") {
		t.Fatalf("plaintext diagnostics changed: error=%q code=%q", plainError, plainCode)
	}
}

func TestWorkerExecutionPanicFailsClosedWithoutLeakingPanicValue(t *testing.T) {
	const secret = "PANIC-VALUE-MUST-NOT-CROSS-RELAY"
	result, sealed, usage, execution, err := safelyExecuteWorker(func() (json.RawMessage, *SealedEnvelope, Usage, *ExecutionMetadata, error) {
		panic(secret)
	})
	if !errors.Is(err, errWorkerExecutionPanicked) {
		t.Fatalf("panic returned %v", err)
	}
	if result != nil || sealed != nil || execution != nil || usage != (Usage{}) {
		t.Fatalf("panic retained an apparent result: result=%s sealed=%v usage=%#v execution=%#v", result, sealed, usage, execution)
	}
	plainText, plainCode := relayVisibleWorkerFailure(Job{}, err)
	if plainCode != FailureExecutionStateAmbiguous || strings.Contains(plainText, secret) {
		t.Fatalf("plaintext panic classification was unsafe: error=%q code=%q", plainText, plainCode)
	}
	sealedText, sealedCode := relayVisibleWorkerFailure(Job{SealedPayload: &SealedEnvelope{Algorithm: sealedAlgorithm}}, err)
	if sealedCode != FailureExecutionStateAmbiguous || sealedText != sealedJobFailureMessage(FailureExecutionStateAmbiguous) || strings.Contains(sealedText, secret) {
		t.Fatalf("sealed panic classification was unsafe: error=%q code=%q", sealedText, sealedCode)
	}
}

func TestWorkerExecutionNilPanicStillFailsClosed(t *testing.T) {
	_, _, _, _, err := safelyExecuteWorker(func() (json.RawMessage, *SealedEnvelope, Usage, *ExecutionMetadata, error) {
		panic(nil)
	})
	if !errors.Is(err, errWorkerExecutionPanicked) {
		t.Fatalf("nil panic escaped the work-item boundary: %v", err)
	}
}

func TestWorkerExecutionPanicBoundaryPreservesNormalResult(t *testing.T) {
	wantResult := json.RawMessage(`{"ok":true}`)
	wantSealed := &SealedEnvelope{Algorithm: sealedAlgorithm}
	wantUsage := Usage{ComputeMS: 42}
	wantExecution := &ExecutionMetadata{AdapterEndpointID: 7}
	result, sealed, usage, execution, err := safelyExecuteWorker(func() (json.RawMessage, *SealedEnvelope, Usage, *ExecutionMetadata, error) {
		return wantResult, wantSealed, wantUsage, wantExecution, nil
	})
	if err != nil || !bytes.Equal(result, wantResult) || sealed != wantSealed || usage != wantUsage || execution != wantExecution {
		t.Fatalf("normal result changed: result=%s sealed=%#v usage=%#v execution=%#v err=%v", result, sealed, usage, execution, err)
	}
}

func TestWorkerReporterPanicIsIsolated(t *testing.T) {
	reporter := isolateWorkerReporter(func(WorkerEvent) { panic("terminal-renderer-bug") })
	reporter(WorkerEvent{Kind: WorkerJobStarted, JobID: "job-one"})
	called := false
	isolateWorkerReporter(func(event WorkerEvent) { called = event.JobID == "job-two" })(WorkerEvent{JobID: "job-two"})
	if !called {
		t.Fatal("ordinary worker reporter event was lost")
	}
}

func TestPairWorkerRejectsUnsafePollingIntervals(t *testing.T) {
	for _, interval := range []int{-1, 0, 61, int(^uint(0) >> 1)} {
		t.Run(strings.ReplaceAll(time.Duration(interval).String(), "-", "negative"), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writeJSON(writer, http.StatusOK, PairResponse{
					DeviceCode: "0123456789abcdef",
					UserCode:   "ABCD-EFGH", VerificationURI: "http://" + request.Host + "/pair",
					ExpiresAt: time.Now().Add(time.Hour), IntervalSeconds: interval,
				})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := PairWorker(ctx, server.URL, "test", filepath.Join(t.TempDir(), "identity.json"), nil, nil)
			if err == nil || !strings.Contains(err.Error(), "pairing interval") {
				t.Fatalf("interval %d returned %v", interval, err)
			}
		})
	}
}

func TestLoadWorkerRejectsOversizedAndInconsistentIdentity(t *testing.T) {
	oversized := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, maximumWorkerIdentityBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorker(WorkerConfig{RelayURL: "http://127.0.0.1:32150", IdentityFile: oversized}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized identity returned %v", err)
	}

	privateKey, _, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, wrongPublicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.json")
	identity := `{"node_id":"node-safe","node_token":"cb_node_0123456789012345678901234567890123456789","private_key":"` + privateKey + `","public_key":"` + wrongPublicKey + `","relay_url":"http://127.0.0.1:32150"}`
	if err := os.WriteFile(identityFile, []byte(identity), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorker(WorkerConfig{RelayURL: "http://127.0.0.1:32150", IdentityFile: identityFile}); err == nil {
		t.Fatal("identity with a mismatched public key was accepted")
	}
}

func TestLoadWorkerRejectsUnsafeDirectNumericConfiguration(t *testing.T) {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.json")
	if err := saveIdentity(identityFile, WorkerIdentity{
		NodeID: "node-safe", NodeToken: "cb_node_0123456789012345678901234567890123456789",
		PrivateKey: privateKey, PublicKey: publicKey, RelayURL: "http://127.0.0.1:32150",
	}); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]WorkerConfig{
		"negative concurrency": {RelayURL: "http://127.0.0.1:32150", IdentityFile: identityFile, MaxConcurrent: -1},
		"excess concurrency":   {RelayURL: "http://127.0.0.1:32150", IdentityFile: identityFile, MaxConcurrent: MaximumWorkerConcurrency + 1},
		"negative heartbeat":   {RelayURL: "http://127.0.0.1:32150", IdentityFile: identityFile, HeartbeatEvery: -time.Second},
		"excess timeout":       {RelayURL: "http://127.0.0.1:32150", IdentityFile: identityFile, RequestTimeout: maximumWorkerHTTPTimeout + time.Second},
		"remote local URL":     {RelayURL: "http://127.0.0.1:32150", IdentityFile: identityFile, LocalURL: "http://192.0.2.10:32145"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadWorker(cfg); err == nil {
				t.Fatal("unsafe direct worker configuration was accepted")
			}
		})
	}
}

func TestPostJSONRejectsOversizedPairingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(make([]byte, maximumPairResponseBytes+1))
	}))
	defer server.Close()
	var output map[string]interface{}
	err := postJSON(context.Background(), server.Client(), server.URL, "", map[string]string{"test": "value"}, &output)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response returned %v", err)
	}
}

func TestRandomDurationBelowStaysWithinBounds(t *testing.T) {
	if got := randomDurationBelow(0); got != 0 {
		t.Fatalf("zero maximum returned %s", got)
	}
	for index := 0; index < 100; index++ {
		got := randomDurationBelow(time.Second)
		if got < 0 || got >= time.Second {
			t.Fatalf("jitter outside [0, 1s): %s", got)
		}
	}
}

func TestWorkerRunningCounterCannotUnderflowOrOverflow(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	worker := &Worker{running: 1}
	worker.changeRunning(-maximumInt - 1)
	if worker.running != 0 {
		t.Fatalf("running counter underflowed to %d", worker.running)
	}
	worker.running = maximumInt
	worker.changeRunning(1)
	if worker.running != maximumInt {
		t.Fatalf("running counter overflowed to %d", worker.running)
	}
}
