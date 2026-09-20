package cluster

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestPublicRelayHealthIsContentMinimizing(t *testing.T) {
	const admin = "admin_external_audit_012345678901234567890123"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin, Version: "v-test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var public map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&public); err != nil {
		t.Fatal(err)
	}
	if public["ok"] != true || public["version"] != "v-test" || public["overview"] != nil || public["idle"] != nil {
		t.Fatalf("public health leaked relay activity: %#v", public)
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/cluster/lifecycle", nil)
	unauthorized, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("lifecycle endpoint was public: %s", unauthorized.Status)
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/v1/cluster/lifecycle", nil)
	request.Header.Set("Authorization", "Bearer "+admin)
	authorized, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer authorized.Body.Close()
	var lifecycle map[string]interface{}
	if err := json.NewDecoder(authorized.Body).Decode(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if _, ok := lifecycle["idle"]; !ok || lifecycle["overview"] != nil {
		t.Fatalf("authenticated lifecycle shape is wrong: %#v", lifecycle)
	}
}

func TestDashboardCSPDoesNotPermitArbitraryInlineScript(t *testing.T) {
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_external_audit_abcdefghijklmnopqrstuvwxyz"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	relay.Handler().ServeHTTP(response, request)
	policy := response.Header().Get("Content-Security-Policy")
	if strings.Contains(policy, "script-src 'self' 'unsafe-inline'") || !strings.Contains(policy, "script-src 'sha256-") {
		t.Fatalf("dashboard script CSP is not hash-bound: %q", policy)
	}
}

func TestRateLimiterRejectsUnknownKeysAtHardCapacity(t *testing.T) {
	relay := &Relay{rate: make(map[string]*rateWindow)}
	now := time.Now()
	for index := 0; index < maximumRateLimitBuckets; index++ {
		relay.rate["198.51.100."+randomID("")] = &rateWindow{started: now}
	}
	handler := relay.rateLimit(1, time.Minute, func(http.ResponseWriter, *http.Request) {
		t.Fatal("capacity-exhausted limiter called the protected handler")
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/pair/request", nil)
	request.RemoteAddr = "203.0.113.44:12345"
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusTooManyRequests || len(relay.rate) != maximumRateLimitBuckets {
		t.Fatalf("rate limiter capacity was not fail-closed: status=%d buckets=%d", response.Code, len(relay.rate))
	}
}

func TestWorkerWireLimitsAreMessageSpecific(t *testing.T) {
	if workerMessageWireLimit("heartbeat") >= MaximumJobResultWireBytes || workerMessageWireLimit("progress") >= workerMessageWireLimit("heartbeat") || workerMessageWireLimit("result") != MaximumJobResultWireBytes {
		t.Fatalf("unexpected worker wire limits: heartbeat=%d progress=%d result=%d", workerMessageWireLimit("heartbeat"), workerMessageWireLimit("progress"), workerMessageWireLimit("result"))
	}
}

func TestPairingCapacityAndFingerprintAreBounded(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.CreatePairing(PairRequest{NodeName: "worker", PublicKey: publicKey, Groups: []string{"private"}}, "https://relay.example/#pair", time.Minute)
	if err != nil || response.UserCode == "" {
		t.Fatalf("valid pairing failed: %#v %v", response, err)
	}
	pairings, err := store.ListPairings()
	if err != nil || len(pairings) != 1 || !strings.HasPrefix(pairings[0].PublicKeyFingerprint, "sha256:") || len(pairings[0].Groups) != 1 {
		t.Fatalf("pairing approval evidence is incomplete: %#v %v", pairings, err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketPairings)
		for index := bucket.Stats().KeyN; index < maximumPendingPairings; index++ {
			pairing := Pairing{DeviceCodeHash: randomID("pair"), UserCode: randomID("code"), NodeName: "flood", PublicKey: publicKey, ExpiresAt: time.Now().Add(time.Minute)}
			if err := putJSON(bucket, pairing.DeviceCodeHash, pairing); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePairing(PairRequest{NodeName: "overflow", PublicKey: publicKey}, "https://relay.example/#pair", time.Minute); err == nil || !strings.Contains(err.Error(), "too many pending") {
		t.Fatalf("pairing capacity did not fail closed: %v", err)
	}
}

func TestAdmissionRejectsSourceAndAttemptAmbiguity(t *testing.T) {
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_external_audit_abcdefghijklmnopqrstuvwxyz", AllowedTasks: []string{"generation"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	record := TokenRecord{Role: "producer", Subject: "producer-a"}
	base := SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"ok"}`)}
	tooLong := base
	tooLong.Source = strings.Repeat("s", 121)
	if _, _, err := relay.prepareAdmission(tooLong, record, admissionValidateOnly); admissionErrorCode(err) != AdmissionCodeSourceInvalid {
		t.Fatalf("oversized source was not rejected with a stable code: %v", err)
	}
	negative := base
	negative.MaxAttempts = -1
	if _, _, err := relay.prepareAdmission(negative, record, admissionValidateOnly); err == nil || !strings.Contains(err.Error(), "max_attempts") {
		t.Fatalf("negative max_attempts became a default: %v", err)
	}
}

func TestStrictJSONRejectsDuplicateProperties(t *testing.T) {
	var target map[string]interface{}
	err := decodeJSON(strings.NewReader(`{"priority":1,"nested":{"task":"a","task":"b"}}`), &target, 4096)
	if err == nil || !strings.Contains(err.Error(), "duplicate JSON property") {
		t.Fatalf("duplicate property was accepted: %v", err)
	}
}

func TestOpenSharedRejectsWrongNonceLengthWithoutPanic(t *testing.T) {
	envelope := &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: encode([]byte{1}), Ciphertext: encode([]byte("invalid"))}
	if _, err := openShared(make([]byte, 32), envelope, nil, "test"); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("wrong nonce length was not rejected: %v", err)
	}
}

func admissionErrorCode(err error) string {
	if problem, ok := err.(*admissionError); ok {
		return problem.code
	}
	return ""
}
