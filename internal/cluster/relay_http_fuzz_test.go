package cluster

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func TestProducerJobHTTPIsolatesListReadAndCancel(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:     filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:   "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	producerA, _, err := relay.store.CreateToken("producer", "producer-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	producerB, _, err := relay.store.CreateToken("producer", "producer-b", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	submit := func(token, id, secret string) Job {
		t.Helper()
		input := SubmitRequest{
			ID:           id,
			Requirements: Requirements{Task: "generation"},
			Payload:      json.RawMessage(`{"prompt":"` + secret + `"}`),
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", token, raw)
		if status != http.StatusAccepted {
			t.Fatalf("submit %s returned %d: %s", id, status, body)
		}
		var job Job
		if err := json.Unmarshal(body, &job); err != nil {
			t.Fatal(err)
		}
		return job
	}

	jobA := submit(producerA, "owner-a-job", "owner-a-secret")
	jobB := submit(producerB, "owner-b-job", "owner-b-secret")

	status, body := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs?limit=100", producerA, nil)
	if status != http.StatusOK {
		t.Fatalf("producer list returned %d: %s", status, body)
	}
	var jobs []Job
	if err := json.Unmarshal(body, &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != jobA.ID || jobs[0].OwnerSubject != "producer-a" {
		t.Fatalf("producer list crossed owner boundary: %#v", jobs)
	}
	if bytes.Contains(body, []byte(jobB.ID)) || bytes.Contains(body, []byte("owner-b-secret")) {
		t.Fatalf("foreign job leaked through producer list: %s", body)
	}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		status, body = relayHTTPTest(t, method, server.URL+"/v1/cluster/jobs/"+jobA.ID, producerB, nil)
		if status != http.StatusForbidden {
			t.Fatalf("foreign %s returned %d, want 403: %s", method, status, body)
		}
		if bytes.Contains(body, []byte("owner-a-secret")) {
			t.Fatalf("foreign %s leaked job content: %s", method, body)
		}
	}

	status, body = relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs/"+jobA.ID+"?compact=1", producerA, nil)
	if status != http.StatusOK {
		t.Fatalf("owner read returned %d: %s", status, body)
	}
	var owned Job
	if err := json.Unmarshal(body, &owned); err != nil {
		t.Fatal(err)
	}
	if owned.ID != jobA.ID || owned.Status != JobQueued || len(owned.Payload) != 0 || owned.SealedPayload != nil {
		t.Fatalf("compact owner read retained input or returned wrong job: %#v", owned)
	}

	status, body = relayHTTPTest(t, http.MethodDelete, server.URL+"/v1/cluster/jobs/"+jobA.ID, producerA, nil)
	if status != http.StatusOK {
		t.Fatalf("owner cancel returned %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &owned); err != nil || owned.Status != JobCancelled {
		t.Fatalf("owner cancel returned an invalid job: %#v, %v", owned, err)
	}
	savedB, err := relay.store.GetJob(jobB.ID)
	if err != nil || savedB.Status != JobQueued {
		t.Fatalf("foreign cancel changed another producer's job: %#v, %v", savedB, err)
	}
}

func TestSubmitHTTPRejectsMalformedUTF8AndUnsafeUnicodeLabels(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:     filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:   "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "unicode-boundary-producer", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	exact160 := strings.Repeat("界", 53) + "a"
	if len(exact160) != 160 {
		t.Fatalf("invalid test fixture length: %d", len(exact160))
	}
	valid := SubmitRequest{ID: "unicode-exact", Requirements: Requirements{Task: "generation", Model: exact160}, Payload: json.RawMessage(`{}`)}
	validRaw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", producer, validRaw); status != http.StatusAccepted {
		t.Fatalf("exact 160-byte UTF-8 label returned %d: %s", status, body)
	}

	cases := []struct {
		name         string
		requirements Requirements
	}{
		{name: "model one byte over", requirements: Requirements{Task: "generation", Model: exact160 + "b"}},
		{name: "model bidi override", requirements: Requirements{Task: "generation", Model: "safe\u202espoof"}},
		{name: "model line separator", requirements: Requirements{Task: "generation", Model: "safe\u2028spoof"}},
		{name: "group zero width joiner", requirements: Requirements{Task: "generation", Group: "safe\u200dspoof"}},
		{name: "session paragraph separator", requirements: Requirements{Task: "generation", SessionID: "safe\u2029spoof"}},
		{name: "provider isolate", requirements: Requirements{Task: "generation", Provider: "adapter\u2066spoof"}},
		{name: "tag at 81 bytes", requirements: Requirements{Task: "generation", RequiredTags: []string{strings.Repeat("t", 81)}}},
	}
	for index, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			input := SubmitRequest{ID: "unsafe-label-" + string(rune('a'+index)), Requirements: item.requirements, Payload: json.RawMessage(`{}`)}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", producer, raw)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("unsafe label returned %d, want 422: %s", status, body)
			}
		})
	}

	invalidUTF8 := append([]byte(`{"id":"invalid-utf8","requirements":{"task":"generation","model":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"},"payload":{}}`)...)
	if status, body := relayHTTPTest(t, http.MethodPost, server.URL+"/v1/cluster/jobs", producer, invalidUTF8); status != http.StatusBadRequest {
		t.Fatalf("malformed UTF-8 JSON returned %d, want 400: %s", status, body)
	}
}

func TestValidateWorkerResultBoundariesAndMalformedUTF8(t *testing.T) {
	plainJob := Job{Payload: json.RawMessage(`{}`)}
	prefix := []byte(`{"text":"`)
	suffix := []byte(`"}`)
	filler := int(MaximumJobResultBytes) - len(prefix) - len(suffix)
	if filler <= 0 {
		t.Fatal("result boundary is too small for a JSON object")
	}
	exact := make([]byte, 0, int(MaximumJobResultBytes)+1)
	exact = append(exact, prefix...)
	exact = append(exact, bytes.Repeat([]byte{'x'}, filler)...)
	exact = append(exact, suffix...)
	if err := validateWorkerResult(plainJob, exact, nil, ""); err != nil {
		t.Fatalf("exact maximum plaintext result was rejected: %v", err)
	}
	over := append(append([]byte{}, exact[:len(exact)-len(suffix)]...), 'x')
	over = append(over, suffix...)
	if err := validateWorkerResult(plainJob, over, nil, ""); err == nil {
		t.Fatal("plaintext result one byte over the maximum was accepted")
	}
	invalidUTF8 := append([]byte(`{"text":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	if err := validateWorkerResult(plainJob, invalidUTF8, nil, ""); err == nil {
		t.Fatal("malformed UTF-8 worker result was accepted")
	}
	for name, result := range map[string]json.RawMessage{
		"empty":  nil,
		"scalar": json.RawMessage(`"text"`),
		"array":  json.RawMessage(`[]`),
		"broken": json.RawMessage(`{"text":`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateWorkerResult(plainJob, result, nil, ""); err == nil {
				t.Fatal("malformed or non-object plaintext result was accepted")
			}
		})
	}

	sealedJob := Job{SealedPayload: &SealedEnvelope{Algorithm: sealedAlgorithm}}
	validSealed := &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: encode(make([]byte, 12)), Ciphertext: encode(make([]byte, 16))}
	if err := validateWorkerResult(sealedJob, nil, validSealed, ""); err != nil {
		t.Fatalf("minimum structurally valid sealed result was rejected: %v", err)
	}
	sealedCases := map[string]*SealedEnvelope{
		"wrong algorithm":    {Algorithm: "none", Nonce: validSealed.Nonce, Ciphertext: validSealed.Ciphertext},
		"public key present": {Algorithm: sealedAlgorithm, EphemeralPublic: "unexpected", Nonce: validSealed.Nonce, Ciphertext: validSealed.Ciphertext},
		"bad nonce base64":   {Algorithm: sealedAlgorithm, Nonce: "%%%", Ciphertext: validSealed.Ciphertext},
		"short nonce":        {Algorithm: sealedAlgorithm, Nonce: encode(make([]byte, 11)), Ciphertext: validSealed.Ciphertext},
		"bad ciphertext":     {Algorithm: sealedAlgorithm, Nonce: validSealed.Nonce, Ciphertext: "%%%"},
		"short ciphertext":   {Algorithm: sealedAlgorithm, Nonce: validSealed.Nonce, Ciphertext: encode(make([]byte, 15))},
	}
	for name, envelope := range sealedCases {
		t.Run(name, func(t *testing.T) {
			if err := validateWorkerResult(sealedJob, nil, envelope, ""); err == nil {
				t.Fatal("malformed sealed result was accepted")
			}
		})
	}
	if err := validateWorkerResult(sealedJob, json.RawMessage(`{}`), validSealed, ""); err == nil {
		t.Fatal("ambiguous plaintext plus sealed result was accepted")
	}
	if err := validateWorkerResult(plainJob, json.RawMessage(`{}`), nil, "worker failed"); err == nil {
		t.Fatal("failed completion carrying a result was accepted")
	}
}

func TestValidateSubmitPayloadRejectsMalformedSealedEnvelopes(t *testing.T) {
	const limit = int64(64)
	valid := &SealedEnvelope{
		Algorithm:       sealedAlgorithm,
		EphemeralPublic: encode(make([]byte, 32)),
		Nonce:           encode(make([]byte, 12)),
		Ciphertext:      encode(make([]byte, 16)),
	}
	if err := validateSubmitPayload(SubmitRequest{Sealed: valid}, limit); err != nil {
		t.Fatalf("minimum structurally valid sealed submission was rejected: %v", err)
	}
	cases := map[string]*SealedEnvelope{
		"wrong algorithm": {Algorithm: "none", EphemeralPublic: valid.EphemeralPublic, Nonce: valid.Nonce, Ciphertext: valid.Ciphertext},
		"bad public key":  {Algorithm: sealedAlgorithm, EphemeralPublic: "%%%", Nonce: valid.Nonce, Ciphertext: valid.Ciphertext},
		"short public key": {
			Algorithm: sealedAlgorithm, EphemeralPublic: encode(make([]byte, 31)), Nonce: valid.Nonce, Ciphertext: valid.Ciphertext,
		},
		"bad nonce": {Algorithm: sealedAlgorithm, EphemeralPublic: valid.EphemeralPublic, Nonce: "%%%", Ciphertext: valid.Ciphertext},
		"short nonce": {
			Algorithm: sealedAlgorithm, EphemeralPublic: valid.EphemeralPublic, Nonce: encode(make([]byte, 11)), Ciphertext: valid.Ciphertext,
		},
		"bad ciphertext": {Algorithm: sealedAlgorithm, EphemeralPublic: valid.EphemeralPublic, Nonce: valid.Nonce, Ciphertext: "%%%"},
		"short ciphertext": {
			Algorithm: sealedAlgorithm, EphemeralPublic: valid.EphemeralPublic, Nonce: valid.Nonce, Ciphertext: encode(make([]byte, 15)),
		},
		"oversized ciphertext": {
			Algorithm: sealedAlgorithm, EphemeralPublic: valid.EphemeralPublic, Nonce: valid.Nonce, Ciphertext: encode(make([]byte, limit+17)),
		},
	}
	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateSubmitPayload(SubmitRequest{Sealed: envelope}, limit); err == nil {
				t.Fatal("malformed sealed submission was accepted")
			}
		})
	}
	if err := validateSubmitPayload(SubmitRequest{Payload: json.RawMessage(`{}`), Sealed: valid}, limit); err == nil {
		t.Fatal("submission containing plaintext and encrypted payloads was accepted")
	}
	if err := validateSubmitPayload(SubmitRequest{Payload: bytes.Repeat([]byte{'x'}, int(limit+1))}, limit); err == nil {
		t.Fatal("plaintext submission one byte over its limit was accepted")
	}
}

func FuzzValidProducerJobIDContract(f *testing.F) {
	for _, seed := range []string{"a", "job-1", "A.b_c-9", strings.Repeat("z", 128), "", ".hidden", "a..b", "a/b", "ü", "a\x00b", string([]byte{0xff})} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if got, want := validJobID(value), referenceValidJobID(value); got != want {
			t.Fatalf("validJobID(%q) = %v, want %v", value, got, want)
		}
	})
}

func FuzzValidRoutingLabelContract(f *testing.F) {
	for _, seed := range []string{"model", "Adapter Model A", "模型", strings.Repeat("é", 80), " safe", "safe ", "safe\nspoof", "safe\u202espoof", "safe\u2028spoof", string([]byte{0xff})} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if got, want := validRoutingLabel(value, 160), referenceValidRoutingLabel(value, 160); got != want {
			t.Fatalf("validRoutingLabel(%q) = %v, want %v", value, got, want)
		}
	})
}

func referenceValidJobID(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.Contains(value, "..") {
		return false
	}
	for index := range len(value) {
		character := value[index]
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphaNumeric && (index == 0 || character != '.' && character != '_' && character != '-') {
			return false
		}
	}
	return true
}

func referenceValidRoutingLabel(value string, maximum int) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
	}) < 0
}

func relayHTTPTest(t *testing.T, method, target, token string, body []byte) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var responseBody bytes.Buffer
	if _, err := responseBody.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody.Bytes()
}
