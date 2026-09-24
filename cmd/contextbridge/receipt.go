package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const (
	executionReceiptSchema       = "contextbridge.execution-receipt.v1"
	executionReceiptSignedSchema = "contextbridge.execution-receipt.v2"
	receiptSigningKeySchema      = "contextbridge.receipt-signing-key.v1"
	receiptTrustKeySchema        = "contextbridge.receipt-trust-key.v1"
	receiptSignatureAlgorithm    = "ed25519"
	maximumExecutionReceiptBytes = 1 << 20
	maximumReceiptKeyBytes       = 64 << 10
)

var receiptKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type executionReceiptEnvelope struct {
	Schema    string                     `json:"schema"`
	Evidence  executionReceiptEvidence   `json:"evidence"`
	Checksum  string                     `json:"checksum_sha256"`
	Signature *executionReceiptSignature `json:"signature,omitempty"`
}

type executionReceiptSignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Issuer    string `json:"issuer"`
	Value     string `json:"value"`
}

type receiptSigningKey struct {
	Schema     string `json:"schema"`
	KeyID      string `json:"key_id"`
	Issuer     string `json:"issuer"`
	Algorithm  string `json:"algorithm"`
	PrivateKey string `json:"private_key"`
}

type receiptTrustKey struct {
	Schema    string `json:"schema"`
	KeyID     string `json:"key_id"`
	Issuer    string `json:"issuer"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

type executionReceiptEvidence struct {
	JobID                 string                    `json:"job_id"`
	ContractVersion       string                    `json:"contract_version"`
	Status                string                    `json:"status"`
	Requested             receiptRequestedSelection `json:"requested"`
	Observed              receiptObservedSelection  `json:"observed,omitempty"`
	RequirementsSHA256    string                    `json:"requirements_sha256"`
	PolicyDecisionSHA256  string                    `json:"policy_decision_sha256,omitempty"`
	Payload               receiptEnvelopeEvidence   `json:"payload"`
	Result                receiptEnvelopeEvidence   `json:"result"`
	AssignedNodeID        string                    `json:"assigned_node_id,omitempty"`
	RoutingDecisionSHA256 string                    `json:"routing_decision_sha256,omitempty"`
	Attempt               int                       `json:"attempt"`
	MaxAttempts           int                       `json:"max_attempts"`
	Usage                 cluster.Usage             `json:"usage"`
	Artifacts             []receiptArtifactEvidence `json:"artifacts,omitempty"`
	FailureCode           string                    `json:"failure_code,omitempty"`
	FailureSHA256         string                    `json:"failure_sha256,omitempty"`
	CreatedAt             time.Time                 `json:"created_at"`
	AssignedAt            *time.Time                `json:"assigned_at,omitempty"`
	StartedAt             *time.Time                `json:"started_at,omitempty"`
	FinishedAt            *time.Time                `json:"finished_at,omitempty"`
}

type receiptRequestedSelection struct {
	Task           string `json:"task,omitempty"`
	Provider       string `json:"provider,omitempty"`
	AdapterProfile string `json:"adapter_profile,omitempty"`
	Model          string `json:"model,omitempty"`
	Reasoning      string `json:"reasoning,omitempty"`
	Vision         bool   `json:"vision,omitempty"`
	Embedding      bool   `json:"embedding,omitempty"`
}

type receiptObservedSelection struct {
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

type receiptEnvelopeEvidence struct {
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
}

type receiptArtifactEvidence struct {
	Index         int    `json:"index"`
	MediaType     string `json:"media_type,omitempty"`
	Size          int    `json:"size_bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	VerifiedBytes bool   `json:"verified_bytes"`
	ReferenceOnly bool   `json:"reference_only,omitempty"`
}

func clusterReceiptCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge cluster receipt show|export|verify|keygen [options]")
	}
	switch args[0] {
	case "show":
		return clusterReceiptShowCommand(args[1:], "")
	case "export":
		return clusterReceiptExportCommand(args[1:])
	case "verify":
		return clusterReceiptVerifyCommand(args[1:])
	case "keygen":
		return clusterReceiptKeygenCommand(args[1:])
	default:
		return fmt.Errorf("unknown cluster receipt command %s", args[0])
	}
}

func clusterReceiptShowCommand(args []string, outputPath string) error {
	flags := flag.NewFlagSet("cluster receipt show", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer/observer/admin token")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: contextbridge cluster receipt show [--config PATH] [--token TOKEN] JOB_ID")
	}
	receipt, err := fetchExecutionReceipt(context.Background(), *path, *token, flags.Arg(0))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if outputPath == "" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	return writeNewReceipt(outputPath, raw)
}

func clusterReceiptExportCommand(args []string) error {
	flags := flag.NewFlagSet("cluster receipt export", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer/observer/admin token")
	out := flags.String("out", "", "new receipt JSON file")
	signingKeyPath := flags.String("signing-key", "", "optional operator receipt-signing private key")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 || strings.TrimSpace(*out) == "" {
		return errors.New("usage: contextbridge cluster receipt export [--config PATH] [--token TOKEN] [--signing-key PRIVATE.json] --out RECEIPT.json JOB_ID")
	}
	receipt, err := fetchExecutionReceipt(context.Background(), *path, *token, flags.Arg(0))
	if err != nil {
		return err
	}
	if strings.TrimSpace(*signingKeyPath) != "" {
		raw, err := readRegularFileBounded(*signingKeyPath, maximumReceiptKeyBytes)
		if err != nil {
			return fmt.Errorf("receipt signing key: %w", err)
		}
		key, err := decodeReceiptSigningKey(raw)
		if err != nil {
			return err
		}
		receipt, err = signExecutionReceipt(receipt, key)
		if err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writeNewReceipt(*out, append(raw, '\n'))
}

func clusterReceiptVerifyCommand(args []string) error {
	flags := flag.NewFlagSet("cluster receipt verify", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer/observer/admin token")
	file := flags.String("file", "", "receipt JSON file")
	trustKeyPath := flags.String("trust-key", "", "explicitly trusted receipt-signing public key")
	offline := flags.Bool("offline", false, "verify signature without contacting a relay")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*file) == "" {
		return errors.New("usage: contextbridge cluster receipt verify [--config PATH] [--token TOKEN] [--trust-key PUBLIC.json] [--offline] --file RECEIPT.json")
	}
	raw, err := readRegularFileBounded(*file, maximumExecutionReceiptBytes)
	if err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	stored, err := decodeExecutionReceipt(raw)
	if err != nil {
		return err
	}
	if err := validateExecutionReceiptChecksum(stored); err != nil {
		return err
	}
	signatureVerified := false
	if stored.Signature != nil {
		if strings.TrimSpace(*trustKeyPath) == "" {
			return errors.New("signed receipt requires an explicit --trust-key; no embedded or arbitrary signer is trusted automatically")
		}
		keyRaw, err := readRegularFileBounded(*trustKeyPath, maximumReceiptKeyBytes)
		if err != nil {
			return fmt.Errorf("receipt trust key: %w", err)
		}
		key, err := decodeReceiptTrustKey(keyRaw)
		if err != nil {
			return err
		}
		if err := verifyExecutionReceiptSignature(stored, key); err != nil {
			return err
		}
		signatureVerified = true
	} else if *offline {
		return errors.New("unsigned v1 receipt has checksum integrity only and cannot be authenticated offline; compare it with the live relay or export a signed v2 receipt")
	} else if strings.TrimSpace(*trustKeyPath) != "" {
		return errors.New("receipt is unsigned; --trust-key applies only to signed v2 receipts")
	}
	if *offline {
		fmt.Printf("Receipt signature valid · %s · %s · issuer %s · key %s\n", stored.Evidence.JobID, stored.Evidence.Status, stored.Signature.Issuer, stored.Signature.KeyID)
		return nil
	}
	current, err := fetchExecutionReceipt(context.Background(), *path, *token, stored.Evidence.JobID)
	if err != nil {
		return fmt.Errorf("verify receipt against relay: %w", err)
	}
	storedJSON, _ := json.Marshal(stored.Evidence)
	currentJSON, _ := json.Marshal(current.Evidence)
	if !bytes.Equal(storedJSON, currentJSON) {
		return errors.New("receipt does not match the current authenticated relay record")
	}
	if signatureVerified {
		fmt.Printf("Receipt signature valid and current relay record matches · %s · %s · issuer %s · key %s\n", stored.Evidence.JobID, stored.Evidence.Status, stored.Signature.Issuer, stored.Signature.KeyID)
	} else {
		fmt.Printf("Receipt checksum valid and current relay record matches · %s · %s · unsigned v1\n", stored.Evidence.JobID, stored.Evidence.Status)
	}
	return nil
}

func clusterReceiptKeygenCommand(args []string) error {
	flags := flag.NewFlagSet("cluster receipt keygen", flag.ContinueOnError)
	privateOut := flags.String("private-out", "", "new private receipt-signing key file")
	publicOut := flags.String("public-out", "", "new public receipt trust-key file")
	keyID := flags.String("key-id", "", "stable operator-controlled key identifier")
	issuer := flags.String("issuer", "", "operator or relay signer name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *privateOut == "" || *publicOut == "" || *keyID == "" || *issuer == "" {
		return errors.New("usage: contextbridge cluster receipt keygen --private-out PRIVATE.json --public-out PUBLIC.json --key-id ID --issuer NAME")
	}
	if sameReceiptOutputPath(*privateOut, *publicOut) {
		return errors.New("receipt private and public key outputs must be different paths")
	}
	for _, output := range []string{*privateOut, *publicOut} {
		if _, err := os.Lstat(output); err == nil {
			return fmt.Errorf("receipt key output already exists: %s", output)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check receipt key output %s: %w", output, err)
		}
	}
	if err := validateReceiptSignerIdentity(*keyID, *issuer); err != nil {
		return err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate receipt signing key: %w", err)
	}
	privateDocument := receiptSigningKey{
		Schema: receiptSigningKeySchema, KeyID: *keyID, Issuer: *issuer, Algorithm: receiptSignatureAlgorithm,
		PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
	}
	publicDocument := receiptTrustKey{
		Schema: receiptTrustKeySchema, KeyID: *keyID, Issuer: *issuer, Algorithm: receiptSignatureAlgorithm,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
	}
	// #nosec G117 -- this is the explicit local private-key export requested by
	// keygen. It is written once to a new mode-0600 file and never logged,
	// returned by an API, embedded in a receipt, or included in the public key.
	privateRaw, _ := json.MarshalIndent(privateDocument, "", "  ")
	publicRaw, _ := json.MarshalIndent(publicDocument, "", "  ")
	privateRaw, publicRaw = append(privateRaw, '\n'), append(publicRaw, '\n')
	if err := writeNewReceipt(*privateOut, privateRaw); err != nil {
		return fmt.Errorf("write receipt signing key: %w", err)
	}
	if err := writeNewReceipt(*publicOut, publicRaw); err != nil {
		return fmt.Errorf("write receipt trust key (private key was created successfully and was not deleted): %w", err)
	}
	fmt.Printf("Receipt signing identity created · issuer %s · key %s\n", *issuer, *keyID)
	fmt.Printf("  private · %s · keep secret\n", *privateOut)
	fmt.Printf("  public  · %s · distribute explicitly to verifiers\n", *publicOut)
	return nil
}

func fetchExecutionReceipt(ctx context.Context, configPath, token, jobID string) (executionReceiptEnvelope, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return executionReceiptEnvelope{}, errors.New("cluster receipt requires a job ID")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return executionReceiptEnvelope{}, err
	}
	token = clusterClientToken(cfg, token)
	if token == "" {
		return executionReceiptEnvelope{}, errors.New("cluster receipt requires a producer, observer, or admin token")
	}
	var job cluster.Job
	target := clusterBaseURL(cfg) + "/v1/cluster/jobs/" + url.PathEscape(jobID)
	if err := clusterGET(ctx, target, token, &job); err != nil {
		return executionReceiptEnvelope{}, err
	}
	return buildExecutionReceipt(job)
}

func buildExecutionReceipt(job cluster.Job) (executionReceiptEnvelope, error) {
	if job.ID == "" {
		return executionReceiptEnvelope{}, errors.New("receipt job ID is empty")
	}
	switch job.Status {
	case cluster.JobCompleted, cluster.JobFailed, cluster.JobCancelled:
	default:
		return executionReceiptEnvelope{}, fmt.Errorf("execution receipt requires a terminal job; current status is %s", job.Status)
	}
	contractVersion, err := cluster.NormalizeJobContractVersion(job.ContractVersion)
	if err != nil {
		return executionReceiptEnvelope{}, fmt.Errorf("receipt job contract: %w", err)
	}
	requirementsDigest, err := receiptDigest(job.Requirements)
	if err != nil {
		return executionReceiptEnvelope{}, err
	}
	payloadEvidence, err := receiptJobEnvelope(job.Payload, job.SealedPayload)
	if err != nil {
		return executionReceiptEnvelope{}, fmt.Errorf("payload evidence: %w", err)
	}
	resultEvidence, err := receiptJobEnvelope(job.Result, job.SealedResult)
	if err != nil {
		return executionReceiptEnvelope{}, fmt.Errorf("result evidence: %w", err)
	}
	evidence := executionReceiptEvidence{
		JobID: job.ID, ContractVersion: contractVersion, Status: job.Status,
		Requested: receiptRequestedSelection{
			Task: job.Requirements.Task, Provider: job.Requirements.Provider,
			AdapterProfile: job.Requirements.AdapterProfile, Model: job.Requirements.Model,
			Reasoning: job.Requirements.Reasoning, Vision: job.Requirements.Vision,
			Embedding: job.Requirements.Embedding,
		},
		RequirementsSHA256: requirementsDigest, Payload: payloadEvidence, Result: resultEvidence,
		AssignedNodeID: job.AssignedNode, Attempt: job.Attempt, MaxAttempts: job.MaxAttempts,
		Usage: job.Usage, CreatedAt: job.CreatedAt.UTC(), AssignedAt: receiptTime(job.AssignedAt),
		StartedAt: receiptTime(job.StartedAt), FinishedAt: receiptTime(job.FinishedAt),
		FailureCode: job.FailureCode,
	}
	if job.RoutingDecision != nil {
		evidence.RoutingDecisionSHA256, err = receiptDigest(job.RoutingDecision)
		if err != nil {
			return executionReceiptEnvelope{}, err
		}
	}
	if job.PolicyDecision.Schema != "" {
		evidence.PolicyDecisionSHA256, err = receiptDigest(job.PolicyDecision)
		if err != nil {
			return executionReceiptEnvelope{}, err
		}
	}
	if job.Error != "" {
		evidence.FailureSHA256 = receiptBytesDigest([]byte(job.Error))
	}
	if len(job.Result) > 0 {
		var submission bridge.Submission
		if err := json.Unmarshal(job.Result, &submission); err == nil && submission.Output != nil {
			evidence.Observed = receiptObservedSelection{
				Provider: submission.Output.Provider, Model: firstNonEmpty(submission.Output.SelectedModel, submission.Output.Model),
				Reasoning: submission.Output.SelectedReasoning,
			}
			evidence.Artifacts, err = receiptArtifacts(submission.Output.Artifacts)
			if err != nil {
				return executionReceiptEnvelope{}, err
			}
		}
	}
	receipt := executionReceiptEnvelope{Schema: executionReceiptSchema, Evidence: evidence}
	receipt.Checksum, err = executionReceiptChecksum(receipt.Schema, receipt.Evidence)
	return receipt, err
}

func receiptJobEnvelope(clear json.RawMessage, sealed *cluster.SealedEnvelope) (receiptEnvelopeEvidence, error) {
	if len(clear) > 0 && sealed != nil {
		return receiptEnvelopeEvidence{}, errors.New("cleartext and sealed values are both present")
	}
	if sealed != nil {
		digest, err := receiptDigest(sealed)
		return receiptEnvelopeEvidence{Mode: "sealed", SHA256: digest}, err
	}
	if len(clear) > 0 {
		digest, err := receiptCanonicalJSONDigest(clear)
		return receiptEnvelopeEvidence{Mode: "cleartext", SHA256: digest}, err
	}
	return receiptEnvelopeEvidence{Mode: "none"}, nil
}

func receiptArtifacts(artifacts []bridge.Artifact) ([]receiptArtifactEvidence, error) {
	result := make([]receiptArtifactEvidence, 0, len(artifacts))
	for index, artifact := range artifacts {
		evidence := receiptArtifactEvidence{Index: index + 1, MediaType: artifact.MediaType, Size: artifact.Size}
		declared := normalizeReceiptDigest(artifact.SHA256)
		if artifact.DataBase64 != "" {
			if declared == "" {
				return nil, fmt.Errorf("artifact %d transferred bytes without a valid declared SHA256", index+1)
			}
			if artifact.Size <= 0 {
				return nil, fmt.Errorf("artifact %d transferred bytes without a positive declared size", index+1)
			}
			decoded, err := base64.StdEncoding.DecodeString(artifact.DataBase64)
			if err != nil {
				return nil, fmt.Errorf("artifact %d has invalid base64: %w", index+1, err)
			}
			actual := receiptBytesDigest(decoded)
			if artifact.Size != len(decoded) {
				return nil, fmt.Errorf("artifact %d size does not match transferred bytes", index+1)
			}
			if declared != actual {
				return nil, fmt.Errorf("artifact %d digest does not match transferred bytes", index+1)
			}
			evidence.SHA256, evidence.VerifiedBytes = actual, true
		} else {
			evidence.SHA256 = declared
			evidence.ReferenceOnly = artifact.URL != ""
		}
		result = append(result, evidence)
	}
	return result, nil
}

func receiptCanonicalJSONDigest(raw []byte) (string, error) {
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", err
	}
	return receiptDigest(value)
}

func receiptDigest(value interface{}) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return receiptBytesDigest(raw), nil
}

func receiptBytesDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalizeReceiptDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "sha256:")))
	if len(value) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return "sha256:" + value
}

func executionReceiptChecksum(schema string, evidence executionReceiptEvidence) (string, error) {
	return receiptDigest(struct {
		Schema   string                   `json:"schema"`
		Evidence executionReceiptEvidence `json:"evidence"`
	}{Schema: schema, Evidence: evidence})
}

func executionReceiptSigningMessage(schema string, evidence executionReceiptEvidence) ([]byte, error) {
	raw, err := json.Marshal(struct {
		Schema   string                   `json:"schema"`
		Evidence executionReceiptEvidence `json:"evidence"`
	}{Schema: schema, Evidence: evidence})
	if err != nil {
		return nil, fmt.Errorf("encode receipt signing input: %w", err)
	}
	return append([]byte("ContextBridge Execution Receipt v2\n"), raw...), nil
}

func signExecutionReceipt(receipt executionReceiptEnvelope, key receiptSigningKey) (executionReceiptEnvelope, error) {
	privateKey, err := validateReceiptSigningKey(key)
	if err != nil {
		return receipt, err
	}
	if receipt.Schema != executionReceiptSchema || receipt.Signature != nil {
		return receipt, errors.New("only an unsigned v1 execution receipt can be signed")
	}
	receipt.Schema = executionReceiptSignedSchema
	receipt.Checksum, err = executionReceiptChecksum(receipt.Schema, receipt.Evidence)
	if err != nil {
		return receipt, err
	}
	message, err := executionReceiptSigningMessage(receipt.Schema, receipt.Evidence)
	if err != nil {
		return receipt, err
	}
	receipt.Signature = &executionReceiptSignature{
		Algorithm: receiptSignatureAlgorithm, KeyID: key.KeyID, Issuer: key.Issuer,
		Value: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message)),
	}
	return receipt, nil
}

func verifyExecutionReceiptSignature(receipt executionReceiptEnvelope, key receiptTrustKey) error {
	publicKey, err := validateReceiptTrustKey(key)
	if err != nil {
		return err
	}
	if receipt.Schema != executionReceiptSignedSchema || receipt.Signature == nil {
		return errors.New("receipt is not a signed v2 execution receipt")
	}
	signature := receipt.Signature
	if signature.Algorithm != receiptSignatureAlgorithm {
		return fmt.Errorf("unsupported receipt signature algorithm %q", signature.Algorithm)
	}
	if signature.KeyID != key.KeyID || signature.Issuer != key.Issuer {
		return errors.New("receipt signer does not match the explicitly trusted key")
	}
	rawSignature, err := base64.StdEncoding.DecodeString(signature.Value)
	if err != nil || len(rawSignature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(rawSignature) != signature.Value {
		return errors.New("receipt has an invalid Ed25519 signature encoding")
	}
	message, err := executionReceiptSigningMessage(receipt.Schema, receipt.Evidence)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, message, rawSignature) {
		return errors.New("receipt signature is invalid for the explicitly trusted key")
	}
	return nil
}

func decodeReceiptSigningKey(raw []byte) (receiptSigningKey, error) {
	var key receiptSigningKey
	if err := decodeReceiptClosedJSON(raw, &key); err != nil {
		return key, fmt.Errorf("decode receipt signing key: %w", err)
	}
	if _, err := validateReceiptSigningKey(key); err != nil {
		return key, err
	}
	return key, nil
}

func decodeReceiptTrustKey(raw []byte) (receiptTrustKey, error) {
	var key receiptTrustKey
	if err := decodeReceiptClosedJSON(raw, &key); err != nil {
		return key, fmt.Errorf("decode receipt trust key: %w", err)
	}
	if _, err := validateReceiptTrustKey(key); err != nil {
		return key, err
	}
	return key, nil
}

func validateReceiptSigningKey(key receiptSigningKey) (ed25519.PrivateKey, error) {
	if key.Schema != receiptSigningKeySchema {
		return nil, fmt.Errorf("unsupported receipt signing-key schema %q", key.Schema)
	}
	if key.Algorithm != receiptSignatureAlgorithm {
		return nil, fmt.Errorf("unsupported receipt signing-key algorithm %q", key.Algorithm)
	}
	if err := validateReceiptSignerIdentity(key.KeyID, key.Issuer); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(key.PrivateKey)
	if err != nil || len(raw) != ed25519.PrivateKeySize || base64.StdEncoding.EncodeToString(raw) != key.PrivateKey {
		return nil, errors.New("receipt signing key has an invalid Ed25519 private key encoding")
	}
	if !bytes.Equal(ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize]), raw) {
		return nil, errors.New("receipt signing key contains inconsistent Ed25519 private key material")
	}
	return ed25519.PrivateKey(raw), nil
}

func validateReceiptTrustKey(key receiptTrustKey) (ed25519.PublicKey, error) {
	if key.Schema != receiptTrustKeySchema {
		return nil, fmt.Errorf("unsupported receipt trust-key schema %q", key.Schema)
	}
	if key.Algorithm != receiptSignatureAlgorithm {
		return nil, fmt.Errorf("unsupported receipt trust-key algorithm %q", key.Algorithm)
	}
	if err := validateReceiptSignerIdentity(key.KeyID, key.Issuer); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(key.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(raw) != key.PublicKey {
		return nil, errors.New("receipt trust key has an invalid Ed25519 public key encoding")
	}
	return ed25519.PublicKey(raw), nil
}

func validateReceiptSignerIdentity(keyID, issuer string) error {
	if !receiptKeyIDPattern.MatchString(keyID) {
		return errors.New("receipt signer key ID is invalid")
	}
	if issuer != strings.TrimSpace(issuer) || issuer == "" || len(issuer) > 200 || !utf8.ValidString(issuer) || strings.IndexFunc(issuer, unicode.IsControl) >= 0 {
		return errors.New("receipt signer issuer must be non-empty, single-line UTF-8 no longer than 200 bytes")
	}
	return nil
}

func decodeReceiptClosedJSON(raw []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func decodeExecutionReceipt(raw []byte) (executionReceiptEnvelope, error) {
	var receipt executionReceiptEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, fmt.Errorf("decode receipt: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return receipt, fmt.Errorf("decode receipt: %w", err)
	}
	if receipt.Schema != executionReceiptSchema && receipt.Schema != executionReceiptSignedSchema {
		return receipt, fmt.Errorf("unsupported receipt schema %q", receipt.Schema)
	}
	if receipt.Evidence.JobID == "" {
		return receipt, errors.New("receipt job ID is empty")
	}
	if receipt.Schema == executionReceiptSchema && receipt.Signature != nil {
		return receipt, errors.New("unsigned v1 receipt must not contain a signature")
	}
	if receipt.Schema == executionReceiptSignedSchema && receipt.Signature == nil {
		return receipt, errors.New("signed v2 receipt is missing its signature")
	}
	return receipt, nil
}

func validateExecutionReceiptChecksum(receipt executionReceiptEnvelope) error {
	expected, err := executionReceiptChecksum(receipt.Schema, receipt.Evidence)
	if err != nil {
		return err
	}
	if receipt.Checksum != expected {
		return errors.New("receipt checksum does not match its evidence")
	}
	return nil
}

func receiptTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sameReceiptOutputPath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func writeNewReceipt(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create receipt without overwriting: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	return file.Sync()
}
