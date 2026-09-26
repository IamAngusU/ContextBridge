package cluster

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPinnedLANRelayClientRejectsUntrustedAndWrongIdentity(t *testing.T) {
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "relay-cert.pem")
	privateKeyPath := filepath.Join(directory, "relay-key.pem")
	trust, err := EnsureLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	defer server.Close()

	client, err := NewRelayHTTPClient(server.URL, trust, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("pinned request failed: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if _, err := http.DefaultClient.Get(server.URL); err == nil {
		t.Fatal("system trust unexpectedly accepted the private self-signed relay")
	}
	wrong := trust
	wrong.SPKISHA256 = "sha256:" + strings.Repeat("0", 64)
	if _, err := NewRelayHTTPClient(server.URL, wrong, time.Second); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong relay pin was accepted: %v", err)
	}
}

func TestLANJoinBundleIsStrictAndHostBound(t *testing.T) {
	directory := t.TempDir()
	trust, err := EnsureLANTLSIdentity(filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem"), "127.0.0.1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "join.json")
	bundle := LANJoinBundle{Version: LANJoinBundleVersion, RelayURL: "https://127.0.0.1:32151", Trust: trust, CreatedAt: time.Now().UTC()}
	if err := SaveLANJoinBundle(path, bundle); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLANJoinBundle(path)
	if err != nil || loaded.Trust.SPKISHA256 != trust.SPKISHA256 {
		t.Fatalf("load bundle = %#v, %v", loaded, err)
	}
	loaded.RelayURL = "https://localhost:32151"
	if err := ValidateLANJoinBundle(loaded, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("certificate host substitution was accepted: %v", err)
	}
	if err := os.WriteFile(path, append([]byte(strings.TrimSpace(string(mustReadTestFile(t, path)))), []byte(`{"extra":true}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLANJoinBundle(path); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestValidateLANListenerEndpointRejectsContradictions(t *testing.T) {
	if err := ValidateLANListenerEndpoint("192.168.10.2:32151", "https://192.168.10.2:32151"); err != nil {
		t.Fatalf("matching LAN endpoint rejected: %v", err)
	}
	if err := ValidateLANListenerEndpoint("0.0.0.0:32151", "https://192.168.10.2:32151"); err != nil {
		t.Fatalf("explicit wildcard rejected: %v", err)
	}
	if err := ValidateLANListenerEndpoint("192.168.10.3:32151", "https://192.168.10.2:32151"); err == nil {
		t.Fatal("contradictory concrete LAN endpoint accepted")
	}
	if err := ValidateLANListenerEndpoint("127.0.0.1:32151", "https://cb.home.arpa:32151"); err == nil {
		t.Fatal("loopback listener advertised as LAN DNS endpoint")
	}
}

func TestLoadLANTLSIdentityNeverCreatesMissingTrustMaterial(t *testing.T) {
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "cert.pem")
	privateKeyPath := filepath.Join(directory, "key.pem")
	if _, err := LoadLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "identity is missing") {
		t.Fatalf("missing identity was accepted: %v", err)
	}
	for _, path := range []string{certificatePath, privateKeyPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only load created %s: %v", path, err)
		}
	}
	trust, err := EnsureLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC())
	if err != nil || loaded.SPKISHA256 != trust.SPKISHA256 {
		t.Fatalf("existing identity did not load: %#v, %v", loaded, err)
	}
}

func TestLoadLANTLSIdentityRejectsBroadUnixPrivateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses ACLs rather than Unix permission bits")
	}
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "cert.pem")
	privateKeyPath := filepath.Join(directory, "key.pem")
	if _, err := EnsureLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateKeyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("broad private-key permissions were accepted: %v", err)
	}
	if err := os.Chmod(privateKeyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC()); err != nil {
		t.Fatalf("owner-only private key was rejected: %v", err)
	}
}

func TestNewRelayRejectsMissingLANIdentityBeforeOpeningStore(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "cluster.db")
	_, err := NewRelay(RelayConfig{
		Listen:            "127.0.0.1:0",
		PublicURL:         "http://127.0.0.1:32150",
		LANListen:         "127.0.0.1:0",
		LANPublicURL:      "https://127.0.0.1:32151",
		LANTLSCertificate: filepath.Join(directory, "missing-cert.pem"),
		LANTLSPrivateKey:  filepath.Join(directory, "missing-key.pem"),
		Database:          databasePath,
		AdminToken:        strings.Repeat("a", 40),
		PairingTTL:        time.Minute,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "identity is missing") {
		t.Fatalf("relay accepted a missing LAN identity: %v", err)
	}
	if _, statErr := os.Lstat(databasePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("relay mutated its durable store before validating LAN identity: %v", statErr)
	}
}

func TestLANRelayPairsAndConnectsWithPinnedTLS(t *testing.T) {
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "relay-cert.pem")
	privateKeyPath := filepath.Join(directory, "relay-key.pem")
	trust, err := EnsureLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	loopback := freeTestAddress(t)
	lan := freeTestAddress(t)
	relay, err := NewRelay(RelayConfig{
		Version: "test", Listen: loopback, PublicURL: "http://" + loopback,
		LANListen: lan, LANPublicURL: "https://" + lan, LANTLSCertificate: certificatePath, LANTLSPrivateKey: privateKeyPath,
		Database: filepath.Join(directory, "cluster.db"), AdminToken: strings.Repeat("a", 40), PairingTTL: time.Minute,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(ctx) }()
	client, err := NewRelayHTTPClient("https://"+lan, trust, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	waitForLANTest(t, 5*time.Second, func() bool {
		response, requestErr := client.Get("https://" + lan + "/health")
		if requestErr != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == http.StatusOK
	})

	identityPath := filepath.Join(directory, "worker.json")
	pairDone := make(chan error, 1)
	go func() {
		pairDone <- PairWorkerWithTrust(ctx, "https://"+lan, "lan-worker", identityPath, []string{"default"}, trust, nil)
	}()
	var userCode string
	waitForLANTest(t, 5*time.Second, func() bool {
		pairings, listErr := relay.store.ListPairings()
		if listErr != nil || len(pairings) != 1 {
			return false
		}
		userCode = pairings[0].UserCode
		return true
	})
	if _, err := relay.store.DecidePairing(userCode, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-pairDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("pinned LAN pairing timed out")
	}
	loadedTrust, err := LoadWorkerRelayTrust(identityPath, "https://"+lan)
	if err != nil || !strings.EqualFold(loadedTrust.SPKISHA256, trust.SPKISHA256) {
		t.Fatalf("saved trust = %#v, %v", loadedTrust, err)
	}
	worker, err := LoadWorker(WorkerConfig{RelayURL: "https://" + lan, IdentityFile: identityPath, Name: "lan-worker", LocalURL: "http://127.0.0.1:1", LocalToken: strings.Repeat("b", 40), MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx, nil) }()
	waitForLANTest(t, 15*time.Second, func() bool {
		nodes, listErr := relay.store.ListNodes()
		return listErr == nil && len(nodes) == 1 && nodes[0].Connected
	})
	cancel()
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("LAN worker did not stop")
	}
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LAN relay did not stop")
	}
}

func freeTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func waitForLANTest(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition did not become ready")
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
