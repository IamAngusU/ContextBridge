package cluster

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

type contractErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func TestContractValidationUsesSubmitAdmissionWithoutQueueing(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:     filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:   "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"},
		MaxJobBytes:  64,
		MaxAttempts:  7,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "contract-producer", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	legacy := SubmitRequest{
		ID:           "contract-parity",
		Requirements: Requirements{Task: "generation"},
		Payload:      json.RawMessage(`{"prompt":"safe"}`),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/contracts/validate", producer, raw)
	if status != http.StatusOK {
		t.Fatalf("validate returned %d: %s", status, body)
	}
	var validation ContractValidation
	if err := json.Unmarshal(body, &validation); err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.ContractVersion != JobContractV1 || validation.PayloadMode != "cleartext" || validation.MaxAttempts != 7 {
		t.Fatalf("unexpected validation: %#v", validation)
	}
	if jobs, err := relay.store.ListJobs(10, ""); err != nil || len(jobs) != 0 {
		t.Fatalf("validation mutated the queue: %#v, %v", jobs, err)
	}

	status, body = relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", producer, raw)
	if status != http.StatusAccepted {
		t.Fatalf("submit returned %d: %s", status, body)
	}
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	if job.ContractVersion != validation.ContractVersion || job.MaxAttempts != validation.MaxAttempts {
		t.Fatalf("submit normalization differs from validation: job=%#v validation=%#v", job, validation)
	}
}

func TestContractValidationAndSubmitReturnStableParityCodes(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:     filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:   "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"},
		MaxJobBytes:  64,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "contract-errors", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		input      SubmitRequest
		wantStatus int
		wantCode   string
	}{
		{
			name:       "unsupported contract",
			input:      SubmitRequest{ContractVersion: "contextbridge.job.v999", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "contract.unsupported",
		},
		{
			name:       "payload required",
			input:      SubmitRequest{Requirements: Requirements{Task: "generation"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "payload.required",
		},
		{
			name:       "payload too large",
			input:      SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"value":"` + strings.Repeat("x", 80) + `"}`)},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "payload.too_large",
		},
		{
			name:       "requirements invalid",
			input:      SubmitRequest{Requirements: Requirements{Task: "forbidden"}, Payload: json.RawMessage(`{}`)},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "requirements.invalid",
		},
		{
			name: "sealed payload requires reservation",
			input: SubmitRequest{Requirements: Requirements{Task: "generation"}, Sealed: &SealedEnvelope{
				Algorithm: sealedAlgorithm, EphemeralPublic: encode(make([]byte, 32)), Nonce: encode(make([]byte, 12)), Ciphertext: encode(make([]byte, 16)),
			}},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "reservation.required",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			raw, err := json.Marshal(item.input)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/v1/cluster/contracts/validate", "/v1/cluster/jobs"} {
				status, body := relayHTTPTest(t, http.MethodPost, server.URL+path, producer, raw)
				if status != item.wantStatus {
					t.Fatalf("%s returned %d, want %d: %s", path, status, item.wantStatus, body)
				}
				var response contractErrorResponse
				if err := json.Unmarshal(body, &response); err != nil {
					t.Fatal(err)
				}
				if response.Code != item.wantCode || response.Error == "" {
					t.Fatalf("%s returned unstable error: %#v", path, response)
				}
			}
		})
	}
}

func TestContractValidationAndSubmitSharePolicyDecision(t *testing.T) {
	policy := ExecutionPolicyConfig{
		Enabled: true, LocalProviders: []string{"ollama"}, RemoteProviders: []string{"adapter"},
		Default: ExecutionPolicyRule{Egress: "local_only", AllowedProviders: []string{"ollama"}},
	}
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"}, ExecutionPolicy: policy,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "policy-producer", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	denied := SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "adapter"}, Payload: json.RawMessage(`{}`)}
	deniedJSON, _ := json.Marshal(denied)
	for _, path := range []string{"/v1/cluster/contracts/validate", "/v1/cluster/jobs"} {
		status, body := relayHTTPTest(t, http.MethodPost, server.URL+path, producer, deniedJSON)
		if status != http.StatusForbidden || !strings.Contains(string(body), `"code":"policy.provider_denied"`) {
			t.Fatalf("%s did not fail with policy parity: %d %s", path, status, body)
		}
	}

	allowed := SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "ollama", Egress: "local_only"}, Payload: json.RawMessage(`{"prompt":"safe"}`)}
	allowedJSON, _ := json.Marshal(allowed)
	status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/contracts/validate", producer, allowedJSON)
	if status != http.StatusOK {
		t.Fatalf("validate returned %d: %s", status, body)
	}
	var validation ContractValidation
	if err := json.Unmarshal(body, &validation); err != nil {
		t.Fatal(err)
	}
	status, body = relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", producer, allowedJSON)
	if status != http.StatusAccepted {
		t.Fatalf("submit returned %d: %s", status, body)
	}
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	if !job.PolicyDecision.EquivalentAuthorization(validation.PolicyDecision) || validation.PolicyDecision.Outcome != "allow" {
		t.Fatalf("validation and durable job policy differ: %#v %#v", validation.PolicyDecision, job.PolicyDecision)
	}
}

func TestContractValidationRejectsReservationsAndUnknownFields(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:   filepath.Join(t.TempDir(), "relay.db"),
		AdminToken: "admin_012345678901234567890123456789012345",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "contract-strict", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	reserved, err := json.Marshal(SubmitRequest{
		Requirements: Requirements{Task: "generation"},
		Sealed: &SealedEnvelope{
			Algorithm: sealedAlgorithm, EphemeralPublic: encode(make([]byte, 32)), Nonce: encode(make([]byte, 12)), Ciphertext: encode(make([]byte, 16)),
		},
		AssignmentID: "assignment-a", AssignmentSecret: "secret-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/contracts/validate", producer, reserved)
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(body), `"code":"reservation.submit_only"`) {
		t.Fatalf("reservation validation returned %d: %s", status, body)
	}
	status, body = relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", producer, reserved)
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(body), `"code":"reservation.invalid_or_expired"`) {
		t.Fatalf("missing reservation submit returned %d: %s", status, body)
	}

	unknown := []byte(`{"requirements":{"task":"generation"},"payload":{},"surprise":true}`)
	status, body = relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/contracts/validate", producer, unknown)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"request.invalid_json"`) {
		t.Fatalf("unknown field returned %d: %s", status, body)
	}
}

func TestStoreDefaultsAndRejectsContractVersions(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if job.ContractVersion != JobContractV1 {
		t.Fatalf("legacy store request normalized to %q", job.ContractVersion)
	}
	if _, err := store.CreateJob(SubmitRequest{ContractVersion: "unknown", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("store accepted an unsupported contract version")
	}
}

func TestStoreMigratesLegacyContractAndRejectsUnknownDurableVersion(t *testing.T) {
	writeStoredJob := func(t *testing.T, path, version string) {
		t.Helper()
		store, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := bolt.Open(path, 0o600, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = db.Update(func(tx *bolt.Tx) error {
			if err := tx.Bucket(bucketStoreMeta).Delete(keyJobContractVersion); err != nil {
				return err
			}
			job := Job{ID: "retained-job", ContractVersion: version, Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), Status: JobCompleted}
			return putJSON(tx.Bucket(bucketJobs), job.ID, job)
		})
		closeErr := db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	writeStoredJob(t, legacyPath, "")
	legacy, err := OpenStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	job, err := legacy.GetJob("retained-job")
	legacy.Close()
	if err != nil || job.ContractVersion != JobContractV1 {
		t.Fatalf("legacy contract migration = %#v, %v", job, err)
	}

	futurePath := filepath.Join(t.TempDir(), "future.db")
	writeStoredJob(t, futurePath, "contextbridge.job.v999")
	if future, err := OpenStore(futurePath); err == nil {
		future.Close()
		t.Fatal("store opened an explicitly unknown durable contract version")
	}
}

func TestPublishedJobContractSchemaNamesRuntimeVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "schemas", "job-contract-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Const string `json:"const"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["contract_version"].Const != JobContractV1 {
		t.Fatalf("published schema contract version = %q, runtime = %q", schema.Properties["contract_version"].Const, JobContractV1)
	}
}

func TestAdmissionStoreErrorsHaveStableCodes(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{ErrQueueFull, AdmissionCodeCapacityQueue},
		{ErrOwnerQueueCapacity, AdmissionCodeCapacityOwnerQueue},
		{ErrReservationCapacity, AdmissionCodeCapacityReservation},
		{ErrOwnerReservationCapacity, AdmissionCodeCapacityOwnerReserve},
		{ErrAdapterSessionBusy, AdmissionCodeAdapterSessionBusy},
		{ErrReservationOwnerMismatch, AdmissionCodeReservationOwner},
		{ErrReservationContextMismatch, AdmissionCodeReservationContext},
		{ErrReservationInvalidOrExpired, AdmissionCodeReservationExpired},
		{ErrIdempotencyConflict, AdmissionCodeIdempotencyConflict},
		{ErrNodeDraining, AdmissionCodeNodeDraining},
	}
	for _, item := range cases {
		if got := admissionStoreErrorCode(item.err); got != item.code {
			t.Fatalf("admissionStoreErrorCode(%v) = %q, want %q", item.err, got, item.code)
		}
	}
	if got := admissionStoreErrorCode(errors.New("unclassified database failure")); got != "" {
		t.Fatalf("unknown storage error received misleading stable code %q", got)
	}
	if got := submissionStoreErrorCode(os.ErrExist); got != AdmissionCodeJobIDConflict {
		t.Fatalf("duplicate job code = %q", got)
	}
	if got := submissionStoreErrorCode(os.ErrNotExist); got != AdmissionCodeReservationExpired {
		t.Fatalf("missing reservation code = %q", got)
	}
	if got := reservationStoreErrorCode(os.ErrExist); got != AdmissionCodeReservationConflict {
		t.Fatalf("duplicate reservation code = %q", got)
	}
}

func TestSubmitReturnsStableConflictAndCapacityCodes(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:    "admin_012345678901234567890123456789012345",
		AllowedTasks:  []string{"generation"},
		MaxQueuedJobs: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "contract-capacity", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	submit := func(id string) (int, contractErrorResponse) {
		t.Helper()
		raw, err := json.Marshal(SubmitRequest{ID: id, Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", producer, raw)
		var response contractErrorResponse
		if status != http.StatusAccepted {
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatalf("decode error response: %v: %s", err, body)
			}
		}
		return status, response
	}

	if status, response := submit("stable-conflict"); status != http.StatusAccepted {
		t.Fatalf("first submit returned %d: %#v", status, response)
	}
	if status, response := submit("stable-conflict"); status != http.StatusConflict || response.Code != AdmissionCodeJobIDConflict {
		t.Fatalf("duplicate submit returned %d: %#v", status, response)
	}
	if status, response := submit("stable-capacity"); status != http.StatusServiceUnavailable || response.Code != AdmissionCodeCapacityQueue {
		t.Fatalf("capacity submit returned %d: %#v", status, response)
	}
}
