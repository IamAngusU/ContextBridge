package config

import (
	"encoding/json"
	"errors"
	"regexp"
)

// AdapterAgentVerificationChecksOption is an explicit operator trust decision,
// not a capability an adapter/model may grant itself. Definitions stay local;
// only safe check identifiers are exposed to the planner.
const AdapterAgentVerificationChecksOption = "agent_verification_checks"

var agentCheckName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var agentCheckDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func AgentVerificationChecks(profile AdapterProfile) (map[string]string, error) {
	value, present := profile.Options[AdapterAgentVerificationChecksOption]
	if !present {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	var checks map[string]string
	if err != nil || json.Unmarshal(raw, &checks) != nil || len(checks) < 1 || len(checks) > 8 {
		return nil, errors.New("agent_verification_checks requires 1..8 check IDs pinned to sha256 digests")
	}
	for name, digest := range checks {
		if !agentCheckName.MatchString(name) || !agentCheckDigest.MatchString(digest) {
			return nil, errors.New("agent_verification_checks contains an invalid check ID or definition digest")
		}
	}
	contract, _ := profile.Options[AdapterAgentInstructionContractOption].(string)
	if contract == "" {
		return nil, errors.New("agent_verification_checks requires an agent_instruction_contract")
	}
	return checks, nil
}
