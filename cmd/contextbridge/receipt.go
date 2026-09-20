package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const (
	executionReceiptSchema       = "contextbridge.execution-receipt.v1"
	maximumExecutionReceiptBytes = 1 << 20
)

type executionReceiptEnvelope struct {
	Schema   string                   `json:"schema"`
	Evidence executionReceiptEvidence `json:"evidence"`
	Checksum string                   `json:"checksum_sha256"`
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
		return errors.New("usage: contextbridge cluster receipt show|export|verify [options]")
	}
	switch args[0] {
	case "show":
		return clusterReceiptShowCommand(args[1:], "")
	case "export":
		return clusterReceiptExportCommand(args[1:])
	case "verify":
		return clusterReceiptVerifyCommand(args[1:])
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
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 || strings.TrimSpace(*out) == "" {
		return errors.New("usage: contextbridge cluster receipt export [--config PATH] [--token TOKEN] --out RECEIPT.json JOB_ID")
	}
	return clusterReceiptShowCommand([]string{"--config", *path, "--token", *token, flags.Arg(0)}, *out)
}

func clusterReceiptVerifyCommand(args []string) error {
	flags := flag.NewFlagSet("cluster receipt verify", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer/observer/admin token")
	file := flags.String("file", "", "receipt JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*file) == "" {
		return errors.New("usage: contextbridge cluster receipt verify [--config PATH] [--token TOKEN] --file RECEIPT.json")
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
	current, err := fetchExecutionReceipt(context.Background(), *path, *token, stored.Evidence.JobID)
	if err != nil {
		return fmt.Errorf("verify receipt against relay: %w", err)
	}
	storedJSON, _ := json.Marshal(stored)
	currentJSON, _ := json.Marshal(current)
	if !bytes.Equal(storedJSON, currentJSON) {
		return errors.New("receipt does not match the current authenticated relay record")
	}
	fmt.Printf("Receipt matches the current relay record · %s · %s\n", stored.Evidence.JobID, stored.Evidence.Status)
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
	if receipt.Schema != executionReceiptSchema {
		return receipt, fmt.Errorf("unsupported receipt schema %q", receipt.Schema)
	}
	if receipt.Evidence.JobID == "" {
		return receipt, errors.New("receipt job ID is empty")
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
