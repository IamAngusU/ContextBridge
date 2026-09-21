package verification

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerificationStatementLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	statement, key := signedFixture(t, now)
	result, err := Verify(statement, key, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.VerificationID != "cbv-example-001" || result.EvidenceBound != 1 {
		t.Fatalf("unexpected verification result: %#v", result)
	}

	statement.Payload.Subject.Version = "tampered"
	if _, err := Verify(statement, key, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered statement passed verification: %v", err)
	}
}

func TestVerificationRejectsExpiredAndExcessiveValidity(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	statement, key := signedFixture(t, now)
	if _, err := Verify(statement, key, now.Add(MaximumValidity)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired statement passed: %v", err)
	}

	statement, key = signedFixture(t, now)
	statement.Payload.ExpiresAt = now.Add(MaximumValidity + time.Second).Format(time.RFC3339)
	resignFixture(t, &statement)
	if _, err := Verify(statement, key, now); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("excessive validity passed: %v", err)
	}
}

func TestDecodeStatementIsClosedAndStrict(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	statement, _ := signedFixture(t, now)
	raw, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStatement(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("statement with trailing JSON passed")
	}
	withUnknown := strings.Replace(string(raw), `"schema":`, `"unknown":true,"schema":`, 1)
	if _, err := DecodeStatement([]byte(withUnknown)); err == nil {
		t.Fatal("statement with unknown field passed")
	}
}

func TestVerifyEvidenceFiles(t *testing.T) {
	directory := t.TempDir()
	content := []byte("bounded evidence\n")
	if err := os.WriteFile(filepath.Join(directory, "report.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	evidence := []Evidence{{Name: "report.json", SHA256: "sha256:" + hex.EncodeToString(digest[:])}}
	if count, err := VerifyEvidenceFiles(directory, evidence); err != nil || count != 1 {
		t.Fatalf("evidence verification failed: count=%d err=%v", count, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "report.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEvidenceFiles(directory, evidence); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed evidence passed: %v", err)
	}
}

func TestVerifyArtifactFile(t *testing.T) {
	content := []byte("exact product bytes")
	path := filepath.Join(t.TempDir(), "product.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	expected := "sha256:" + hex.EncodeToString(digest[:])
	if err := VerifyArtifactFile(path, expected); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifactFile(path, expected); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed artifact passed: %v", err)
	}
}

func TestVerificationRejectsWrongTrustKeyAndUnsortedScope(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	statement, key := signedFixture(t, now)
	key.KeyID = "different-key"
	if _, err := Verify(statement, key, now); err == nil || !strings.Contains(err.Error(), "key ID") {
		t.Fatalf("wrong key passed: %v", err)
	}

	statement, key = signedFixture(t, now)
	statement.Payload.Scopes = []string{"z.scope", "a.scope"}
	resignFixture(t, &statement)
	if _, err := Verify(statement, key, now); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("unsorted scope passed: %v", err)
	}
}

var fixturePrivateKey ed25519.PrivateKey

func signedFixture(t *testing.T, issued time.Time) (Statement, TrustKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixturePrivateKey = privateKey
	evidenceDigest := sha256.Sum256([]byte("bounded evidence\n"))
	artifactDigest := sha256.Sum256([]byte("product artifact"))
	statement := Statement{
		Schema: StatementSchema,
		Payload: Payload{
			Program:        ProgramV1,
			VerificationID: "cbv-example-001",
			Issuer:         "ContextBridge Project",
			Subject: Subject{
				Vendor:         "Example Vendor",
				Product:        "Example Worker",
				Version:        "1.2.3",
				ArtifactSHA256: "sha256:" + hex.EncodeToString(artifactDigest[:]),
			},
			TestedWith: TestedWith{
				ContextBridgeVersion: "v0.7.0",
				SourceCommit:         strings.Repeat("a", 40),
				Contracts:            []string{"contextbridge.job.v1", "contextbridge.worker-conformance.v1"},
			},
			Scopes:    []string{"contextbridge.worker-conformance.v1"},
			Evidence:  []Evidence{{Name: "report.json", SHA256: "sha256:" + hex.EncodeToString(evidenceDigest[:])}},
			IssuedAt:  issued.Format(time.RFC3339),
			ExpiresAt: issued.Add(MaximumValidity).Format(time.RFC3339),
		},
		Signature: Signature{Algorithm: Algorithm, KeyID: "contextbridge-test-2026"},
	}
	resignFixture(t, &statement)
	key := TrustKey{
		Schema:    TrustKeySchema,
		KeyID:     statement.Signature.KeyID,
		Issuer:    statement.Payload.Issuer,
		Algorithm: Algorithm,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
	}
	return statement, key
}

func resignFixture(t *testing.T, statement *Statement) {
	t.Helper()
	message, err := SigningMessage(statement.Schema, statement.Payload)
	if err != nil {
		t.Fatal(err)
	}
	statement.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(fixturePrivateKey, message))
}
