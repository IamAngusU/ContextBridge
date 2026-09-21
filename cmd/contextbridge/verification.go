package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	verificationpkg "github.com/IamAngusU/ContextBridge/internal/verification"
)

type verificationCLIResult struct {
	Schema          string                  `json:"schema"`
	Valid           bool                    `json:"valid"`
	VerificationID  string                  `json:"verification_id,omitempty"`
	Issuer          string                  `json:"issuer,omitempty"`
	Subject         verificationpkg.Subject `json:"subject,omitempty"`
	Scopes          []string                `json:"scopes,omitempty"`
	IssuedAt        string                  `json:"issued_at,omitempty"`
	ExpiresAt       string                  `json:"expires_at,omitempty"`
	EvidenceBound   int                     `json:"evidence_bound"`
	EvidenceChecked int                     `json:"evidence_checked"`
	EvidenceStatus  string                  `json:"evidence_status,omitempty"`
	ArtifactChecked bool                    `json:"artifact_checked"`
	ArtifactStatus  string                  `json:"artifact_status,omitempty"`
	Error           string                  `json:"error,omitempty"`
}

func verificationCommand(args []string) error {
	if len(args) == 0 || args[0] != "verify" {
		return errors.New("usage: contextbridge verification verify --file STATEMENT.json --trust-key KEY.json [--artifact FILE] [--require-artifact] [--evidence-dir DIR] [--require-evidence] [--json]")
	}
	flags := flag.NewFlagSet("verification verify", flag.ContinueOnError)
	file := flags.String("file", "", "signed verification statement")
	trustKey := flags.String("trust-key", "", "trusted issuer public key")
	artifact := flags.String("artifact", "", "subject artifact to re-hash")
	requireArtifact := flags.Bool("require-artifact", false, "require and re-hash the subject artifact")
	evidenceDir := flags.String("evidence-dir", "", "directory containing signed evidence files")
	requireEvidence := flags.Bool("require-evidence", false, "require and re-hash every signed evidence file")
	asJSON := flags.Bool("json", false, "print machine-readable verification result")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *file == "" || *trustKey == "" {
		return errors.New("usage: contextbridge verification verify --file STATEMENT.json --trust-key KEY.json [--artifact FILE] [--require-artifact] [--evidence-dir DIR] [--require-evidence] [--json]")
	}
	if *requireArtifact && *artifact == "" {
		return errors.New("--require-artifact requires --artifact")
	}
	if *requireEvidence && *evidenceDir == "" {
		return errors.New("--require-evidence requires --evidence-dir")
	}
	statementRaw, err := readRegularFileBounded(*file, verificationpkg.MaximumStatementBytes)
	if err != nil {
		return fmt.Errorf("read verification statement: %w", err)
	}
	keyRaw, err := readRegularFileBounded(*trustKey, verificationpkg.MaximumTrustKeyBytes)
	if err != nil {
		return fmt.Errorf("read verification trust key: %w", err)
	}
	statement, err := verificationpkg.DecodeStatement(statementRaw)
	if err != nil {
		return printVerificationFailure(*asJSON, err)
	}
	key, err := verificationpkg.DecodeTrustKey(keyRaw)
	if err != nil {
		return printVerificationFailure(*asJSON, err)
	}
	verified, err := verificationpkg.Verify(statement, key, time.Now())
	if err != nil {
		return printVerificationFailure(*asJSON, err)
	}
	checked := 0
	evidenceStatus := "bound_not_rechecked"
	artifactChecked := false
	artifactStatus := "bound_not_rechecked"
	if *artifact != "" {
		if err := verificationpkg.VerifyArtifactFile(*artifact, statement.Payload.Subject.ArtifactSHA256); err != nil {
			return printVerificationFailure(*asJSON, err)
		}
		artifactChecked = true
		artifactStatus = "verified"
	}
	if *evidenceDir != "" {
		checked, err = verificationpkg.VerifyEvidenceFiles(*evidenceDir, statement.Payload.Evidence)
		if err != nil {
			return printVerificationFailure(*asJSON, err)
		}
		evidenceStatus = "verified"
	}
	result := verificationCLIResult{
		Schema:          "contextbridge.verification-result.v1",
		Valid:           true,
		VerificationID:  verified.VerificationID,
		Issuer:          verified.Issuer,
		Subject:         verified.Subject,
		Scopes:          verified.Scopes,
		IssuedAt:        verified.IssuedAt.Format(time.RFC3339),
		ExpiresAt:       verified.ExpiresAt.Format(time.RFC3339),
		EvidenceBound:   verified.EvidenceBound,
		EvidenceChecked: checked,
		EvidenceStatus:  evidenceStatus,
		ArtifactChecked: artifactChecked,
		ArtifactStatus:  artifactStatus,
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("ContextBridge verification  [VALID]  [%s]\n", result.VerificationID)
	fmt.Printf("  issuer    · %s\n", result.Issuer)
	fmt.Printf("  subject   · %s · %s · %s\n", result.Subject.Vendor, result.Subject.Product, result.Subject.Version)
	fmt.Printf("  artifact  · %s\n", result.Subject.ArtifactSHA256)
	fmt.Printf("  artifact  · %s\n", result.ArtifactStatus)
	fmt.Printf("  valid     · %s through %s\n", result.IssuedAt, result.ExpiresAt)
	fmt.Printf("  evidence  · %d bound · %d rechecked · %s\n", result.EvidenceBound, result.EvidenceChecked, result.EvidenceStatus)
	return nil
}

func printVerificationFailure(asJSON bool, err error) error {
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(verificationCLIResult{
			Schema: "contextbridge.verification-result.v1",
			Valid:  false,
			Error:  err.Error(),
		})
	}
	return err
}
