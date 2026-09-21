package verification

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	StatementSchema = "contextbridge.verification-statement.v1"
	TrustKeySchema  = "contextbridge.verification-trust-key.v1"
	ProgramV1       = "contextbridge-verified.v1"
	Algorithm       = "ed25519"

	MaximumStatementBytes = 1 << 20
	MaximumTrustKeyBytes  = 64 << 10
	MaximumEvidenceBytes  = 64 << 20
	MaximumArtifactBytes  = 8 << 30
	MaximumValidity       = 366 * 24 * time.Hour
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	fileNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	hexCommitPattern  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type Statement struct {
	Schema    string    `json:"schema"`
	Payload   Payload   `json:"payload"`
	Signature Signature `json:"signature"`
}

type Payload struct {
	Program        string     `json:"program"`
	VerificationID string     `json:"verification_id"`
	Issuer         string     `json:"issuer"`
	Subject        Subject    `json:"subject"`
	TestedWith     TestedWith `json:"tested_with"`
	Scopes         []string   `json:"scopes"`
	Evidence       []Evidence `json:"evidence"`
	IssuedAt       string     `json:"issued_at"`
	ExpiresAt      string     `json:"expires_at"`
}

type Subject struct {
	Vendor         string `json:"vendor"`
	Product        string `json:"product"`
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

type TestedWith struct {
	ContextBridgeVersion string   `json:"contextbridge_version"`
	SourceCommit         string   `json:"source_commit"`
	Contracts            []string `json:"contracts"`
}

type Evidence struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

type TrustKey struct {
	Schema    string `json:"schema"`
	KeyID     string `json:"key_id"`
	Issuer    string `json:"issuer"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

type Result struct {
	VerificationID string
	Issuer         string
	Subject        Subject
	Scopes         []string
	IssuedAt       time.Time
	ExpiresAt      time.Time
	EvidenceBound  int
}

func DecodeStatement(raw []byte) (Statement, error) {
	var statement Statement
	if err := decodeClosedJSON(raw, &statement); err != nil {
		return statement, fmt.Errorf("decode verification statement: %w", err)
	}
	if statement.Schema != StatementSchema {
		return statement, fmt.Errorf("unsupported verification statement schema %q", statement.Schema)
	}
	return statement, nil
}

func DecodeTrustKey(raw []byte) (TrustKey, error) {
	var key TrustKey
	if err := decodeClosedJSON(raw, &key); err != nil {
		return key, fmt.Errorf("decode verification trust key: %w", err)
	}
	if key.Schema != TrustKeySchema {
		return key, fmt.Errorf("unsupported verification trust-key schema %q", key.Schema)
	}
	return key, nil
}

func Verify(statement Statement, key TrustKey, now time.Time) (Result, error) {
	issuedAt, expiresAt, err := validateStatement(statement)
	if err != nil {
		return Result{}, err
	}
	publicKey, err := validateTrustKey(key)
	if err != nil {
		return Result{}, err
	}
	if statement.Signature.KeyID != key.KeyID {
		return Result{}, errors.New("verification statement key ID does not match the trusted key")
	}
	if statement.Payload.Issuer != key.Issuer {
		return Result{}, errors.New("verification statement issuer does not match the trusted key")
	}
	signature, err := base64.StdEncoding.DecodeString(statement.Signature.Value)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != statement.Signature.Value {
		return Result{}, errors.New("verification statement has an invalid Ed25519 signature encoding")
	}
	message, err := SigningMessage(statement.Schema, statement.Payload)
	if err != nil {
		return Result{}, err
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return Result{}, errors.New("verification statement signature is invalid")
	}

	now = now.UTC()
	if now.Before(issuedAt.Add(-10 * time.Minute)) {
		return Result{}, errors.New("verification statement is not valid yet")
	}
	if !now.Before(expiresAt) {
		return Result{}, errors.New("verification statement has expired")
	}
	return Result{
		VerificationID: statement.Payload.VerificationID,
		Issuer:         statement.Payload.Issuer,
		Subject:        statement.Payload.Subject,
		Scopes:         append([]string(nil), statement.Payload.Scopes...),
		IssuedAt:       issuedAt,
		ExpiresAt:      expiresAt,
		EvidenceBound:  len(statement.Payload.Evidence),
	}, nil
}

func SigningMessage(schema string, payload Payload) ([]byte, error) {
	envelope := struct {
		Schema  string  `json:"schema"`
		Payload Payload `json:"payload"`
	}{Schema: schema, Payload: payload}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode verification signing input: %w", err)
	}
	return append([]byte("ContextBridge Verification Statement v1\n"), raw...), nil
}

func VerifyEvidenceFiles(directory string, evidence []Evidence) (int, error) {
	if strings.TrimSpace(directory) == "" {
		return 0, errors.New("evidence directory is required")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return 0, fmt.Errorf("open evidence directory: %w", err)
	}
	defer root.Close()
	for _, item := range evidence {
		if !fileNamePattern.MatchString(item.Name) || filepath.Base(item.Name) != item.Name {
			return 0, fmt.Errorf("unsafe evidence file name %q", item.Name)
		}
		file, err := root.Open(item.Name)
		if err != nil {
			return 0, fmt.Errorf("open evidence %q: %w", item.Name, err)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return 0, fmt.Errorf("stat evidence %q: %w", item.Name, statErr)
		}
		if !info.Mode().IsRegular() || info.Size() > MaximumEvidenceBytes {
			_ = file.Close()
			return 0, fmt.Errorf("evidence %q must be a regular file no larger than %d bytes", item.Name, MaximumEvidenceBytes)
		}
		hash := sha256.New()
		written, copyErr := io.Copy(hash, io.LimitReader(file, MaximumEvidenceBytes+1))
		closeErr := file.Close()
		if copyErr != nil {
			return 0, fmt.Errorf("hash evidence %q: %w", item.Name, copyErr)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("close evidence %q: %w", item.Name, closeErr)
		}
		if written > MaximumEvidenceBytes {
			return 0, fmt.Errorf("evidence %q exceeds %d bytes", item.Name, MaximumEvidenceBytes)
		}
		actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if actual != strings.ToLower(item.SHA256) {
			return 0, fmt.Errorf("evidence %q digest does not match the signed statement", item.Name)
		}
	}
	return len(evidence), nil
}

func VerifyArtifactFile(path, expectedDigest string) error {
	if !validDigest(expectedDigest) {
		return errors.New("expected artifact SHA-256 is invalid")
	}
	// #nosec G703 -- path is an explicit operator-selected CLI input and is
	// bounded, hashed, and required to be a regular file below.
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open subject artifact: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat subject artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > MaximumArtifactBytes {
		return fmt.Errorf("subject artifact must be a regular file no larger than %d bytes", int64(MaximumArtifactBytes))
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, MaximumArtifactBytes+1))
	if err != nil {
		return fmt.Errorf("hash subject artifact: %w", err)
	}
	if written > MaximumArtifactBytes {
		return fmt.Errorf("subject artifact exceeds %d bytes", int64(MaximumArtifactBytes))
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != expectedDigest {
		return errors.New("subject artifact digest does not match the signed statement")
	}
	return nil
}

func validateStatement(statement Statement) (time.Time, time.Time, error) {
	if statement.Payload.Program != ProgramV1 {
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported verification program %q", statement.Payload.Program)
	}
	if !identifierPattern.MatchString(statement.Payload.VerificationID) {
		return time.Time{}, time.Time{}, errors.New("verification ID is invalid")
	}
	if err := boundedText("issuer", statement.Payload.Issuer, 200); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if err := boundedText("subject vendor", statement.Payload.Subject.Vendor, 200); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if err := boundedText("subject product", statement.Payload.Subject.Product, 200); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if err := boundedText("subject version", statement.Payload.Subject.Version, 100); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !validDigest(statement.Payload.Subject.ArtifactSHA256) {
		return time.Time{}, time.Time{}, errors.New("subject artifact SHA-256 is invalid")
	}
	if err := boundedText("tested ContextBridge version", statement.Payload.TestedWith.ContextBridgeVersion, 100); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !hexCommitPattern.MatchString(statement.Payload.TestedWith.SourceCommit) {
		return time.Time{}, time.Time{}, errors.New("tested ContextBridge source commit must be a lowercase 40- or 64-character hexadecimal digest")
	}
	if err := validateSortedIdentifiers("contract", statement.Payload.TestedWith.Contracts, 1, 32); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if err := validateSortedIdentifiers("scope", statement.Payload.Scopes, 1, 32); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if len(statement.Payload.Evidence) == 0 || len(statement.Payload.Evidence) > 64 {
		return time.Time{}, time.Time{}, errors.New("verification statement must bind between 1 and 64 evidence files")
	}
	lastName := ""
	for _, evidence := range statement.Payload.Evidence {
		if !fileNamePattern.MatchString(evidence.Name) || filepath.Base(evidence.Name) != evidence.Name {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid evidence file name %q", evidence.Name)
		}
		if evidence.Name <= lastName {
			return time.Time{}, time.Time{}, errors.New("evidence files must be unique and sorted by name")
		}
		if !validDigest(evidence.SHA256) {
			return time.Time{}, time.Time{}, fmt.Errorf("evidence %q has an invalid SHA-256", evidence.Name)
		}
		lastName = evidence.Name
	}
	issuedAt, err := time.Parse(time.RFC3339, statement.Payload.IssuedAt)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("issued_at must be an RFC3339 timestamp")
	}
	expiresAt, err := time.Parse(time.RFC3339, statement.Payload.ExpiresAt)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("expires_at must be an RFC3339 timestamp")
	}
	issuedAt = issuedAt.UTC()
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(issuedAt) {
		return time.Time{}, time.Time{}, errors.New("verification expiry must be after issuance")
	}
	if expiresAt.Sub(issuedAt) > MaximumValidity {
		return time.Time{}, time.Time{}, fmt.Errorf("verification validity exceeds %s", MaximumValidity)
	}
	if statement.Signature.Algorithm != Algorithm {
		return time.Time{}, time.Time{}, fmt.Errorf("unsupported verification signature algorithm %q", statement.Signature.Algorithm)
	}
	if !identifierPattern.MatchString(statement.Signature.KeyID) {
		return time.Time{}, time.Time{}, errors.New("verification signature key ID is invalid")
	}
	return issuedAt, expiresAt, nil
}

func validateTrustKey(key TrustKey) (ed25519.PublicKey, error) {
	if key.Algorithm != Algorithm {
		return nil, fmt.Errorf("unsupported trust-key algorithm %q", key.Algorithm)
	}
	if !identifierPattern.MatchString(key.KeyID) {
		return nil, errors.New("verification trust-key ID is invalid")
	}
	if err := boundedText("verification trust-key issuer", key.Issuer, 200); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(key.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(raw) != key.PublicKey {
		return nil, errors.New("verification trust key has an invalid Ed25519 public key encoding")
	}
	return ed25519.PublicKey(raw), nil
}

func validateSortedIdentifiers(label string, values []string, minimum, maximum int) error {
	if len(values) < minimum || len(values) > maximum {
		return fmt.Errorf("verification %ss must contain between %d and %d entries", label, minimum, maximum)
	}
	if !sort.StringsAreSorted(values) {
		return fmt.Errorf("verification %ss must be sorted", label)
	}
	last := ""
	for _, value := range values {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("invalid verification %s %q", label, value)
		}
		if value == last {
			return fmt.Errorf("duplicate verification %s %q", label, value)
		}
		last = value
	}
	return nil
}

func boundedText(label, value string, maximum int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || len(value) > maximum || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must be non-empty, single-line text no longer than %d bytes", label, maximum)
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && value == strings.ToLower(value)
}

func decodeClosedJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains trailing data")
		}
		return err
	}
	return nil
}
