package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const relayConformanceV1 = "contextbridge.relay-conformance.v1"

type relayConformanceReport struct {
	Schema     string                  `json:"schema"`
	Compatible bool                    `json:"compatible"`
	DurationMS int64                   `json:"duration_ms"`
	Checks     []relayConformanceCheck `json:"checks"`
}

type relayConformanceCheck struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

func clusterConformanceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge cluster conformance relay|worker [options]")
	}
	if args[0] == "worker" {
		return clusterWorkerConformanceCommand(args[1:])
	}
	if args[0] != "relay" {
		return errors.New("usage: contextbridge cluster conformance relay|worker [options]")
	}
	flags := flag.NewFlagSet("cluster conformance relay", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer/admin token")
	asJSON := flags.Bool("json", false, "print machine-readable conformance report")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: contextbridge cluster conformance relay [--config PATH] [--token TOKEN] [--json]")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	*token = clusterClientToken(cfg, *token)
	if *token == "" {
		return errors.New("relay conformance requires a producer or admin token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := runRelayConformance(ctx, clusterBaseURL(cfg), *token)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
	} else {
		state := "PASS"
		if !report.Compatible {
			state = "FAIL"
		}
		fmt.Printf("Relay conformance  [%s]  [%d checks]  [%d ms]  [no job submitted]\n", state, len(report.Checks), report.DurationMS)
		for _, check := range report.Checks {
			marker := "✓"
			if !check.Passed {
				marker = "!"
			}
			fmt.Printf("%s %-28s %s\n", marker, check.ID, check.Detail)
		}
	}
	if !report.Compatible {
		return errors.New("relay does not satisfy the advertised ContextBridge conformance boundary")
	}
	return nil
}

func runRelayConformance(ctx context.Context, baseURL, token string) (relayConformanceReport, error) {
	started := time.Now()
	report := relayConformanceReport{Schema: relayConformanceV1, Compatible: true}
	add := func(id string, passed bool, detail string) {
		report.Checks = append(report.Checks, relayConformanceCheck{ID: id, Passed: passed, Detail: detail})
		if !passed {
			report.Compatible = false
		}
	}

	var manifest cluster.ProtocolManifest
	if err := clusterGET(ctx, strings.TrimRight(baseURL, "/")+"/v1/cluster/protocol", token, &manifest); err != nil {
		return relayConformanceReport{}, fmt.Errorf("read relay protocol manifest: %w", err)
	}
	add("protocol.identity", manifest.Schema == cluster.ProtocolManifestV1 && manifest.WireProtocolVersion == cluster.ProtocolVersion,
		fmt.Sprintf("%s · wire v%d", cleanConformanceDetail(manifest.Schema), manifest.WireProtocolVersion))
	add("contract.version", containsExact(manifest.JobContractVersions, cluster.JobContractV1),
		strings.Join(manifest.JobContractVersions, ", "))
	requiredFeatures := []string{"assignment_fencing_v1", "durable_execution_policy_v1", "job_contract_dry_run", "stable_runtime_failure_codes", "worker_conformance_report_v1"}
	missingFeatures := missingExact(manifest.Features, requiredFeatures)
	add("protocol.features", len(missingFeatures) == 0, missingDetail(missingFeatures))
	catalogsValid := sortedUniqueNonEmpty(manifest.AdmissionErrorCodes) && sortedUniqueNonEmpty(manifest.RuntimeFailureCodes) &&
		containsExact(manifest.AdmissionErrorCodes, cluster.AdmissionCodeContractUnsupported) &&
		containsExact(manifest.AdmissionErrorCodes, cluster.AdmissionCodeRequestInvalidJSON)
	add("error.catalogs", catalogsValid,
		fmt.Sprintf("%d admission · %d runtime", len(manifest.AdmissionErrorCodes), len(manifest.RuntimeFailureCodes)))
	limitsValid := manifest.Limits.MaximumConfiguredJobPayloadBytes > 0 &&
		manifest.Limits.MaximumConfiguredJobPayloadBytes <= manifest.Limits.MaximumJobPayloadBytes &&
		manifest.Limits.MaximumJobResultBytes > 0 && manifest.Limits.MaximumWorkerConcurrency > 0
	add("protocol.limits", limitsValid,
		fmt.Sprintf("job %d · result %d · slots %d", manifest.Limits.MaximumConfiguredJobPayloadBytes, manifest.Limits.MaximumJobResultBytes, manifest.Limits.MaximumWorkerConcurrency))

	valid := []byte(`{"contract_version":"contextbridge.job.v1","requirements":{"task":"generation"},"payload":{"prompt":"CONFORMANCE-DRY-RUN-NO-SUBMIT"}}`)
	status, body, err := relayConformancePost(ctx, baseURL, token, valid)
	if err != nil {
		return relayConformanceReport{}, err
	}
	var validation cluster.ContractValidation
	validDryRun := status == http.StatusOK && json.Unmarshal(body, &validation) == nil && validation.Valid && validation.ContractVersion == cluster.JobContractV1 && validation.PolicyDecision.Schema == cluster.PolicyDecisionV1 && validation.PolicyDecision.Outcome == "allow"
	add("contract.valid_dry_run", validDryRun, fmt.Sprintf("HTTP %d · no job submitted", status))

	unsupported := []byte(`{"contract_version":"contextbridge.job.v999","requirements":{"task":"generation"},"payload":{}}`)
	status, body, err = relayConformancePost(ctx, baseURL, token, unsupported)
	if err != nil {
		return relayConformanceReport{}, err
	}
	code := conformanceErrorCode(body)
	add("contract.unsupported_code", status == http.StatusUnprocessableEntity && code == cluster.AdmissionCodeContractUnsupported,
		fmt.Sprintf("HTTP %d · %s", status, cleanConformanceDetail(code)))

	unknown := []byte(`{"requirements":{"task":"generation"},"payload":{},"unknown_conformance_field":true}`)
	status, body, err = relayConformancePost(ctx, baseURL, token, unknown)
	if err != nil {
		return relayConformanceReport{}, err
	}
	code = conformanceErrorCode(body)
	add("contract.closed_schema", status == http.StatusBadRequest && code == cluster.AdmissionCodeRequestInvalidJSON,
		fmt.Sprintf("HTTP %d · %s", status, cleanConformanceDetail(code)))

	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

func relayConformancePost(ctx context.Context, baseURL, token string, body []byte) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/cluster/contracts/validate", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("relay conformance request: %w", err)
	}
	defer response.Body.Close()
	raw, err := readClusterAPIResponse(response.Body)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, raw, nil
}

func conformanceErrorCode(raw []byte) string {
	var response struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &response)
	return response.Code
}

func sortedUniqueNonEmpty(values []string) bool {
	if len(values) == 0 || !sort.StringsAreSorted(values) {
		return false
	}
	for index, value := range values {
		if value == "" || index > 0 && value == values[index-1] {
			return false
		}
	}
	return true
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func missingExact(values, required []string) []string {
	missing := make([]string, 0)
	for _, wanted := range required {
		if !containsExact(values, wanted) {
			missing = append(missing, wanted)
		}
	}
	return missing
}

func missingDetail(missing []string) string {
	if len(missing) == 0 {
		return "required features advertised"
	}
	return "missing: " + strings.Join(missing, ", ")
}

func cleanConformanceDetail(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	if len(value) > 160 {
		value = value[:160]
	}
	return value
}
