package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const e2eeAADScheme = "contextbridge.cluster.e2ee.v2"

// AssignmentRequest carries every producer-controlled namespace value that
// must be fixed before an E2EE payload is encrypted. The relay supplies the
// authenticated owner subject in the returned Assignment.
type AssignmentRequest struct {
	TenantID     string       `json:"tenant_id,omitempty"`
	Requirements Requirements `json:"requirements"`
}

// EncryptionContext is public metadata authenticated by the sealed job and
// result. It intentionally excludes scheduling-only fields such as priority
// and timestamps, but includes every outer field used by worker execution.
type EncryptionContext struct {
	JobID          string         `json:"job_id"`
	NodeID         string         `json:"node_id"`
	Attempt        int            `json:"attempt"`
	OwnerSubject   string         `json:"owner_subject"`
	TenantID       string         `json:"tenant_id,omitempty"`
	Requirements   Requirements   `json:"requirements"`
	PolicyDecision PolicyDecision `json:"policy_decision"`
}

func (assignment Assignment) EncryptionContext() EncryptionContext {
	return EncryptionContext{
		JobID:          assignment.JobID,
		NodeID:         assignment.NodeID,
		Attempt:        assignment.Attempt,
		OwnerSubject:   assignment.OwnerSubject,
		TenantID:       assignment.TenantID,
		Requirements:   assignment.Requirements,
		PolicyDecision: assignment.PolicyDecision,
	}
}

func (job Job) EncryptionContext() EncryptionContext {
	return EncryptionContext{
		JobID:          job.ID,
		NodeID:         job.AssignedNode,
		Attempt:        job.Attempt,
		OwnerSubject:   job.OwnerSubject,
		TenantID:       job.TenantID,
		Requirements:   job.Requirements,
		PolicyDecision: job.PolicyDecision,
	}
}

// EncryptionContextForNode additionally requires that the outer assignment
// names the authenticated worker that received it.
func (job Job) EncryptionContextForNode(nodeID string) (EncryptionContext, error) {
	if nodeID == "" || job.AssignedNode != nodeID {
		return EncryptionContext{}, errors.New("encrypted job is assigned to another node")
	}
	context := job.EncryptionContext()
	if err := context.Validate(); err != nil {
		return EncryptionContext{}, err
	}
	return context, nil
}

func (context EncryptionContext) Validate() error {
	if context.JobID == "" || context.NodeID == "" || context.Attempt <= 0 || context.OwnerSubject == "" {
		return errors.New("encrypted job context requires job, node, assignment attempt, and authenticated owner identities")
	}
	if err := context.PolicyDecision.ValidateAllowed(); err != nil {
		return errors.New("encrypted job context has an invalid policy decision: " + err.Error())
	}
	return nil
}

func (context EncryptionContext) Equal(other EncryptionContext) bool {
	return bytes.Equal(canonicalEncryptionContext(context), canonicalEncryptionContext(other))
}

func canonicalEncryptionContext(context EncryptionContext) []byte {
	encoded, _ := json.Marshal(context) // EncryptionContext contains only JSON-safe values.
	return encoded
}

func encryptionAAD(purpose string, context EncryptionContext) []byte {
	envelope := struct {
		Scheme  string            `json:"scheme"`
		Purpose string            `json:"purpose"`
		Context EncryptionContext `json:"context"`
	}{Scheme: e2eeAADScheme, Purpose: purpose, Context: context}
	encoded, _ := json.Marshal(envelope) // The envelope contains only JSON-safe values.
	return encoded
}

func JobAAD(context EncryptionContext) []byte    { return encryptionAAD("job", context) }
func ResultAAD(context EncryptionContext) []byte { return encryptionAAD("result", context) }

// ValidateAssignmentResponse prevents a producer from encrypting after an
// assignment response changed an explicitly requested execution context. A
// blank group may be filled by the relay from a single-group producer token.
func ValidateAssignmentResponse(request AssignmentRequest, response AssignmentResponse, now time.Time) (EncryptionContext, error) {
	assignment := response.Assignment
	if assignment.ID == "" || assignment.PublicKey == "" || response.Secret == "" {
		return EncryptionContext{}, errors.New("E2EE assignment is incomplete")
	}
	if assignment.Attempt != 1 {
		return EncryptionContext{}, errors.New("E2EE assignment must reserve the first execution attempt")
	}
	if !assignment.ExpiresAt.After(now) {
		return EncryptionContext{}, errors.New("E2EE assignment is already expired")
	}
	actual := assignment.EncryptionContext()
	if err := actual.Validate(); err != nil {
		return EncryptionContext{}, err
	}
	if assignment.PolicyDecision.Schema != "" && assignment.PolicyDecision.Outcome != "allow" {
		return EncryptionContext{}, errors.New("E2EE assignment policy did not allow execution")
	}
	expectedRequirements := request.Requirements
	if expectedRequirements.Group == "" {
		expectedRequirements.Group = assignment.Requirements.Group
	}
	// The relay may bind a adapter reservation to the exact waiting endpoint that
	// satisfied the requested profile/model/reasoning. The producer cannot
	// choose this local adapter identifier, but it is authenticated from this
	// point onward as part of the E2EE assignment context.
	if expectedRequirements.AdapterEndpointID == 0 && strings.EqualFold(expectedRequirements.Provider, "adapter") && assignment.Requirements.AdapterEndpointID > 0 {
		expectedRequirements.AdapterEndpointID = assignment.Requirements.AdapterEndpointID
	}
	if strings.EqualFold(expectedRequirements.Provider, "adapter") && assignment.Requirements.AdapterSessionRecovery {
		expectedRequirements.AdapterSessionRecovery = true
	}
	expected := EncryptionContext{
		JobID:          assignment.JobID,
		NodeID:         assignment.NodeID,
		Attempt:        assignment.Attempt,
		OwnerSubject:   assignment.OwnerSubject,
		TenantID:       request.TenantID,
		Requirements:   expectedRequirements,
		PolicyDecision: assignment.PolicyDecision,
	}
	if !actual.Equal(expected) {
		return EncryptionContext{}, errors.New("E2EE assignment changed the requested execution context")
	}
	return actual, nil
}

func ValidateEncryptedJobContext(expected EncryptionContext, job Job) error {
	actual := job.EncryptionContext()
	// A reserved job is visible to its producer while still queued, immediately
	// before AssignJob advances the persisted generation to the reserved first
	// attempt. All other context fields must already match exactly.
	if job.Status == JobQueued && job.Attempt+1 == expected.Attempt {
		actual.Attempt = expected.Attempt
	}
	if err := actual.Validate(); err != nil {
		return err
	}
	if !actual.Equal(expected) {
		return errors.New("relay returned an E2EE job with a different authenticated execution context")
	}
	return nil
}
