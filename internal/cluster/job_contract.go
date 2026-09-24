package cluster

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

type admissionMode uint8

const (
	admissionSubmit admissionMode = iota
	admissionValidateOnly
)

const (
	AdmissionCodeAdapterSessionBusy    = "adapter.session_busy"
	AdmissionCodeCapacityOwnerQueue    = "capacity.owner_queue_full"
	AdmissionCodeCapacityOwnerReserve  = "capacity.owner_reservation_full"
	AdmissionCodeCapacityQueue         = "capacity.queue_full"
	AdmissionCodeCapacityReservation   = "capacity.reservation_full"
	AdmissionCodeContractUnsupported   = "contract.unsupported"
	AdmissionCodeIdempotencyConflict   = "idempotency.conflict"
	AdmissionCodeIdempotencyInvalid    = "idempotency.invalid"
	AdmissionCodeJobIDConflict         = "job_id.conflict"
	AdmissionCodeJobIDInvalid          = "job_id.invalid"
	AdmissionCodeNodeDraining          = "node.draining"
	AdmissionCodePayloadInvalid        = "payload.invalid"
	AdmissionCodePayloadRequired       = "payload.required"
	AdmissionCodePayloadTooLarge       = "payload.too_large"
	AdmissionCodePolicyCostExceeded    = PolicyCodeCostExceeded
	AdmissionCodePolicyCostRequired    = PolicyCodeCostRequired
	AdmissionCodePolicyCostUnknown     = PolicyCodeCostUnverifiable
	AdmissionCodePolicyEgress          = PolicyCodeEgressDenied
	AdmissionCodePolicyGroup           = PolicyCodeGroupDenied
	AdmissionCodePolicyProvider        = PolicyCodeProviderDenied
	AdmissionCodePolicyTenant          = PolicyCodeTenantDenied
	AdmissionCodePolicyTenantRequired  = PolicyCodeTenantRequired
	AdmissionCodePriorityInvalid       = "priority.invalid"
	AdmissionCodeRequestInvalidJSON    = "request.invalid_json"
	AdmissionCodeRequirementsInvalid   = "requirements.invalid"
	AdmissionCodeRequirementsManaged   = "requirements.relay_managed"
	AdmissionCodeReservationConflict   = "reservation.conflict"
	AdmissionCodeReservationContext    = "reservation.context_mismatch"
	AdmissionCodeReservationInvalid    = "reservation.invalid"
	AdmissionCodeReservationExpired    = "reservation.invalid_or_expired"
	AdmissionCodeReservationOwner      = "reservation.owner_mismatch"
	AdmissionCodeReservationRequired   = "reservation.required"
	AdmissionCodeReservationSubmitOnly = "reservation.submit_only"
	AdmissionCodeScopeForbidden        = "scope.forbidden"
	AdmissionCodeServiceStopping       = "service.stopping"
	AdmissionCodeSourceInvalid         = "source.invalid"
	AdmissionCodeTenantInvalid         = "tenant.invalid"
)

var stableAdmissionErrorCodes = []string{
	AdmissionCodeAdapterSessionBusy,
	AdmissionCodeCapacityOwnerQueue,
	AdmissionCodeCapacityOwnerReserve,
	AdmissionCodeCapacityQueue,
	AdmissionCodeCapacityReservation,
	AdmissionCodeContractUnsupported,
	AdmissionCodeIdempotencyConflict,
	AdmissionCodeIdempotencyInvalid,
	AdmissionCodeJobIDConflict,
	AdmissionCodeJobIDInvalid,
	AdmissionCodeNodeDraining,
	AdmissionCodePayloadInvalid,
	AdmissionCodePayloadRequired,
	AdmissionCodePayloadTooLarge,
	AdmissionCodePolicyCostExceeded,
	AdmissionCodePolicyCostRequired,
	AdmissionCodePolicyCostUnknown,
	AdmissionCodePolicyEgress,
	AdmissionCodePolicyGroup,
	AdmissionCodePolicyProvider,
	AdmissionCodePolicyTenant,
	AdmissionCodePolicyTenantRequired,
	AdmissionCodePriorityInvalid,
	AdmissionCodeRequestInvalidJSON,
	AdmissionCodeRequirementsInvalid,
	AdmissionCodeRequirementsManaged,
	AdmissionCodeReservationConflict,
	AdmissionCodeReservationContext,
	AdmissionCodeReservationInvalid,
	AdmissionCodeReservationExpired,
	AdmissionCodeReservationOwner,
	AdmissionCodeReservationRequired,
	AdmissionCodeReservationSubmitOnly,
	AdmissionCodeScopeForbidden,
	AdmissionCodeServiceStopping,
	AdmissionCodeSourceInvalid,
	AdmissionCodeTenantInvalid,
}

func StableAdmissionErrorCodes() []string {
	return append([]string(nil), stableAdmissionErrorCodes...)
}

// admissionError carries a stable machine-readable reason alongside the
// human-facing error. The text may improve over time; callers can branch on
// Code without scraping it.
type admissionError struct {
	status int
	code   string
	err    error
}

func (e *admissionError) Error() string { return e.err.Error() }
func (e *admissionError) Unwrap() error { return e.err }
func (e *admissionError) Code() string  { return e.code }

func rejectAdmission(status int, code string, err error) error {
	return &admissionError{status: status, code: code, err: err}
}

// NormalizeJobContractVersion preserves pre-contract clients as V1 while
// failing closed for every explicitly requested version the running relay
// does not understand.
func NormalizeJobContractVersion(version string) (string, error) {
	if version == "" || version == JobContractV1 {
		return JobContractV1, nil
	}
	return "", fmt.Errorf("unsupported job contract version %q; this relay supports %s", version, JobContractV1)
}

func (r *Relay) prepareAdmission(input SubmitRequest, record TokenRecord, mode admissionMode) (SubmitRequest, ContractValidation, error) {
	contractVersion, err := NormalizeJobContractVersion(input.ContractVersion)
	if err != nil {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeContractUnsupported, err)
	}
	input.ContractVersion = contractVersion

	if mode == admissionValidateOnly && (input.AssignmentID != "" || input.AssignmentSecret != "") {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeReservationSubmitOnly, errors.New("one-time encrypted reservations can only be validated by actual submission"))
	}
	if err := validateSubmitPayload(input, r.cfg.MaxJobBytes); err != nil {
		status := http.StatusBadRequest
		code := AdmissionCodePayloadInvalid
		var limitErr *payloadLimitError
		if errors.As(err, &limitErr) {
			status = http.StatusRequestEntityTooLarge
			code = AdmissionCodePayloadTooLarge
		}
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(status, code, err)
	}
	if len(input.Payload) == 0 && input.Sealed == nil {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodePayloadRequired, errors.New("payload or sealed_payload is required"))
	}
	if input.Sealed != nil && input.AssignmentID == "" {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeReservationRequired, errors.New("sealed_payload requires a one-time assignment reservation"))
	}
	if (input.AssignmentID != "" && (input.AssignmentSecret == "" || input.Sealed == nil)) || (input.AssignmentID == "" && input.AssignmentSecret != "") {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeReservationInvalid, errors.New("assignment_id, assignment_secret, and sealed_payload must be supplied together"))
	}
	if input.ID != "" && !validJobID(input.ID) {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeJobIDInvalid, errors.New("job id must use 1-128 safe ASCII characters and must not contain '..'"))
	}
	if input.Priority < -100 || input.Priority > 100 {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodePriorityInvalid, errors.New("priority must be between -100 and 100"))
	}
	if len([]byte(input.Source)) > 120 || strings.IndexFunc(input.Source, unicode.IsControl) >= 0 {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeSourceInvalid, errors.New("source must be at most 120 UTF-8 bytes without control characters"))
	}
	if input.MaxAttempts < 0 || input.MaxAttempts > 10 {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeRequirementsInvalid, errors.New("max_attempts must be between 0 and 10"))
	}
	if err := scopeRequirements(&input.Requirements, record); err != nil {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusForbidden, AdmissionCodeScopeForbidden, err)
	}
	if (input.Requirements.AdapterEndpointID != 0 || input.Requirements.AdapterPrincipal != "" || input.Requirements.AdapterSessionRecovery) && input.AssignmentID == "" {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeRequirementsManaged, errors.New("adapter endpoint and recovery requirements are relay-assigned and cannot be submitted directly"))
	}
	validatedRequirements := input.Requirements
	if input.AssignmentID != "" {
		// The reservation owns these values and ConsumeReservationAdmitted compares
		// the complete authenticated assignment before accepting the sealed job.
		validatedRequirements.AdapterEndpointID = 0
		validatedRequirements.AdapterPrincipal = ""
		validatedRequirements.AdapterSessionRecovery = false
	}
	if err := r.validateRequirements(validatedRequirements); err != nil {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeRequirementsInvalid, err)
	}
	if err := validateTenantID(input.TenantID); err != nil {
		return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusUnprocessableEntity, AdmissionCodeTenantInvalid, err)
	}
	policyDecision, err := EvaluateExecutionPolicy(r.cfg.ExecutionPolicy, input.TenantID, input.Requirements, time.Now().UTC())
	if err != nil {
		var violation *PolicyViolation
		if errors.As(err, &violation) {
			return SubmitRequest{}, ContractValidation{}, rejectAdmission(http.StatusForbidden, violation.Code(), err)
		}
		return SubmitRequest{}, ContractValidation{}, err
	}
	input.PolicyDecision = policyDecision
	if input.MaxAttempts == 0 {
		input.MaxAttempts = r.cfg.MaxAttempts
	}
	if input.MaxAttempts == 0 {
		input.MaxAttempts = 3
	}
	input.OwnerSubject = record.Subject

	payloadMode := "cleartext"
	if input.Sealed != nil {
		payloadMode = "sealed"
	}
	validation := ContractValidation{
		ContractVersion: input.ContractVersion,
		Valid:           true,
		PayloadMode:     payloadMode,
		MaxAttempts:     input.MaxAttempts,
		PolicyDecision:  policyDecision,
	}
	return input, validation, nil
}

func writeAdmissionError(w http.ResponseWriter, err error) {
	var problem *admissionError
	if errors.As(err, &problem) {
		writeError(w, problem.status, problem)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

// admissionStoreErrorCode turns the bounded set of durable admission races
// into public identifiers. The store's prose remains useful to operators but
// clients must not need to scrape it. Unknown storage failures deliberately
// remain unclassified rather than receiving a misleading stable code.
func admissionStoreErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrQueueFull):
		return AdmissionCodeCapacityQueue
	case errors.Is(err, ErrOwnerQueueCapacity):
		return AdmissionCodeCapacityOwnerQueue
	case errors.Is(err, ErrReservationCapacity):
		return AdmissionCodeCapacityReservation
	case errors.Is(err, ErrOwnerReservationCapacity):
		return AdmissionCodeCapacityOwnerReserve
	case errors.Is(err, ErrAdapterSessionBusy):
		return AdmissionCodeAdapterSessionBusy
	case errors.Is(err, ErrReservationOwnerMismatch):
		return AdmissionCodeReservationOwner
	case errors.Is(err, ErrReservationContextMismatch):
		return AdmissionCodeReservationContext
	case errors.Is(err, ErrReservationInvalidOrExpired):
		return AdmissionCodeReservationExpired
	case errors.Is(err, ErrIdempotencyConflict):
		return AdmissionCodeIdempotencyConflict
	case errors.Is(err, ErrNodeDraining):
		return AdmissionCodeNodeDraining
	default:
		return ""
	}
}

func submissionStoreErrorCode(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return AdmissionCodeReservationExpired
	}
	if errors.Is(err, os.ErrExist) {
		return AdmissionCodeJobIDConflict
	}
	return admissionStoreErrorCode(err)
}

func reservationStoreErrorCode(err error) string {
	if errors.Is(err, os.ErrExist) {
		return AdmissionCodeReservationConflict
	}
	return admissionStoreErrorCode(err)
}
