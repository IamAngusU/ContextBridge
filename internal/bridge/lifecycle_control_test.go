package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

const lifecycleTestToken = "test-lifecycle-token"

func TestLifecycleStopRequiresLoopbackBearerAndPOST(t *testing.T) {
	server := &Server{cfg: config.Config{Server: config.Server{Token: lifecycleTestToken}}}
	server.SetLifecycleControl(func() bool { return true }, func() {})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/system/stop", strings.NewReader(`{"force":false}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stop status = %d, want 401", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodGet, httpServer.URL+"/v1/system/stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+lifecycleTestToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("authorized GET status/Allow = %d/%q, want 405/POST", response.StatusCode, response.Header.Get("Allow"))
	}

	request = httptest.NewRequest(http.MethodPost, "http://localhost/v1/system/stop", strings.NewReader(`{"force":false}`))
	request.RemoteAddr = "198.51.100.7:43210"
	request.Header.Set("Authorization", "Bearer "+lifecycleTestToken)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-loopback stop status = %d, want 403", recorder.Code)
	}
}

func TestLifecycleStopRejectsBusyUnlessForceIsExplicit(t *testing.T) {
	var stops atomic.Int32
	server := &Server{cfg: config.Config{Server: config.Server{Token: lifecycleTestToken}}}
	server.SetLifecycleControl(func() bool { return false }, func() { stops.Add(1) })
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, raw := lifecycleStopRequest(t, httpServer.URL, `{"force":false}`)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "--force") {
		t.Fatalf("busy stop response = %d %s, want 409 with force guidance", response.StatusCode, raw)
	}
	if stops.Load() != 0 {
		t.Fatal("busy default stop invoked the root cancellation")
	}

	response, raw = lifecycleStopRequest(t, httpServer.URL, `{"force":true}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("forced stop response = %d %s, want 200", response.StatusCode, raw)
	}
	var result struct {
		OK       bool `json:"ok"`
		Stopping bool `json:"stopping"`
		Forced   bool `json:"forced"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.OK || !result.Stopping || !result.Forced {
		t.Fatalf("forced stop confirmation = %s (%v)", raw, err)
	}
	deadline := time.Now().Add(time.Second)
	for stops.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stops.Load() != 1 {
		t.Fatalf("forced stop callback count = %d, want 1", stops.Load())
	}
}

func TestHealthUsesConfiguredAggregateLifecycleIdle(t *testing.T) {
	directory := t.TempDir()
	server, err := NewServer(config.Config{
		Server:  config.Server{Token: lifecycleTestToken},
		Storage: config.Storage{Directory: directory, Inbox: directory},
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	server.SetLifecycleControl(func() bool { return false }, func() {})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var health struct {
		Idle bool `json:"idle"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.Idle {
		t.Fatal("health exposed local-only idle instead of the configured aggregate state")
	}
}

func TestLifecycleStopWritesConfirmationThenCancelsRootOnce(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stops atomic.Int32
	server := &Server{cfg: config.Config{Server: config.Server{Token: lifecycleTestToken}}}
	server.SetLifecycleControl(func() bool { return true }, func() {
		stops.Add(1)
		cancel()
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	for attempt := 0; attempt < 2; attempt++ {
		response, raw := lifecycleStopRequest(t, httpServer.URL, `{}`)
		if response.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"stopping":true`)) {
			t.Fatalf("stop confirmation %d = %d %s", attempt, response.StatusCode, raw)
		}
	}
	select {
	case <-root.Done():
	case <-time.After(time.Second):
		t.Fatal("successful stop did not cancel the root context")
	}
	if stops.Load() != 1 {
		t.Fatalf("root stop callback count = %d, want 1", stops.Load())
	}
	if _, err := server.beginJobAccounting(context.Background()); !errors.Is(err, errServiceStopping) {
		t.Fatalf("accepted stop did not close local job admission: %v", err)
	}
}

func TestLifecycleStopResponseSurvivesRealServerShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	server, err := NewServer(config.Config{
		Server:  config.Server{Listen: address, Token: lifecycleTestToken},
		Storage: config.Storage{Directory: directory, Inbox: directory},
		Runtime: config.Runtime{HardwareRefreshSeconds: 60},
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.SetLifecycleControl(server.Idle, cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(root) }()
	base := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, probeErr := http.Get(base + "/health")
		if probeErr == nil {
			response.Body.Close()
			break
		}
		select {
		case runErr := <-runDone:
			t.Fatalf("real server exited before becoming healthy: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("real server did not start: %v", probeErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, raw := lifecycleStopRequest(t, base, `{}`)
	if response.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"stopping":true`)) {
		t.Fatalf("real shutdown response = %d %s", response.StatusCode, raw)
	}
	if response.ContentLength != int64(len(raw)) || !response.Close {
		t.Fatalf("stop response framing = length %d/%d close=%v", response.ContentLength, len(raw), response.Close)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("graceful server shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("real server did not exit after confirmed stop")
	}
	if _, _, err := server.claimSchedule("new-run-after-stop", time.Now().UTC(), true); !errors.Is(err, errServiceStopping) {
		t.Fatalf("accepted stop did not close schedule admission: %v", err)
	}
}

func TestRejectedBusyStopReopensLocalAdmission(t *testing.T) {
	directory := t.TempDir()
	server, err := NewServer(config.Config{
		Server:  config.Server{Token: lifecycleTestToken},
		Storage: config.Storage{Directory: directory, Inbox: directory},
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	server.SetLifecycleControl(func() bool { return false }, func() {})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response, _ := lifecycleStopRequest(t, httpServer.URL, `{}`)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("busy stop status = %d, want 409", response.StatusCode)
	}
	release, err := server.beginJobAccounting(context.Background())
	if err != nil {
		t.Fatalf("rejected stop left local admission quiesced: %v", err)
	}
	release()
}

func TestConcurrentSiblingAdmissionRejectsStopAndReopensLocalAdmission(t *testing.T) {
	directory := t.TempDir()
	server, err := NewServer(config.Config{
		Server:  config.Server{Token: lifecycleTestToken},
		Storage: config.Storage{Directory: directory, Inbox: directory},
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	var stops atomic.Int32
	server.SetLifecycleControl(func() bool { return true }, func() { stops.Add(1) })
	server.SetLifecycleQuiesce(func(bool) bool { return false })
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response, raw := lifecycleStopRequest(t, httpServer.URL, `{}`)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "became busy") {
		t.Fatalf("sibling admission race response = %d %s, want 409", response.StatusCode, raw)
	}
	if stops.Load() != 0 {
		t.Fatal("sibling admission race still cancelled the root context")
	}
	release, err := server.beginJobAccounting(context.Background())
	if err != nil {
		t.Fatalf("rejected sibling quiesce left local admission closed: %v", err)
	}
	release()
}

func TestLifecycleStopRejectsInvalidOrUnconfiguredControlRequests(t *testing.T) {
	server := &Server{cfg: config.Config{Server: config.Server{Token: lifecycleTestToken}}}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, _ := lifecycleStopRequest(t, httpServer.URL, `{}`)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured stop status = %d, want 503", response.StatusCode)
	}

	server.SetLifecycleControl(func() bool { return true }, func() {})
	response, _ = lifecycleStopRequest(t, httpServer.URL, `{"force":true,"surprise":true}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown stop field status = %d, want 400", response.StatusCode)
	}
	response, _ = lifecycleStopRequest(t, httpServer.URL, `{"force":`+strings.Repeat(" ", maximumControlRequestBytes)+`true}`)
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized stop status = %d, want 413", response.StatusCode)
	}
}

func lifecycleStopRequest(t *testing.T, base, body string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/v1/system/stop", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+lifecycleTestToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, raw
}
