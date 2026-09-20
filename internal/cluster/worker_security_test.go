package cluster

import (
	"context"
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
