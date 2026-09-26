package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestClusterLANInitCreatesReusablePublicBundleAndPrivateIdentity(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(directory, "join.json")
	args := []string{"--config", configPath, "--listen", "127.0.0.1:32151", "--advertise-host", "127.0.0.1", "--out", bundlePath}
	if err := clusterLANInitCommand(args); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !configured.Cluster.Relay.Enabled || !configured.Cluster.Relay.LAN.Enabled || configured.Cluster.Relay.LAN.PublicURL != "https://127.0.0.1:32151" {
		t.Fatalf("LAN relay config = %#v", configured.Cluster.Relay)
	}
	bundle, err := cluster.LoadLANJoinBundle(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Trust.SPKISHA256 == "" || strings.Contains(string(mustReadCLIFile(t, bundlePath)), "PRIVATE KEY") {
		t.Fatal("join bundle is missing its pin or contains private key material")
	}
	keyInfo, err := os.Stat(configured.Cluster.Relay.LAN.PrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && keyInfo.Mode().Perm()&0077 != 0 {
		t.Fatalf("private key permissions = %o", keyInfo.Mode().Perm())
	}
	if err := clusterLANInitCommand(args); err != nil {
		t.Fatalf("idempotent LAN initialization failed: %v", err)
	}
}

func TestClusterLANInitDefaultsToAdvertisedInterface(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(directory, "join.json")
	if err := clusterLANInitCommand([]string{"--config", configPath, "--advertise-host", "192.168.44.20", "--out", bundlePath}); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := configured.Cluster.Relay.LAN.Listen; got != "192.168.44.20:32151" {
		t.Fatalf("safe LAN listen = %q, want selected interface", got)
	}
}

func TestClusterLANInitRejectsContradictoryConcreteListen(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	err := clusterLANInitCommand([]string{"--config", configPath, "--listen", "192.168.44.21:32151", "--advertise-host", "192.168.44.20", "--out", filepath.Join(directory, "join.json")})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("contradictory listen error = %v", err)
	}
}

func TestLANListenAdvertisementAllowsOnlyExplicitWildcard(t *testing.T) {
	wildcard, err := validateLANListenAdvertisement("0.0.0.0:32151", "192.168.44.20")
	if err != nil || !wildcard {
		t.Fatalf("explicit wildcard = %v, %v", wildcard, err)
	}
	if _, err := validateLANListenAdvertisement("127.0.0.1:32151", "cb.home.arpa"); err == nil {
		t.Fatal("loopback listener advertised as LAN DNS name")
	}
}

func TestClusterClientUsesTrustBoundToWorkerIdentity(t *testing.T) {
	pinnedClusterClients = sync.Map{}
	t.Cleanup(func() { pinnedClusterClients = sync.Map{} })
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "cert.pem")
	privateKeyPath := filepath.Join(directory, "key.pem")
	trust, err := cluster.EnsureLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/cluster/overview" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"nodes_total":0}`))
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	defer server.Close()
	privateKey, publicKey, err := cluster.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(directory, "worker.json")
	identity := cluster.WorkerIdentity{NodeID: "node_lan_test", NodeToken: strings.Repeat("n", 40), PrivateKey: privateKey, PublicKey: publicKey, RelayURL: server.URL, RelayTrust: &trust}
	raw, _ := json.Marshal(identity)
	if err := os.WriteFile(identityPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Cluster.Worker.RelayURL = server.URL
	cfg.Cluster.Worker.IdentityFile = identityPath
	target := clusterBaseURL(cfg) + "/v1/cluster/overview"
	var output map[string]interface{}
	if err := clusterGET(t.Context(), target, strings.Repeat("t", 40), &output); err != nil {
		t.Fatalf("pinned cluster client failed: %v", err)
	}
}

func TestClusterTrustNeverLeaksToAnotherOriginOrSurvivesIdentityLoss(t *testing.T) {
	pinnedClusterClients = sync.Map{}
	t.Cleanup(func() { pinnedClusterClients = sync.Map{} })
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "cert.pem")
	privateKeyPath := filepath.Join(directory, "key.pem")
	trust, err := cluster.EnsureLANTLSIdentity(certificatePath, privateKeyPath, "127.0.0.1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey, err := cluster.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(directory, "worker.json")
	workerRelayURL := "https://127.0.0.1:32151"
	identity := cluster.WorkerIdentity{NodeID: "node_origin_scope", NodeToken: strings.Repeat("n", 40), PrivateKey: privateKey, PublicKey: publicKey, RelayURL: workerRelayURL, RelayTrust: &trust}
	raw, _ := json.Marshal(identity)
	if err := os.WriteFile(identityPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Cluster.Worker.RelayURL = workerRelayURL
	cfg.Cluster.Worker.IdentityFile = identityPath

	otherOrigin := "https://127.0.0.1:32152"
	registerClusterTrust(cfg, otherOrigin)
	if got := clusterHTTPClient(otherOrigin); got != http.DefaultClient {
		t.Fatal("worker relay trust leaked to a different URL origin")
	}
	registerClusterTrust(cfg, workerRelayURL)
	if got := clusterHTTPClient(workerRelayURL); got == http.DefaultClient {
		t.Fatal("worker relay trust was not registered for its exact origin")
	}
	if err := os.Remove(identityPath); err != nil {
		t.Fatal(err)
	}
	registerClusterTrust(cfg, workerRelayURL)
	if got := clusterHTTPClient(workerRelayURL); got != http.DefaultClient {
		t.Fatal("stale pinned client survived loss of its worker identity")
	}
}

func mustReadCLIFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
