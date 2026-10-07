package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/strictjson"
)

type agentVerificationGate struct {
	Check string `json:"check"`
}

type agentVerificationReceipt struct {
	Schema           string `json:"schema"`
	Check            string `json:"check"`
	DefinitionSHA256 string `json:"definition_sha256"`
	InputSHA256      string `json:"input_sha256"`
	Status           string `json:"status"`
	Passed           *int   `json:"passed"`
	Total            *int   `json:"total"`
}

// No new jobs, loops, retries, or arbitrary branches: a gate can only skip the
// remaining, already-approved plan. All gates share one operator-owned check.
func validateAgentVerificationGates(steps []agentStep) error {
	var first *agentStep
	for i := range steps {
		step := &steps[i]
		if step.VerificationGate == nil {
			continue
		}
		if step.Provider != "adapter" || !agentStepIDPattern.MatchString(step.VerificationGate.Check) {
			return errors.New("verification_gate requires an adapter step and a valid check ID")
		}
		if first != nil && (first.Profile != step.Profile || first.VerificationGate.Check != step.VerificationGate.Check) {
			return errors.New("all verification gates must use the same profile and check")
		}
		first = step
	}
	if first != nil && steps[len(steps)-1].VerificationGate == nil {
		return errors.New("a gated plan must end with its verification gate")
	}
	return nil
}

func agentVerificationDefinition(cfg config.Config, step agentStep) (string, error) {
	if step.Provider != "adapter" || step.VerificationGate == nil {
		return "", errors.New("verification requires an explicitly gated adapter step")
	}
	profile, ok := cfg.AdapterProfiles[step.Profile]
	if !ok {
		return "", errors.New("verification profile is unavailable")
	}
	checks, err := config.AgentVerificationChecks(profile)
	if err != nil {
		return "", err
	}
	definition := checks[step.VerificationGate.Check]
	if definition == "" {
		return "", errors.New("verification check is not pinned by the operator profile")
	}
	return definition, nil
}

func agentVerificationStatus(cfg config.Config, step agentStep, submittedText string, output *bridge.Output) (string, error) {
	if step.VerificationGate == nil {
		return "", nil
	}
	definition, err := agentVerificationDefinition(cfg, step)
	if err != nil {
		return "", err
	}
	if output == nil || output.Mode != "json" || output.Text != "" || output.Error != "" || output.Truncated || len(output.JSON) == 0 || len(output.JSON) > 256<<10 {
		return "", errors.New("gate requires a complete JSON adapter result, not prose or partial evidence")
	}
	if err := strictjson.Validate(output.JSON); err != nil {
		return "", errors.New("gate result is ambiguous JSON")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(output.JSON, &result); err != nil || result == nil {
		return "", errors.New("gate result must be a JSON object")
	}
	var receipt agentVerificationReceipt
	if err := decodeAgentJSON(result["agent_verification"], &receipt); err != nil {
		return "", errors.New("missing or invalid agent_verification receipt")
	}
	inputDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(submittedText)))
	if receipt.Schema != "contextbridge.agent-verification.v1" || receipt.Check != step.VerificationGate.Check || receipt.DefinitionSHA256 != definition || receipt.InputSHA256 != inputDigest {
		return "", errors.New("verification receipt does not match the pinned check and exact submitted input")
	}
	if receipt.Passed == nil || receipt.Total == nil || *receipt.Total < 1 || *receipt.Total > 1_000_000 || *receipt.Passed < 0 || *receipt.Passed > *receipt.Total {
		return "", errors.New("verification receipt has invalid or missing counters")
	}
	switch receipt.Status {
	case "passed":
		if *receipt.Passed == *receipt.Total {
			return "passed", nil
		}
	case "failed":
		if *receipt.Passed < *receipt.Total {
			return "failed", nil
		}
	case "inconclusive":
		return "", errors.New("verification is inconclusive; stopped without replay or speculative repair")
	}
	return "", errors.New("verification receipt has inconsistent status or counters")
}

func agentVerificationPlannerInstructions(cfg config.Config, policy agentPolicy) string {
	available := map[string][]string{}
	for _, name := range policy.AllowedAdapterProfiles {
		checks, err := config.AgentVerificationChecks(cfg.AdapterProfiles[name])
		if err != nil || len(checks) == 0 {
			continue
		}
		for check := range checks {
			available[name] = append(available[name], check)
		}
		sort.Strings(available[name])
	}
	if len(available) == 0 {
		return ""
	}
	raw, _ := json.Marshal(available)
	return "\nOptional verification_gate: {\"check\":\"CHECK_ID\"} is allowed only on adapter steps using these operator-pinned profile/check IDs: " + string(raw) + ". " +
		"Use gates for bounded check/repair workflows. A passed gate ends the WHOLE run, skipping remaining steps. " +
		"A failed gate permits only the next approved step; inconclusive or malformed evidence stops the run. " +
		"A gated plan MUST end with a gate; every gate uses the SAME profile and check ID. " +
		"Never put a gate on a model step or invent check IDs. No promotion or deployment follows gate success."
}
