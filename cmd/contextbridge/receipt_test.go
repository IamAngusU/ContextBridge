package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func FuzzReceiptCanonicalJSONRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`{"b":2,"a":1}`,
		` { "a" : 1, "b" : 2 } `,
		`{"n":9007199254740993,"nested":[true,null,"x"]}`,
		`[]`, `null`, `1e-9`, `{"a":1}{"b":2}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maximumExecutionReceiptBytes {
			t.Skip()
		}
		first, err := receiptCanonicalJSONDigest(raw)
		if err != nil {
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value interface{}
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("receipt digest accepted JSON that the canonical decoder rejected: %v", err)
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("canonical receipt value did not marshal: %v", err)
		}
		second, err := receiptCanonicalJSONDigest(canonical)
		if err != nil || first != second {
			t.Fatalf("canonical receipt digest changed across round trip: %q != %q (%v)", first, second, err)
		}
	})
}

func FuzzReceiptEvidenceMutationChangesChecksum(f *testing.F) {
	for _, seed := range []string{"job-a", "unicode-世界", "", strings.Repeat("x", 256)} {
		f.Add(seed, uint32(1))
	}
	f.Fuzz(func(t *testing.T, jobID string, attempt uint32) {
		if len(jobID) > 4096 {
			t.Skip()
		}
		base := executionReceiptEvidence{JobID: jobID, Status: cluster.JobCompleted, Attempt: int(attempt)}
		first, err := executionReceiptChecksum(executionReceiptSchema, base)
		if err != nil {
			t.Fatal(err)
		}
		mutated := base
		mutated.Attempt++
		second, err := executionReceiptChecksum(executionReceiptSchema, mutated)
		if err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Fatal("receipt evidence mutation retained the original checksum")
		}
	})
}

func receiptTestJob(t *testing.T) cluster.Job {
	t.Helper()
	artifactBytes := []byte("verified artifact bytes")
	artifactDigest := sha256.Sum256(artifactBytes)
	result, err := json.Marshal(bridge.Submission{
		Status: cluster.JobCompleted,
		Output: &bridge.Output{
			Mode: "text", Text: "private result text", Provider: "adapter",
			SelectedModel: "Adapter Test Model", SelectedReasoning: "high",
			Artifacts: []bridge.Artifact{{
				Name: "private-name.txt", MediaType: "text/plain", Size: len(artifactBytes),
				SHA256: hex.EncodeToString(artifactDigest[:]), DataBase64: base64.StdEncoding.EncodeToString(artifactBytes),
				URL: "https://private.example/artifact",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	assigned := created.Add(time.Second)
	started := assigned.Add(time.Second)
	finished := started.Add(2 * time.Second)
	return cluster.Job{
		ID: "job-receipt-1", OwnerSubject: "private-owner", TenantID: "private-tenant",
		Requirements: cluster.Requirements{
			Task: "generation", SessionID: "private-session", Provider: "adapter",
			AdapterProfile: "profile-one", Model: "Adapter Test Model", Reasoning: "high", Vision: true,
		},
		Payload: []byte(`{"prompt":"private prompt text"}`), Result: result,
		PolicyDecision: cluster.PolicyDecision{
			Schema: cluster.PolicyDecisionV1, Outcome: "allow", ReasonCodes: []string{cluster.PolicyCodeAllowed},
			RuleID: "tenant:sha256:opaque", PolicyFingerprint: "sha256:" + strings.Repeat("1", 64), EgressClass: "remote",
			ProviderClassification: "remote", CostEnforcement: "unbounded_unknown", EvaluatedAt: created,
		},
		Status: cluster.JobCompleted, Attempt: 1, MaxAttempts: 1, AssignedNode: "node-private-id",
		RoutingDecision: &cluster.RoutingDecision{
			ID: "route-private-id", JobID: "job-receipt-1", CandidateCount: 1,
			SelectedNodeID: "node-private-id", Candidates: []cluster.RoutingCandidateDecision{{NodeID: "node-private-id", Eligible: true}},
		},
		Usage: cluster.Usage{ComputeMS: 2000, QueueMS: 1000, CostStatus: cluster.CostUnknown, CostUnknownJobs: 1},
		Error: "private provider error detail", FailureCode: cluster.FailureWorkerExecution, CreatedAt: created, AssignedAt: assigned,
		StartedAt: started, FinishedAt: finished,
	}
}

func TestExecutionReceiptIsDeterministicAndContentMinimizing(t *testing.T) {
	job := receiptTestJob(t)
	first, err := buildExecutionReceipt(job)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildExecutionReceipt(job)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, _ := json.Marshal(first)
	secondRaw, _ := json.Marshal(second)
	if string(firstRaw) != string(secondRaw) {
		t.Fatal("the same durable job produced different receipt bytes")
	}
	for _, secret := range []string{"private prompt text", "private result text", "private-name.txt", "private.example", "private provider error detail", "private-session", "private-owner", "private-tenant"} {
		if strings.Contains(string(firstRaw), secret) {
			t.Fatalf("receipt leaked content %q", secret)
		}
	}
	if len(first.Evidence.Artifacts) != 1 || !first.Evidence.Artifacts[0].VerifiedBytes || first.Evidence.Artifacts[0].SHA256 == "" {
		t.Fatalf("verified artifact evidence was not retained: %#v", first.Evidence.Artifacts)
	}
	if first.Evidence.Payload.SHA256 == "" || first.Evidence.Result.SHA256 == "" || first.Evidence.RequirementsSHA256 == "" || first.Evidence.PolicyDecisionSHA256 == "" || first.Evidence.RoutingDecisionSHA256 == "" || first.Evidence.FailureSHA256 == "" {
		t.Fatalf("receipt is missing durable digests: %#v", first.Evidence)
	}
	if first.Evidence.ContractVersion != cluster.JobContractV1 {
		t.Fatalf("receipt contract version = %q", first.Evidence.ContractVersion)
	}
	if first.Evidence.FailureCode != cluster.FailureWorkerExecution {
		t.Fatalf("receipt failure code = %q", first.Evidence.FailureCode)
	}
	if err := validateExecutionReceiptChecksum(first); err != nil {
		t.Fatal(err)
	}
	first.Evidence.Status = cluster.JobFailed
	if err := validateExecutionReceiptChecksum(first); err == nil {
		t.Fatal("modified receipt evidence retained a valid checksum")
	}
}

func TestExecutionReceiptRejectsNonTerminalAndTamperedArtifact(t *testing.T) {
	job := receiptTestJob(t)
	job.Status = cluster.JobRunning
	if _, err := buildExecutionReceipt(job); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("non-terminal job received a receipt: %v", err)
	}

	job = receiptTestJob(t)
	var submission bridge.Submission
	if err := json.Unmarshal(job.Result, &submission); err != nil {
		t.Fatal(err)
	}
	submission.Output.Artifacts[0].SHA256 = strings.Repeat("0", sha256.Size*2)
	job.Result, _ = json.Marshal(submission)
	if _, err := buildExecutionReceipt(job); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tampered artifact received a receipt: %v", err)
	}
}

func TestExecutionReceiptRejectsUnverifiableTransferredBytes(t *testing.T) {
	for name, mutate := range map[string]func(*bridge.Artifact){
		"missing digest":   func(artifact *bridge.Artifact) { artifact.SHA256 = "" },
		"malformed digest": func(artifact *bridge.Artifact) { artifact.SHA256 = "sha256:not-valid" },
		"missing size":     func(artifact *bridge.Artifact) { artifact.Size = 0 },
		"wrong size":       func(artifact *bridge.Artifact) { artifact.Size++ },
	} {
		t.Run(name, func(t *testing.T) {
			job := receiptTestJob(t)
			var submission bridge.Submission
			if err := json.Unmarshal(job.Result, &submission); err != nil {
				t.Fatal(err)
			}
			mutate(&submission.Output.Artifacts[0])
			job.Result, _ = json.Marshal(submission)
			if _, err := buildExecutionReceipt(job); err == nil {
				t.Fatal("unverifiable transferred bytes received a verified execution receipt")
			}
		})
	}
}

func TestSignedExecutionReceiptBindsExactEvidenceAndExplicitTrustKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signingKey := receiptSigningKey{
		Schema: receiptSigningKeySchema, KeyID: "operator-2026", Issuer: "Example Operator", Algorithm: receiptSignatureAlgorithm,
		PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
	}
	trustKey := receiptTrustKey{
		Schema: receiptTrustKeySchema, KeyID: signingKey.KeyID, Issuer: signingKey.Issuer, Algorithm: receiptSignatureAlgorithm,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
	}
	unsigned, err := buildExecutionReceipt(receiptTestJob(t))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signExecutionReceipt(unsigned, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Schema != executionReceiptSignedSchema || signed.Signature == nil {
		t.Fatalf("signed receipt shape = %#v", signed)
	}
	if err := validateExecutionReceiptChecksum(signed); err != nil {
		t.Fatal(err)
	}
	if err := verifyExecutionReceiptSignature(signed, trustKey); err != nil {
		t.Fatal(err)
	}

	mutated := signed
	mutated.Evidence.JobID = "job-attacker-recomputed-checksum"
	mutated.Checksum, err = executionReceiptChecksum(mutated.Schema, mutated.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExecutionReceiptChecksum(mutated); err != nil {
		t.Fatalf("attacker-recomputed checksum should remain only an integrity check: %v", err)
	}
	if err := verifyExecutionReceiptSignature(mutated, trustKey); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("signature copied to different job evidence was accepted: %v", err)
	}

	wrongTrust := trustKey
	wrongTrust.KeyID = "different-key"
	if err := verifyExecutionReceiptSignature(signed, wrongTrust); err == nil || !strings.Contains(err.Error(), "explicitly trusted") {
		t.Fatalf("different explicit trust identity was accepted: %v", err)
	}
}

func TestReceiptSchemasKeepUnsignedAndSignedClaimsSeparate(t *testing.T) {
	receipt, err := buildExecutionReceipt(receiptTestJob(t))
	if err != nil {
		t.Fatal(err)
	}
	receipt.Signature = &executionReceiptSignature{Algorithm: receiptSignatureAlgorithm, KeyID: "key", Issuer: "Issuer", Value: "value"}
	raw, _ := json.Marshal(receipt)
	if _, err := decodeExecutionReceipt(raw); err == nil || !strings.Contains(err.Error(), "v1") {
		t.Fatalf("v1 receipt smuggled a signature: %v", err)
	}
	receipt.Signature = nil
	receipt.Schema = executionReceiptSignedSchema
	raw, _ = json.Marshal(receipt)
	if _, err := decodeExecutionReceipt(raw); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("v2 receipt omitted its signature: %v", err)
	}
}

func TestSignedReceiptDoesNotExposeSealedPayloadOrResult(t *testing.T) {
	job := receiptTestJob(t)
	job.Payload = nil
	job.Result = nil
	job.SealedPayload = &cluster.SealedEnvelope{Algorithm: "x25519-aes-256-gcm", EphemeralPublic: "payload-public-secret", Nonce: "payload-nonce-secret", Ciphertext: "payload-ciphertext-secret"}
	job.SealedResult = &cluster.SealedEnvelope{Algorithm: "x25519-aes-256-gcm", Nonce: "result-nonce-secret", Ciphertext: "result-ciphertext-secret"}
	receipt, err := buildExecutionReceipt(job)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"payload-public-secret", "payload-nonce-secret", "payload-ciphertext-secret", "result-nonce-secret", "result-ciphertext-secret"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("sealed receipt exposed %q: %s", secret, raw)
		}
	}
	if receipt.Evidence.Payload.Mode != "sealed" || receipt.Evidence.Result.Mode != "sealed" || receipt.Evidence.Payload.SHA256 == "" || receipt.Evidence.Result.SHA256 == "" {
		t.Fatalf("sealed envelope digests were not retained: %#v", receipt.Evidence)
	}
}

func TestClusterReceiptSignedExportVerifiesOfflineAfterRelayIsGone(t *testing.T) {
	job := receiptTestJob(t)
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job)
	}))

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = server.URL
	cfg.Cluster.ClientToken = "receipt-token"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(directory, "receipt-private.json")
	publicPath := filepath.Join(directory, "receipt-public.json")
	if err := clusterReceiptKeygenCommand([]string{"--private-out", privatePath, "--public-out", publicPath, "--key-id", "relay-2026", "--issuer", "Example Relay"}); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(directory, "signed-receipt.json")
	if err := clusterReceiptExportCommand([]string{"--config", configPath, "--signing-key", privatePath, "--out", receiptPath, job.ID}); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 {
		t.Fatalf("signed export read relay %d times, want one", reads.Load())
	}
	if err := clusterReceiptVerifyCommand([]string{"--config", configPath, "--file", receiptPath, "--trust-key", publicPath}); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 {
		t.Fatalf("online signed verification read relay %d times, want two total", reads.Load())
	}
	server.Close()
	if err := clusterReceiptVerifyCommand([]string{"--file", receiptPath, "--trust-key", publicPath, "--offline"}); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 {
		t.Fatal("offline verification contacted the retired relay")
	}
	if err := clusterReceiptVerifyCommand([]string{"--file", receiptPath, "--offline"}); err == nil || !strings.Contains(err.Error(), "--trust-key") {
		t.Fatalf("signed receipt trusted an embedded identity: %v", err)
	}

	unsigned, err := buildExecutionReceipt(job)
	if err != nil {
		t.Fatal(err)
	}
	unsignedRaw, _ := json.Marshal(unsigned)
	unsignedPath := filepath.Join(directory, "unsigned-receipt.json")
	if err := os.WriteFile(unsignedPath, unsignedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clusterReceiptVerifyCommand([]string{"--file", unsignedPath, "--offline"}); err == nil || !strings.Contains(err.Error(), "cannot be authenticated offline") {
		t.Fatalf("unsigned checksum was presented as offline authenticity: %v", err)
	}
}

func TestClusterReceiptExportAndVerifyAgainstRelay(t *testing.T) {
	job := receiptTestJob(t)
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cluster/jobs/"+job.ID || r.Header.Get("Authorization") != "Bearer receipt-token" {
			http.Error(w, "unexpected request", http.StatusForbidden)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job)
	}))
	defer server.Close()

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cluster.Relay.PublicURL = server.URL
	cfg.Cluster.ClientToken = "receipt-token"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	receiptPath := filepath.Join(directory, "receipt.json")
	if err := clusterReceiptShowCommand([]string{job.ID, "--config", configPath}, receiptPath); err != nil {
		t.Fatal(err)
	}
	if err := clusterReceiptVerifyCommand([]string{"--config", configPath, "--file", receiptPath}); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 {
		t.Fatalf("export and verify should each read the durable job once; got %d", reads.Load())
	}
	if err := clusterReceiptShowCommand([]string{"--config", configPath, job.ID}, receiptPath); err == nil || !strings.Contains(err.Error(), "without overwriting") {
		t.Fatalf("receipt export overwrote an existing file: %v", err)
	}

	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt executionReceiptEnvelope
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Evidence.Attempt++
	raw, _ = json.Marshal(receipt)
	if err := os.WriteFile(receiptPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clusterReceiptVerifyCommand([]string{"--config", configPath, "--file", receiptPath}); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("modified receipt passed verification: %v", err)
	}
}
