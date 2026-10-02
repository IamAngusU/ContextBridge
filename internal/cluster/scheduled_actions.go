package cluster

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	bolt "go.etcd.io/bbolt"
)

const (
	ScheduledActionRequestV1 = "contextbridge.scheduled-action-request.v1"
	ScheduledActionPolicyV1  = "contextbridge.scheduled-action-policy.v1"
	ScheduledActionV1        = "contextbridge.scheduled-action.v1"
	ScheduledActionListV1    = "contextbridge.scheduled-action-list.v1"
	ScheduledActionPayloadV1 = "contextbridge.scheduled-adapter-action.v1"
	scheduledActionTask      = "scheduled_action"

	ScheduledActionPreview   = "preview"
	ScheduledActionActive    = "active"
	ScheduledActionCompleted = "completed"
	ScheduledActionFailed    = "failed"
	ScheduledActionUnknown   = "unknown"
	ScheduledActionCancelled = "cancelled"
	ScheduledActionExpired   = "expired"

	ScheduledActionFailureJobMissing               = "scheduled_action.job_missing"
	ScheduledActionFailureCancelAmbiguous          = "scheduled_action.cancel_ambiguous"
	ScheduledActionFailureDeliveryTimeoutAmbiguous = "scheduled_action.delivery_timeout_ambiguous"
	ScheduledActionFailureDeliveryWindowExpired    = "scheduled_action.delivery_window_expired"
	ScheduledActionFailureJobCancelled             = "scheduled_action.job_cancelled"
	ScheduledActionFailureCredentialInactive       = "scheduled_action.credential_inactive"
	ScheduledActionFailureEnvelopeFailed           = "scheduled_action.envelope_failed"
	ScheduledActionFailurePolicyDenied             = "scheduled_action.policy_denied"
	ScheduledActionFailureDispatchFailed           = "scheduled_action.dispatch_failed"

	defaultScheduledMaxActive             = 8
	maximumScheduledMaxActive             = 100
	defaultScheduledHorizonSeconds        = int64(7 * 24 * 60 * 60)
	maximumScheduledHorizonSeconds        = int64(365 * 24 * 60 * 60)
	defaultScheduledMinIntervalSeconds    = int64(15 * 60)
	minimumScheduledIntervalSeconds       = int64(60)
	defaultScheduledMaxOccurrences        = 32
	maximumScheduledOccurrences           = 366
	defaultScheduledDeliveryWindowSeconds = int64(60 * 60)
	minimumScheduledDeliveryWindowSeconds = int64(30)
	maximumScheduledDeliveryWindowSeconds = int64(24 * 60 * 60)
	maximumScheduledActionTargets         = 16
	maximumScheduledActionKindsPerTarget  = 32
	maximumScheduledDestinationsPerTarget = 32
	maximumScheduledActionRecords         = 10_000
	maximumScheduledActionRecordsPerOwner = 1_024
	maximumScheduledActionBatch           = 32
	maximumScheduledActionList            = 100
	scheduledActionPreviewLifetime        = 10 * time.Minute
	scheduledActionMinimumLead            = time.Second
	scheduledActionPendingPoll            = time.Second
)

var (
	ErrScheduledActionCapacity       = errors.New("scheduled action capacity is full")
	ErrScheduledActionState          = errors.New("scheduled action state changed")
	ErrScheduledActionScope          = errors.New("producer credential does not authorize this scheduled action")
	ErrScheduledActionCredential     = errors.New("scheduled action credential is revoked, expired, or changed")
	ErrScheduledActionAdapterStopped = errors.New("scheduled action adapter is disabled")
	ErrScheduledActionTaskReserved   = errors.New("scheduled_action is reserved for the relay's confirmed scheduled-action dispatcher")
)

var scheduledActionFailureCodeList = []string{
	ScheduledActionFailureCancelAmbiguous,
	ScheduledActionFailureCredentialInactive,
	ScheduledActionFailureDeliveryTimeoutAmbiguous,
	ScheduledActionFailureDeliveryWindowExpired,
	ScheduledActionFailureDispatchFailed,
	ScheduledActionFailureEnvelopeFailed,
	ScheduledActionFailureJobCancelled,
	ScheduledActionFailureJobMissing,
	ScheduledActionFailurePolicyDenied,
}

// StableScheduledActionFailureCodes returns the terminal action-level codes
// advertised by the protocol manifest. Job-level provider failures remain on
// the linked durable job rather than being rewritten here.
func StableScheduledActionFailureCodes() []string {
	return append([]string(nil), scheduledActionFailureCodeList...)
}

// ScheduledActionRequest contains only normalized execution references. A
// channel may parse natural language before building this object, but the
// relay never authorizes or stores the original free-form instruction.
type ScheduledActionRequest struct {
	Schema                string    `json:"schema"`
	TenantID              string    `json:"tenant_id,omitempty"`
	AdapterUID            string    `json:"adapter_uid"`
	ActionKind            string    `json:"action_kind"`
	DestinationRef        string    `json:"destination_ref"`
	PayloadRef            string    `json:"payload_ref"`
	StartAt               time.Time `json:"start_at"`
	Timezone              string    `json:"timezone"`
	RepeatEverySeconds    int64     `json:"repeat_every_seconds,omitempty"`
	Occurrences           int       `json:"occurrences,omitempty"`
	DeliveryWindowSeconds int64     `json:"delivery_window_seconds,omitempty"`
	Priority              int       `json:"priority,omitempty"`
}

// ScheduledAction is the durable, content-minimized state machine. The
// credential hash is stored only so dispatch can re-prove the exact original
// authority. Every API response clears it before serialization.
type ScheduledAction struct {
	Schema                string    `json:"schema"`
	ID                    string    `json:"id"`
	Status                string    `json:"status"`
	OwnerSubject          string    `json:"owner_subject"`
	TenantID              string    `json:"tenant_id,omitempty"`
	CredentialID          string    `json:"credential_id"`
	CredentialHash        string    `json:"credential_hash,omitempty"`
	AdapterUID            string    `json:"adapter_uid"`
	AdapterProfile        string    `json:"adapter_profile"`
	AdapterPrincipal      string    `json:"adapter_principal,omitempty"`
	ActionKind            string    `json:"action_kind"`
	DestinationRef        string    `json:"destination_ref"`
	PayloadRef            string    `json:"payload_ref"`
	StartAt               time.Time `json:"start_at"`
	LocalStart            string    `json:"local_start"`
	Timezone              string    `json:"timezone"`
	RepeatEverySeconds    int64     `json:"repeat_every_seconds,omitempty"`
	Occurrences           int       `json:"occurrences"`
	DeliveryWindowSeconds int64     `json:"delivery_window_seconds"`
	Priority              int       `json:"priority,omitempty"`
	CompletedOccurrences  int       `json:"completed_occurrences"`
	SkippedOccurrences    int       `json:"skipped_occurrences"`
	CurrentOccurrence     int       `json:"current_occurrence,omitempty"`
	CurrentDueAt          time.Time `json:"current_due_at,omitempty,omitzero"`
	CurrentExpiresAt      time.Time `json:"current_expires_at,omitempty,omitzero"`
	CurrentJobID          string    `json:"current_job_id,omitempty"`
	LastJobID             string    `json:"last_job_id,omitempty"`
	LastOutcome           string    `json:"last_outcome,omitempty"`
	FailureCode           string    `json:"failure_code,omitempty"`
	WaitingReason         string    `json:"waiting_reason,omitempty"`
	NextRunAt             time.Time `json:"next_run_at,omitempty,omitzero"`
	NextCheckAt           time.Time `json:"next_check_at,omitempty,omitzero"`
	PreviewExpiresAt      time.Time `json:"preview_expires_at,omitempty,omitzero"`
	ConfirmedAt           time.Time `json:"confirmed_at,omitempty,omitzero"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type ScheduledActionList struct {
	Schema  string            `json:"schema"`
	Actions []ScheduledAction `json:"actions"`
	Total   int               `json:"total"`
}

type scheduledAdapterActionPayload struct {
	Schema         string    `json:"schema"`
	ScheduleID     string    `json:"schedule_id"`
	AdapterUID     string    `json:"adapter_uid"`
	Occurrence     int       `json:"occurrence"`
	ActionKind     string    `json:"action_kind"`
	DestinationRef string    `json:"destination_ref"`
	PayloadRef     string    `json:"payload_ref"`
	ScheduledFor   time.Time `json:"scheduled_for"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func validateScheduledActionLimits(limits *ScheduledActionLimits) error {
	if limits == nil {
		return nil
	}
	if limits.Schema != ScheduledActionPolicyV1 {
		return fmt.Errorf("producer_limits.scheduled_actions.schema must be %s", ScheduledActionPolicyV1)
	}
	if len(limits.Targets) < 1 || len(limits.Targets) > maximumScheduledActionTargets {
		return fmt.Errorf("producer_limits.scheduled_actions.targets must contain 1 to %d values", maximumScheduledActionTargets)
	}
	seenUIDs := make(map[string]struct{}, len(limits.Targets))
	for index, target := range limits.Targets {
		field := fmt.Sprintf("targets[%d]", index)
		if !validAdapterUID(target.AdapterUID) || target.AdapterUID != strings.ToLower(target.AdapterUID) {
			return fmt.Errorf("producer_limits.scheduled_actions.%s.adapter_uid is invalid", field)
		}
		uid := strings.ToLower(target.AdapterUID)
		if _, exists := seenUIDs[uid]; exists {
			return errors.New("producer_limits.scheduled_actions.targets contains a duplicate adapter_uid")
		}
		seenUIDs[uid] = struct{}{}
		if !validRoutingLabel(target.AdapterProfile, 80) {
			return fmt.Errorf("producer_limits.scheduled_actions.%s.adapter_profile is invalid", field)
		}
		if !validRoutingLabel(target.AdapterPrincipal, 80) {
			return fmt.Errorf("producer_limits.scheduled_actions.%s.adapter_principal is invalid", field)
		}
		if len(target.ActionKinds) < 1 || len(target.ActionKinds) > maximumScheduledActionKindsPerTarget {
			return fmt.Errorf("producer_limits.scheduled_actions.%s.action_kinds must contain 1 to %d values", field, maximumScheduledActionKindsPerTarget)
		}
		if len(target.DestinationRefs) < 1 || len(target.DestinationRefs) > maximumScheduledDestinationsPerTarget {
			return fmt.Errorf("producer_limits.scheduled_actions.%s.destination_refs must contain 1 to %d values", field, maximumScheduledDestinationsPerTarget)
		}
		if err := validateUniqueScheduledValues(target.ActionKinds, validScheduledActionKind, field+".action_kinds"); err != nil {
			return err
		}
		if err := validateUniqueScheduledValues(target.DestinationRefs, validScheduledDestinationRef, field+".destination_refs"); err != nil {
			return err
		}
	}
	if limits.MaxActive < 0 || limits.MaxActive > maximumScheduledMaxActive {
		return fmt.Errorf("producer_limits.scheduled_actions.max_active must be 0 to %d", maximumScheduledMaxActive)
	}
	if limits.MaxHorizonSeconds < 0 || limits.MaxHorizonSeconds > maximumScheduledHorizonSeconds {
		return fmt.Errorf("producer_limits.scheduled_actions.max_horizon_seconds must be 0 to %d", maximumScheduledHorizonSeconds)
	}
	if limits.MinIntervalSeconds != 0 && (limits.MinIntervalSeconds < minimumScheduledIntervalSeconds || limits.MinIntervalSeconds > maximumScheduledHorizonSeconds) {
		return fmt.Errorf("producer_limits.scheduled_actions.min_interval_seconds must be 0 or %d to %d", minimumScheduledIntervalSeconds, maximumScheduledHorizonSeconds)
	}
	if limits.MaxOccurrences < 0 || limits.MaxOccurrences > maximumScheduledOccurrences {
		return fmt.Errorf("producer_limits.scheduled_actions.max_occurrences must be 0 to %d", maximumScheduledOccurrences)
	}
	if limits.MaxDeliveryWindowSeconds != 0 && (limits.MaxDeliveryWindowSeconds < minimumScheduledDeliveryWindowSeconds || limits.MaxDeliveryWindowSeconds > maximumScheduledDeliveryWindowSeconds) {
		return fmt.Errorf("producer_limits.scheduled_actions.max_delivery_window_seconds must be 0 or %d to %d", minimumScheduledDeliveryWindowSeconds, maximumScheduledDeliveryWindowSeconds)
	}
	return nil
}

func validateUniqueScheduledValues(values []string, valid func(string) bool, field string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return fmt.Errorf("producer_limits.scheduled_actions.%s contains an invalid value", field)
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("producer_limits.scheduled_actions.%s contains a case-insensitive duplicate", field)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func normalizeScheduledActionLimits(input *ScheduledActionLimits) *ScheduledActionLimits {
	if input == nil {
		return nil
	}
	limits := *input
	limits.Targets = make([]ScheduledActionTarget, len(input.Targets))
	for index, inputTarget := range input.Targets {
		target := inputTarget
		target.AdapterUID = strings.ToLower(strings.TrimSpace(target.AdapterUID))
		target.AdapterProfile = strings.TrimSpace(target.AdapterProfile)
		target.AdapterPrincipal = strings.TrimSpace(target.AdapterPrincipal)
		target.ActionKinds = append([]string(nil), inputTarget.ActionKinds...)
		target.DestinationRefs = append([]string(nil), inputTarget.DestinationRefs...)
		for item := range target.ActionKinds {
			target.ActionKinds[item] = strings.ToLower(strings.TrimSpace(target.ActionKinds[item]))
		}
		for item := range target.DestinationRefs {
			target.DestinationRefs[item] = strings.ToLower(strings.TrimSpace(target.DestinationRefs[item]))
		}
		sort.Strings(target.ActionKinds)
		sort.Strings(target.DestinationRefs)
		limits.Targets[index] = target
	}
	if limits.MaxActive == 0 {
		limits.MaxActive = defaultScheduledMaxActive
	}
	if limits.MaxHorizonSeconds == 0 {
		limits.MaxHorizonSeconds = defaultScheduledHorizonSeconds
	}
	if limits.MinIntervalSeconds == 0 {
		limits.MinIntervalSeconds = defaultScheduledMinIntervalSeconds
	}
	if limits.MaxOccurrences == 0 {
		limits.MaxOccurrences = defaultScheduledMaxOccurrences
	}
	if limits.MaxDeliveryWindowSeconds == 0 {
		limits.MaxDeliveryWindowSeconds = defaultScheduledDeliveryWindowSeconds
	}
	sort.Slice(limits.Targets, func(left, right int) bool {
		return limits.Targets[left].AdapterUID < limits.Targets[right].AdapterUID
	})
	return &limits
}

func validScheduledActionKind(value string) bool {
	return validAdapterID(value, 80)
}

func validScheduledDestinationRef(value string) bool {
	return validFixedOpaqueRef(value, "dst_")
}

func validScheduledPayloadRef(value string) bool {
	return validFixedOpaqueRef(value, "ref_")
}

func validScheduledActionID(value string) bool {
	return validFixedOpaqueRef(value, "sact_")
}

func validFixedOpaqueRef(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func scheduledActionTarget(limits *ScheduledActionLimits, adapterUID string) (ScheduledActionTarget, bool) {
	if limits == nil {
		return ScheduledActionTarget{}, false
	}
	for _, target := range limits.Targets {
		if strings.EqualFold(target.AdapterUID, adapterUID) {
			return target, true
		}
	}
	return ScheduledActionTarget{}, false
}

func scheduledActionAllowed(record TokenRecord, action ScheduledAction) bool {
	limits := record.ProducerLimits.ScheduledActions
	target, found := scheduledActionTarget(limits, action.AdapterUID)
	return record.Role == "producer" && found && !record.ProducerLimits.RequireE2EE &&
		record.ID == action.CredentialID && record.Subject == action.OwnerSubject &&
		(len(record.ProducerLimits.AllowedTenants) == 0 || contains(record.ProducerLimits.AllowedTenants, action.TenantID)) &&
		producerPriorityAllowed(action.Priority, record.ProducerLimits) &&
		strings.EqualFold(target.AdapterProfile, action.AdapterProfile) && target.AdapterPrincipal == action.AdapterPrincipal &&
		containsFold(target.ActionKinds, action.ActionKind) && containsFold(target.DestinationRefs, action.DestinationRef)
}

func normalizeScheduledActionRequest(input ScheduledActionRequest, record TokenRecord, now time.Time) (ScheduledAction, error) {
	if input.Schema != ScheduledActionRequestV1 {
		return ScheduledAction{}, errors.New("scheduled action request schema is unsupported")
	}
	limits := record.ProducerLimits.ScheduledActions
	if record.Role != "producer" || limits == nil {
		return ScheduledAction{}, ErrScheduledActionScope
	}
	if record.ProducerLimits.RequireE2EE {
		return ScheduledAction{}, errors.New("scheduled adapter actions are unavailable to a credential that requires E2EE payloads")
	}
	if err := scopeTenantID(&input.TenantID, record); err != nil {
		return ScheduledAction{}, err
	}
	// Unlike ordinary job metadata, tenant_id participates in the durable
	// scheduled-action capacity boundary. Do not let an unconstrained producer
	// manufacture arbitrary tenant labels to evade max_active. Administrators
	// that need tenant-specific schedules must bind those tenants explicitly on
	// the credential; otherwise the action remains in the owner-level scope.
	if len(record.ProducerLimits.AllowedTenants) == 0 && input.TenantID != "" {
		return ScheduledAction{}, ErrTenantScopeForbidden
	}
	if !validAdapterUID(input.AdapterUID) || input.AdapterUID != strings.ToLower(input.AdapterUID) {
		return ScheduledAction{}, ErrScheduledActionScope
	}
	target, found := scheduledActionTarget(limits, input.AdapterUID)
	if !found {
		return ScheduledAction{}, ErrScheduledActionScope
	}
	if !validScheduledActionKind(input.ActionKind) || !containsFold(target.ActionKinds, input.ActionKind) {
		return ScheduledAction{}, ErrScheduledActionScope
	}
	if !validScheduledDestinationRef(input.DestinationRef) || !containsFold(target.DestinationRefs, input.DestinationRef) {
		return ScheduledAction{}, ErrScheduledActionScope
	}
	if !validScheduledPayloadRef(input.PayloadRef) {
		return ScheduledAction{}, errors.New("payload_ref must be an opaque ref_ identifier with 128 bits of lowercase hexadecimal entropy")
	}
	if input.Priority < -100 || input.Priority > 100 || !producerPriorityAllowed(input.Priority, record.ProducerLimits) {
		return ScheduledAction{}, ErrPriorityScopeForbidden
	}
	if input.StartAt.IsZero() || input.StartAt.Before(now.Add(scheduledActionMinimumLead)) {
		return ScheduledAction{}, fmt.Errorf("start_at must be at least %s in the future", scheduledActionMinimumLead)
	}
	if input.StartAt.After(now.Add(time.Duration(limits.MaxHorizonSeconds) * time.Second)) {
		return ScheduledAction{}, errors.New("start_at exceeds the credential scheduling horizon")
	}
	if !validRoutingLabel(input.Timezone, 80) || strings.EqualFold(input.Timezone, "Local") {
		return ScheduledAction{}, errors.New("timezone must be an explicit IANA timezone")
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return ScheduledAction{}, errors.New("timezone is not a recognized IANA timezone")
	}
	localized := input.StartAt.In(location)
	_, suppliedOffset := input.StartAt.Zone()
	_, expectedOffset := localized.Zone()
	if suppliedOffset != expectedOffset {
		return ScheduledAction{}, errors.New("start_at UTC offset does not match timezone at that instant")
	}
	occurrences := input.Occurrences
	if occurrences == 0 {
		occurrences = 1
	}
	if input.RepeatEverySeconds == 0 {
		if occurrences != 1 {
			return ScheduledAction{}, errors.New("one-shot scheduled actions must have exactly one occurrence")
		}
	} else {
		if occurrences < 2 || occurrences > limits.MaxOccurrences {
			return ScheduledAction{}, fmt.Errorf("interval scheduled actions must have 2 to %d occurrences", limits.MaxOccurrences)
		}
		if input.RepeatEverySeconds < limits.MinIntervalSeconds || input.RepeatEverySeconds > limits.MaxHorizonSeconds {
			return ScheduledAction{}, fmt.Errorf("repeat_every_seconds must be %d to %d", limits.MinIntervalSeconds, limits.MaxHorizonSeconds)
		}
		spanOccurrences := int64(occurrences - 1)
		if input.RepeatEverySeconds > limits.MaxHorizonSeconds/spanOccurrences {
			return ScheduledAction{}, errors.New("the last occurrence exceeds the credential scheduling horizon")
		}
		last := input.StartAt.Add(time.Duration(spanOccurrences*input.RepeatEverySeconds) * time.Second)
		if last.After(now.Add(time.Duration(limits.MaxHorizonSeconds) * time.Second)) {
			return ScheduledAction{}, errors.New("the last occurrence exceeds the credential scheduling horizon")
		}
	}
	window := input.DeliveryWindowSeconds
	if window == 0 {
		window = min(defaultScheduledDeliveryWindowSeconds, limits.MaxDeliveryWindowSeconds)
	}
	if window < minimumScheduledDeliveryWindowSeconds || window > limits.MaxDeliveryWindowSeconds {
		return ScheduledAction{}, fmt.Errorf("delivery_window_seconds must be %d to %d", minimumScheduledDeliveryWindowSeconds, limits.MaxDeliveryWindowSeconds)
	}
	action := ScheduledAction{
		Schema: ScheduledActionV1, Status: ScheduledActionPreview, OwnerSubject: record.Subject, TenantID: input.TenantID,
		CredentialID: record.ID, CredentialHash: record.AuthHash, AdapterUID: strings.ToLower(input.AdapterUID),
		AdapterProfile: target.AdapterProfile, AdapterPrincipal: target.AdapterPrincipal,
		ActionKind: strings.ToLower(input.ActionKind), DestinationRef: strings.ToLower(input.DestinationRef),
		PayloadRef: strings.ToLower(input.PayloadRef), StartAt: input.StartAt.UTC(), LocalStart: localized.Format(time.RFC3339Nano),
		Timezone: input.Timezone, RepeatEverySeconds: input.RepeatEverySeconds, Occurrences: occurrences,
		DeliveryWindowSeconds: window, Priority: input.Priority, NextRunAt: input.StartAt.UTC(),
	}
	if !scheduledActionAllowed(record, action) {
		return ScheduledAction{}, ErrScheduledActionScope
	}
	return action, nil
}

func scheduledActionResponse(action ScheduledAction) ScheduledAction {
	action.CredentialHash = ""
	action.AdapterPrincipal = ""
	return action
}

func scheduledActionTerminal(status string) bool {
	switch status {
	case ScheduledActionCompleted, ScheduledActionFailed, ScheduledActionUnknown, ScheduledActionCancelled, ScheduledActionExpired:
		return true
	default:
		return false
	}
}

func scheduledActionCheckKey(at time.Time, id string) []byte {
	key := make([]byte, 8+len(id))
	binary.BigEndian.PutUint64(key[:8], uint64(at.UTC().UnixNano()))
	copy(key[8:], id)
	return key
}

func scheduledActionCheckTime(key []byte) (time.Time, bool) {
	if len(key) <= 8 {
		return time.Time{}, false
	}
	nanoseconds := binary.BigEndian.Uint64(key[:8])
	if nanoseconds > uint64(^uint64(0)>>1) {
		return time.Time{}, false
	}
	return time.Unix(0, int64(nanoseconds)).UTC(), true
}

func validStoredScheduledAction(action ScheduledAction, key string) bool {
	return action.Schema == ScheduledActionV1 && action.ID == key && validScheduledActionID(action.ID) &&
		action.OwnerSubject != "" && action.CredentialID != "" && len(action.CredentialHash) == 64 &&
		validAdapterUID(action.AdapterUID) && validRoutingLabel(action.AdapterProfile, 80) &&
		validRoutingLabel(action.AdapterPrincipal, 80) &&
		validScheduledActionKind(action.ActionKind) && validScheduledDestinationRef(action.DestinationRef) &&
		validScheduledPayloadRef(action.PayloadRef) && !action.CreatedAt.IsZero() && !action.UpdatedAt.IsZero()
}

func putScheduledActionTx(tx *bolt.Tx, previous *ScheduledAction, action ScheduledAction) error {
	if previous != nil && !previous.NextCheckAt.IsZero() {
		if err := tx.Bucket(bucketScheduledChecks).Delete(scheduledActionCheckKey(previous.NextCheckAt, previous.ID)); err != nil {
			return err
		}
	}
	if scheduledActionTerminal(action.Status) {
		action.NextCheckAt = time.Time{}
		action.WaitingReason = ""
	}
	if err := putJSON(tx.Bucket(bucketScheduledActions), action.ID, action); err != nil {
		return err
	}
	if !action.NextCheckAt.IsZero() {
		return tx.Bucket(bucketScheduledChecks).Put(scheduledActionCheckKey(action.NextCheckAt, action.ID), []byte(action.ID))
	}
	return nil
}

func producerCredentialByHashTx(tx *bolt.Tx, hash string, now time.Time) (TokenRecord, bool) {
	if len(hash) != 64 {
		return TokenRecord{}, false
	}
	var record TokenRecord
	if err := getJSON(tx.Bucket(bucketTokens), hash, &record); err != nil || record.Role != "producer" || record.Revoked || (!record.ExpiresAt.IsZero() && !now.Before(record.ExpiresAt)) {
		return TokenRecord{}, false
	}
	record.AuthHash = hash
	return record, true
}

func (s *Store) ProducerCredentialByHash(hash string, now time.Time) (TokenRecord, bool) {
	var record TokenRecord
	valid := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		record, valid = producerCredentialByHashTx(tx, hash, now.UTC())
		return nil
	})
	return record, valid
}

func (s *Store) retainedScheduledActionMetrics() (map[string]uint64, error) {
	counts := map[string]uint64{
		ScheduledActionPreview: 0, ScheduledActionActive: 0, ScheduledActionCompleted: 0,
		ScheduledActionFailed: 0, ScheduledActionUnknown: 0, ScheduledActionCancelled: 0,
		ScheduledActionExpired: 0,
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketScheduledActions).ForEach(func(key, value []byte) error {
			var action ScheduledAction
			if err := json.Unmarshal(value, &action); err != nil || !validStoredScheduledAction(action, string(key)) || !validScheduledActionStatus(action.Status) {
				return errors.New("scheduled action record is invalid")
			}
			counts[action.Status] = saturatingUint64Add(counts[action.Status], 1)
			return nil
		})
	})
	return counts, err
}

func (s *Store) CreateScheduledActionPreview(action ScheduledAction, now time.Time) (ScheduledAction, error) {
	if action.Schema != ScheduledActionV1 || action.Status != ScheduledActionPreview || action.ID != "" || action.CredentialHash == "" {
		return ScheduledAction{}, errors.New("scheduled action preview is invalid")
	}
	id, err := randomID("sact")
	if err != nil {
		return ScheduledAction{}, err
	}
	action.ID = id
	action.CreatedAt = now.UTC()
	action.UpdatedAt = action.CreatedAt
	action.PreviewExpiresAt = action.CreatedAt.Add(scheduledActionPreviewLifetime)
	action.NextCheckAt = action.PreviewExpiresAt
	err = s.db.Update(func(tx *bolt.Tx) error {
		record, valid := producerCredentialByHashTx(tx, action.CredentialHash, now)
		if !valid || !scheduledActionAllowed(record, action) || record.ProducerLimits.ScheduledActions == nil {
			return ErrScheduledActionCredential
		}
		bucket := tx.Bucket(bucketScheduledActions)
		if bucket.Stats().KeyN >= maximumScheduledActionRecords {
			return ErrScheduledActionCapacity
		}
		active := 0
		ownerRecords := 0
		if err := bucket.ForEach(func(_, value []byte) error {
			var existing ScheduledAction
			if err := json.Unmarshal(value, &existing); err != nil {
				return err
			}
			if existing.OwnerSubject == action.OwnerSubject {
				ownerRecords++
			}
			if existing.OwnerSubject == action.OwnerSubject && existing.TenantID == action.TenantID &&
				(existing.Status == ScheduledActionPreview || existing.Status == ScheduledActionActive) &&
				(existing.Status != ScheduledActionPreview || now.Before(existing.PreviewExpiresAt)) {
				active++
			}
			return nil
		}); err != nil {
			return err
		}
		if ownerRecords >= maximumScheduledActionRecordsPerOwner {
			return ErrScheduledActionCapacity
		}
		if active >= record.ProducerLimits.ScheduledActions.MaxActive {
			return ErrScheduledActionCapacity
		}
		return putScheduledActionTx(tx, nil, action)
	})
	return scheduledActionResponse(action), err
}

func (s *Store) ConfirmScheduledAction(id, credentialHash string, now time.Time) (ScheduledAction, error) {
	var action ScheduledAction
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil || action.CredentialHash != credentialHash {
			return os.ErrNotExist
		}
		if action.Status != ScheduledActionPreview {
			return ErrScheduledActionState
		}
		if !now.Before(action.PreviewExpiresAt) || !now.Before(action.StartAt) {
			return ErrScheduledActionState
		}
		record, valid := producerCredentialByHashTx(tx, credentialHash, now)
		if !valid || !scheduledActionAllowed(record, action) {
			return ErrScheduledActionCredential
		}
		previous := action
		action.Status = ScheduledActionActive
		action.ConfirmedAt = now.UTC()
		action.UpdatedAt = action.ConfirmedAt
		action.PreviewExpiresAt = time.Time{}
		action.NextCheckAt = action.StartAt
		return putScheduledActionTx(tx, &previous, action)
	})
	return scheduledActionResponse(action), err
}

func (s *Store) GetScheduledActionForCredential(id, credentialHash string) (ScheduledAction, error) {
	var action ScheduledAction
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil || action.CredentialHash != credentialHash {
			return os.ErrNotExist
		}
		if !validStoredScheduledAction(action, id) {
			return errors.New("scheduled action record is invalid")
		}
		return nil
	})
	return scheduledActionResponse(action), err
}

func (s *Store) ListScheduledActionsForCredential(credentialHash, status string, limit int) (ScheduledActionList, error) {
	if status != "" && !validScheduledActionStatus(status) {
		return ScheduledActionList{}, errors.New("scheduled action status is invalid")
	}
	if limit < 1 || limit > maximumScheduledActionList {
		limit = maximumScheduledActionList
	}
	result := ScheduledActionList{Schema: ScheduledActionListV1, Actions: []ScheduledAction{}}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketScheduledActions).ForEach(func(key, value []byte) error {
			var action ScheduledAction
			if err := json.Unmarshal(value, &action); err != nil || !validStoredScheduledAction(action, string(key)) {
				return errors.New("scheduled action record is invalid")
			}
			if action.CredentialHash != credentialHash || (status != "" && action.Status != status) {
				return nil
			}
			result.Total++
			result.Actions = append(result.Actions, scheduledActionResponse(action))
			return nil
		})
	})
	if err != nil {
		return ScheduledActionList{}, err
	}
	sort.Slice(result.Actions, func(left, right int) bool {
		if !result.Actions[left].CreatedAt.Equal(result.Actions[right].CreatedAt) {
			return result.Actions[left].CreatedAt.After(result.Actions[right].CreatedAt)
		}
		return result.Actions[left].ID < result.Actions[right].ID
	})
	if len(result.Actions) > limit {
		result.Actions = result.Actions[:limit]
	}
	return result, nil
}

func (s *Store) CancelScheduledAction(id, credentialHash string, now time.Time) (ScheduledAction, error) {
	var action ScheduledAction
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil || action.CredentialHash != credentialHash {
			return os.ErrNotExist
		}
		if scheduledActionTerminal(action.Status) {
			return ErrScheduledActionState
		}
		previous := action
		if action.CurrentJobID != "" {
			var job Job
			if err := getJSON(tx.Bucket(bucketJobs), action.CurrentJobID, &job); err != nil {
				action.Status = ScheduledActionUnknown
				action.LastOutcome = "unknown"
				action.FailureCode = ScheduledActionFailureJobMissing
				action.UpdatedAt = now.UTC()
				return putScheduledActionTx(tx, &previous, action)
			}
			if terminalJobStatus(job.Status) {
				action.LastJobID = job.ID
				action.CurrentJobID = ""
				action.CurrentDueAt = time.Time{}
				action.CurrentExpiresAt = time.Time{}
				action.WaitingReason = ""
				processed := action.CurrentOccurrence
				action.CurrentOccurrence = 0
				switch job.Status {
				case JobCompleted:
					action.CompletedOccurrences++
					action.LastOutcome = "completed"
					if processed >= action.Occurrences {
						action.Status = ScheduledActionCompleted
						action.UpdatedAt = now.UTC()
						return putScheduledActionTx(tx, &previous, action)
					}
				case JobFailed:
					action.UpdatedAt = now.UTC()
					if job.FailureCode == FailureExecutionStateAmbiguous || job.FailureCode == FailureExecutionTimeoutAmbiguous {
						action.Status = ScheduledActionUnknown
						action.LastOutcome = "unknown"
						action.FailureCode = job.FailureCode
					} else {
						action.Status = ScheduledActionFailed
						action.LastOutcome = "failed"
						action.FailureCode = job.FailureCode
					}
					return putScheduledActionTx(tx, &previous, action)
				case JobCancelled:
					// The requested terminal schedule state is already safe: no future
					// occurrence may be emitted, and the linked job remains auditable.
					action.LastOutcome = "cancelled"
				}
			}
			if !terminalJobStatus(job.Status) {
				wasStarted := job.Status == JobAssigned || job.Status == JobRunning
				if _, err := cancelJobTx(tx, s, job, now); err != nil {
					return err
				}
				if wasStarted {
					action.Status = ScheduledActionUnknown
					action.LastOutcome = "unknown"
					action.FailureCode = ScheduledActionFailureCancelAmbiguous
					action.UpdatedAt = now.UTC()
					return putScheduledActionTx(tx, &previous, action)
				}
			}
		}
		action.Status = ScheduledActionCancelled
		action.FailureCode = ""
		action.LastOutcome = "cancelled"
		action.UpdatedAt = now.UTC()
		return putScheduledActionTx(tx, &previous, action)
	})
	return scheduledActionResponse(action), err
}

// resolveScheduledActionPreDispatchCancellation replaces a conservative
// ambiguous result only after the relay's live reservation gate proved that
// dispatch never began. markStoreTerminalState prevents a concurrent
// beginDispatch from invalidating that proof.
func (s *Store) resolveScheduledActionPreDispatchCancellation(id, expectedCode string, now time.Time) (ScheduledAction, error) {
	var action ScheduledAction
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil {
			return err
		}
		if action.Status != ScheduledActionUnknown || action.FailureCode != expectedCode || action.CurrentJobID == "" {
			return ErrScheduledActionState
		}
		var job Job
		if err := getJSON(tx.Bucket(bucketJobs), action.CurrentJobID, &job); err != nil || job.Status != JobCancelled {
			return ErrScheduledActionState
		}
		previous := action
		switch expectedCode {
		case ScheduledActionFailureCancelAmbiguous:
			action.Status = ScheduledActionCancelled
			action.LastOutcome = "cancelled"
			action.FailureCode = ""
		case ScheduledActionFailureDeliveryTimeoutAmbiguous:
			action.Status = ScheduledActionFailed
			action.LastOutcome = "not_dispatched"
			action.FailureCode = ScheduledActionFailureDeliveryWindowExpired
		default:
			return ErrScheduledActionState
		}
		action.UpdatedAt = now.UTC()
		return putScheduledActionTx(tx, &previous, action)
	})
	return scheduledActionResponse(action), err
}

func (s *Store) DueScheduledActions(now time.Time, limit int) ([]ScheduledAction, error) {
	if limit < 1 || limit > maximumScheduledActionBatch {
		limit = maximumScheduledActionBatch
	}
	result := make([]ScheduledAction, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketScheduledChecks).Cursor()
		for key, value := cursor.First(); key != nil && len(result) < limit; key, value = cursor.Next() {
			checkAt, valid := scheduledActionCheckTime(key)
			if !valid {
				return errors.New("scheduled action check index is invalid")
			}
			if checkAt.After(now) {
				break
			}
			id := string(value)
			var action ScheduledAction
			if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil || !validStoredScheduledAction(action, id) || !action.NextCheckAt.Equal(checkAt) {
				return errors.New("scheduled action check index is inconsistent")
			}
			result = append(result, action)
		}
		return nil
	})
	return result, err
}

// AdvanceScheduledAction reconciles a due preview or a dispatched occurrence.
// It returns needsDispatch only for an active occurrence that is inside its
// delivery window and does not already own a job.
func (s *Store) AdvanceScheduledAction(id string, now time.Time) (ScheduledAction, bool, error) {
	var action ScheduledAction
	needsDispatch := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil {
			return err
		}
		if action.NextCheckAt.After(now) || scheduledActionTerminal(action.Status) {
			return nil
		}
		previous := action
		if action.Status == ScheduledActionPreview {
			if !now.Before(action.PreviewExpiresAt) {
				action.Status = ScheduledActionExpired
				action.LastOutcome = "preview_expired"
				action.UpdatedAt = now.UTC()
				return putScheduledActionTx(tx, &previous, action)
			}
			action.NextCheckAt = action.PreviewExpiresAt
			return putScheduledActionTx(tx, &previous, action)
		}
		if action.Status != ScheduledActionActive {
			return errors.New("scheduled action has an invalid active state")
		}
		if action.CurrentJobID == "" {
			changed := false
			for occurrence := action.CompletedOccurrences + action.SkippedOccurrences + 1; occurrence <= action.Occurrences; occurrence++ {
				due, valid := scheduledActionOccurrenceAt(action, occurrence)
				if !valid {
					return errors.New("scheduled action recurrence is invalid")
				}
				action.NextRunAt = due
				deadline := due.Add(time.Duration(action.DeliveryWindowSeconds) * time.Second)
				if !now.Before(deadline) {
					action.SkippedOccurrences++
					action.LastOutcome = "occurrence_skipped"
					changed = true
					continue
				}
				if due.After(now) {
					action.NextCheckAt = due
					action.UpdatedAt = now.UTC()
					return putScheduledActionTx(tx, &previous, action)
				}
				needsDispatch = true
				if !changed {
					return nil
				}
				action.NextCheckAt = now.UTC()
				action.UpdatedAt = now.UTC()
				return putScheduledActionTx(tx, &previous, action)
			}
			action.Status = ScheduledActionCompleted
			action.LastOutcome = "completed_with_skips"
			action.UpdatedAt = now.UTC()
			return putScheduledActionTx(tx, &previous, action)
		}
		var job Job
		if err := getJSON(tx.Bucket(bucketJobs), action.CurrentJobID, &job); err != nil {
			action.Status = ScheduledActionUnknown
			action.FailureCode = ScheduledActionFailureJobMissing
			action.LastOutcome = "unknown"
			action.UpdatedAt = now.UTC()
			return putScheduledActionTx(tx, &previous, action)
		}
		if !terminalJobStatus(job.Status) {
			if !now.Before(action.CurrentExpiresAt) {
				wasStarted := job.Status == JobAssigned || job.Status == JobRunning
				if _, err := cancelJobTx(tx, s, job, now); err != nil {
					return err
				}
				if wasStarted {
					action.Status = ScheduledActionUnknown
					action.LastOutcome = "unknown"
					action.FailureCode = ScheduledActionFailureDeliveryTimeoutAmbiguous
				} else {
					action.Status = ScheduledActionFailed
					action.LastOutcome = "not_dispatched"
					action.FailureCode = ScheduledActionFailureDeliveryWindowExpired
				}
				action.UpdatedAt = now.UTC()
				return putScheduledActionTx(tx, &previous, action)
			}
			action.NextCheckAt = minTime(now.Add(scheduledActionPendingPoll), action.CurrentExpiresAt)
			action.WaitingReason = "job_pending"
			action.UpdatedAt = now.UTC()
			return putScheduledActionTx(tx, &previous, action)
		}
		action.LastJobID = job.ID
		action.CurrentJobID = ""
		action.CurrentDueAt = time.Time{}
		action.CurrentExpiresAt = time.Time{}
		action.WaitingReason = ""
		switch job.Status {
		case JobCompleted:
			action.CompletedOccurrences++
			action.LastOutcome = "completed"
		case JobFailed:
			if job.FailureCode == FailureExecutionStateAmbiguous || job.FailureCode == FailureExecutionTimeoutAmbiguous {
				action.Status = ScheduledActionUnknown
				action.LastOutcome = "unknown"
				action.FailureCode = job.FailureCode
				action.UpdatedAt = now.UTC()
				return putScheduledActionTx(tx, &previous, action)
			}
			action.Status = ScheduledActionFailed
			action.LastOutcome = "failed"
			action.FailureCode = job.FailureCode
			action.UpdatedAt = now.UTC()
			return putScheduledActionTx(tx, &previous, action)
		case JobCancelled:
			// The owner or an operator can cancel the linked job through the
			// ordinary job API instead of the schedule API. Once assignment began,
			// that path cannot prove whether the external side effect started, so
			// retain the same no-replay ambiguity guarantee as schedule-aware
			// cancellation. A never-assigned queued job is definitively safe.
			if !job.AssignedAt.IsZero() || !job.StartedAt.IsZero() {
				action.Status = ScheduledActionUnknown
				action.LastOutcome = "unknown"
				action.FailureCode = ScheduledActionFailureCancelAmbiguous
			} else {
				action.Status = ScheduledActionFailed
				action.LastOutcome = "cancelled"
				action.FailureCode = ScheduledActionFailureJobCancelled
			}
			action.UpdatedAt = now.UTC()
			return putScheduledActionTx(tx, &previous, action)
		}
		processed := action.CurrentOccurrence
		action.CurrentOccurrence = 0
		if processed >= action.Occurrences {
			action.Status = ScheduledActionCompleted
			if action.SkippedOccurrences > 0 {
				action.LastOutcome = "completed_with_skips"
			}
			action.UpdatedAt = now.UTC()
			return putScheduledActionTx(tx, &previous, action)
		}
		next := processed + 1
		for next <= action.Occurrences {
			due, valid := scheduledActionOccurrenceAt(action, next)
			if !valid {
				return errors.New("scheduled action recurrence is invalid")
			}
			if now.Before(due.Add(time.Duration(action.DeliveryWindowSeconds) * time.Second)) {
				action.NextRunAt = due
				action.NextCheckAt = due
				if !due.After(now) {
					action.NextCheckAt = now.UTC()
				}
				action.UpdatedAt = now.UTC()
				return putScheduledActionTx(tx, &previous, action)
			}
			action.SkippedOccurrences++
			next++
		}
		action.Status = ScheduledActionCompleted
		action.LastOutcome = "completed_with_skips"
		action.UpdatedAt = now.UTC()
		return putScheduledActionTx(tx, &previous, action)
	})
	return action, needsDispatch, err
}

func scheduledActionOccurrenceAt(action ScheduledAction, occurrence int) (time.Time, bool) {
	if occurrence < 1 || occurrence > action.Occurrences || action.StartAt.IsZero() {
		return time.Time{}, false
	}
	if occurrence == 1 {
		return action.StartAt, true
	}
	if action.RepeatEverySeconds < minimumScheduledIntervalSeconds {
		return time.Time{}, false
	}
	spanOccurrences := int64(occurrence - 1)
	if action.RepeatEverySeconds > maximumScheduledHorizonSeconds/spanOccurrences {
		return time.Time{}, false
	}
	return action.StartAt.Add(time.Duration(spanOccurrences*action.RepeatEverySeconds) * time.Second), true
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func (s *Store) DeferScheduledAction(id, reason string, now time.Time) (ScheduledAction, error) {
	var action ScheduledAction
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil {
			return err
		}
		if action.Status != ScheduledActionActive || action.CurrentJobID != "" || action.NextRunAt.After(now) {
			return ErrScheduledActionState
		}
		previous := action
		deadline := action.NextRunAt.Add(time.Duration(action.DeliveryWindowSeconds) * time.Second)
		if !now.Before(deadline) {
			action.Status = ScheduledActionFailed
			action.LastOutcome = "not_dispatched"
			action.FailureCode = ScheduledActionFailureDeliveryWindowExpired
		} else {
			action.WaitingReason = cleanLabel(reason, 80)
			action.NextCheckAt = minTime(now.Add(time.Second), deadline)
		}
		action.UpdatedAt = now.UTC()
		return putScheduledActionTx(tx, &previous, action)
	})
	return action, err
}

func (s *Store) FailScheduledAction(id, code, outcome string, now time.Time) (ScheduledAction, error) {
	var action ScheduledAction
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil {
			return err
		}
		if scheduledActionTerminal(action.Status) {
			return ErrScheduledActionState
		}
		previous := action
		action.Status = ScheduledActionFailed
		action.FailureCode = cleanLabel(code, 100)
		action.LastOutcome = cleanLabel(outcome, 80)
		action.UpdatedAt = now.UTC()
		return putScheduledActionTx(tx, &previous, action)
	})
	return action, err
}

func adapterEnabledTx(tx *bolt.Tx, adapterUID string) (bool, error) {
	var control AdapterControl
	err := getJSON(tx.Bucket(bucketAdapterControls), adapterUID, &control)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if control.Schema != AdapterControlV1 || control.AdapterUID != adapterUID || control.ActorSubject == "" || control.UpdatedAt.IsZero() {
		return false, errors.New("adapter control record is invalid")
	}
	return control.Enabled, nil
}

func (s *Store) DispatchScheduledAction(id string, request SubmitRequest, maxQueued int, now time.Time) (ScheduledAction, Job, error) {
	job, err := prepareJob(request, "", "", now)
	if err != nil {
		return ScheduledAction{}, Job{}, err
	}
	var action ScheduledAction
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil {
			return err
		}
		if action.Status != ScheduledActionActive || action.CurrentJobID != "" || action.NextRunAt.After(now) {
			return ErrScheduledActionState
		}
		if !now.Before(action.NextRunAt.Add(time.Duration(action.DeliveryWindowSeconds) * time.Second)) {
			return ErrScheduledActionState
		}
		record, valid := producerCredentialByHashTx(tx, action.CredentialHash, now)
		if !valid || !scheduledActionAllowed(record, action) || request.OwnerSubject != action.OwnerSubject || request.TenantID != action.TenantID ||
			!scheduledActionDispatchMatches(action, record, request) {
			return ErrScheduledActionCredential
		}
		enabled, err := adapterEnabledTx(tx, action.AdapterUID)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrScheduledActionAdapterStopped
		}
		if _, err := s.admitPreparedJobTx(tx, &job, maxQueued, record.ProducerLimits, "", "", now); err != nil {
			return err
		}
		previous := action
		action.CurrentOccurrence = action.CompletedOccurrences + action.SkippedOccurrences + 1
		action.CurrentDueAt = action.NextRunAt
		action.CurrentExpiresAt = action.NextRunAt.Add(time.Duration(action.DeliveryWindowSeconds) * time.Second)
		action.CurrentJobID = job.ID
		action.WaitingReason = "job_pending"
		action.NextCheckAt = minTime(now.Add(scheduledActionPendingPoll), action.CurrentExpiresAt)
		action.UpdatedAt = now.UTC()
		return putScheduledActionTx(tx, &previous, action)
	})
	return action, job, err
}

func scheduledActionDispatchMatches(action ScheduledAction, record TokenRecord, request SubmitRequest) bool {
	expected, err := buildScheduledActionSubmit(action)
	if err != nil || request.ID != "" || request.ContractVersion != expected.ContractVersion || request.Source != expected.Source ||
		request.Priority != expected.Priority || request.MaxAttempts != expected.MaxAttempts || request.Sealed != nil || request.PoolAuthorization != nil ||
		request.AssignmentID != "" || request.AssignmentSecret != "" || request.Pipeline != "" || request.Step != "" || request.ParentID != "" ||
		request.scheduledActionInternal != expected.scheduledActionInternal ||
		!bytes.Equal(request.Payload, expected.Payload) {
		return false
	}
	requirements := request.Requirements
	if requirements.Task != expected.Requirements.Task || requirements.SessionID != expected.Requirements.SessionID ||
		requirements.Provider != expected.Requirements.Provider || requirements.AdapterProfile != expected.Requirements.AdapterProfile ||
		requirements.AdapterPrincipal != action.AdapterPrincipal || requirements.Model != "" || requirements.Reasoning != "" ||
		requirements.AdapterEndpointID != 0 || requirements.AdapterSessionRecovery || requirements.AdapterSessionKey != "" ||
		requirements.AdapterFreshSession || requirements.AdapterEphemeralSession || len(requirements.RequiredTags) != 0 ||
		len(requirements.PreferredNodes) != 0 || requirements.MinFreeVRAM != 0 || requirements.Vision || requirements.Embedding ||
		requirements.InputImageCount != 0 || requirements.InputImageBytes != 0 || requirements.InputImageMaxBytes != 0 ||
		len(requirements.InputImageMediaTypes) != 0 || requirements.InputAudioBytes != 0 || requirements.InputAudioDurationMS != 0 ||
		requirements.InputAudioMediaType != "" || requirements.MaxCostUSD != 0 {
		return false
	}
	if requirements.Group != "" && !containsFold(record.Groups, requirements.Group) {
		return false
	}
	return requirements.Egress == "" || (record.ProducerLimits.Egress == "local_only" && requirements.Egress == "local_only")
}

func buildScheduledActionSubmit(action ScheduledAction) (SubmitRequest, error) {
	envelope := scheduledAdapterActionPayload{
		Schema: ScheduledActionPayloadV1, ScheduleID: action.ID,
		AdapterUID: action.AdapterUID,
		Occurrence: action.CompletedOccurrences + action.SkippedOccurrences + 1,
		ActionKind: action.ActionKind, DestinationRef: action.DestinationRef, PayloadRef: action.PayloadRef,
		ScheduledFor: action.NextRunAt, ExpiresAt: action.NextRunAt.Add(time.Duration(action.DeliveryWindowSeconds) * time.Second),
	}
	payload, err := json.Marshal(map[string]interface{}{
		"source":   "scheduled-action",
		"prompt":   "Execute the authorized scheduled adapter action reference.",
		"output":   map[string]interface{}{"mode": "json", "max_bytes": 4096},
		"metadata": map[string]interface{}{"contextbridge_scheduled_action": envelope},
	})
	if err != nil {
		return SubmitRequest{}, err
	}
	return SubmitRequest{
		ContractVersion: JobContractV1, TenantID: action.TenantID, Source: "scheduled-action",
		Requirements:            Requirements{Task: scheduledActionTask, Provider: "adapter", AdapterProfile: action.AdapterProfile, SessionID: action.ID},
		Payload:                 payload,
		Priority:                action.Priority,
		MaxAttempts:             1,
		scheduledActionInternal: true,
	}, nil
}

func (r *Relay) scheduledAdapterState(uid string, now time.Time) (enabled, available bool, err error) {
	enabled, err = r.store.AdapterEnabled(uid)
	if err != nil || !enabled {
		return enabled, false, err
	}
	r.adapterPresenceMu.Lock()
	r.pruneAdapterPresencesLocked(now)
	for _, presence := range r.adapterPresences {
		if presence.AdapterUID == uid && presence.Enabled && presence.Available && presence.LeaseExpiresAt.After(now) &&
			containsFold(presence.Capabilities, "scheduled-action") {
			available = true
			break
		}
	}
	r.adapterPresenceMu.Unlock()
	return enabled, available, nil
}

func (r *Relay) runScheduledActions(now time.Time) {
	actions, err := r.store.DueScheduledActions(now, maximumScheduledActionBatch)
	if err != nil {
		r.logger.Printf("scheduled action scan failed: %v", err)
		return
	}
	for _, candidate := range actions {
		action, dispatch, advanceErr := r.store.AdvanceScheduledAction(candidate.ID, now)
		if advanceErr == nil {
			action = r.finalizeScheduledActionCancelledJob(action, now)
		}
		if advanceErr != nil || !dispatch {
			if advanceErr != nil {
				r.logger.Printf("scheduled action %s reconciliation failed: %v", candidate.ID, advanceErr)
			}
			continue
		}
		record, valid := r.store.ProducerCredentialByHash(action.CredentialHash, now)
		if !valid || !scheduledActionAllowed(record, action) {
			_, _ = r.store.FailScheduledAction(action.ID, ScheduledActionFailureCredentialInactive, "not_dispatched", now)
			continue
		}
		enabled, available, stateErr := r.scheduledAdapterState(action.AdapterUID, now)
		if stateErr != nil {
			r.logger.Printf("scheduled action %s adapter state failed: %v", action.ID, stateErr)
			_, _ = r.store.DeferScheduledAction(action.ID, "adapter_state_unavailable", now)
			continue
		}
		if !enabled {
			_, _ = r.store.DeferScheduledAction(action.ID, "adapter_disabled", now)
			continue
		}
		if !available {
			_, _ = r.store.DeferScheduledAction(action.ID, "adapter_unavailable", now)
			continue
		}
		request, buildErr := buildScheduledActionSubmit(action)
		if buildErr != nil {
			_, _ = r.store.FailScheduledAction(action.ID, ScheduledActionFailureEnvelopeFailed, "not_dispatched", now)
			continue
		}
		prepared, _, admissionErr := r.prepareAdmission(request, record, admissionScheduledAction)
		if admissionErr != nil {
			_, _ = r.store.FailScheduledAction(action.ID, ScheduledActionFailurePolicyDenied, "not_dispatched", now)
			continue
		}
		// AdapterPrincipal is relay-managed routing authority. A producer could
		// never submit it through the ordinary job endpoint; this internal path
		// binds the already validated profile to the target's administrator-
		// issued v2 principal only after normal admission policy has succeeded.
		prepared.Requirements.AdapterPrincipal = action.AdapterPrincipal
		if !r.beginAdmission() {
			_, _ = r.store.DeferScheduledAction(action.ID, "relay_stopping", now)
			continue
		}
		_, job, dispatchErr := r.store.DispatchScheduledAction(action.ID, prepared, r.cfg.MaxQueuedJobs, now)
		r.endAdmission()
		if dispatchErr != nil {
			// Another scheduler pass may have won the same occurrence between the
			// due scan and this transaction. Its durable state is authoritative;
			// never turn that harmless stale observation into a terminal failure.
			if errors.Is(dispatchErr, ErrScheduledActionState) {
				continue
			}
			if errors.Is(dispatchErr, ErrQueueFull) || errors.Is(dispatchErr, ErrOwnerQueueCapacity) || errors.Is(dispatchErr, ErrOwnerRateCapacity) || errors.Is(dispatchErr, ErrScheduledActionAdapterStopped) {
				_, _ = r.store.DeferScheduledAction(action.ID, "admission_temporarily_unavailable", now)
				continue
			}
			if errors.Is(dispatchErr, ErrScheduledActionCredential) {
				_, _ = r.store.FailScheduledAction(action.ID, ScheduledActionFailureCredentialInactive, "not_dispatched", now)
				continue
			}
			r.logger.Printf("scheduled action %s dispatch failed: %v", action.ID, dispatchErr)
			_, _ = r.store.FailScheduledAction(action.ID, ScheduledActionFailureDispatchFailed, "not_dispatched", now)
			continue
		}
		_ = r.store.AddEvent(Event{Kind: "scheduled_action.dispatched", Message: "Scheduled adapter action dispatched", JobID: job.ID, Data: map[string]interface{}{"scheduled_action_id": action.ID}})
		r.signalDispatch()
	}
}

func (r *Relay) handleScheduledActionPreview(w http.ResponseWriter, req *http.Request) {
	var input ScheduledActionRequest
	if err := decodeJSON(req.Body, &input, 32<<10); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "scheduled_action.invalid_request", err)
		return
	}
	record, _ := tokenRecord(req.Context())
	now := time.Now().UTC()
	action, err := normalizeScheduledActionRequest(input, record, now)
	if err != nil {
		status := http.StatusUnprocessableEntity
		code := "scheduled_action.invalid_request"
		if errors.Is(err, ErrScheduledActionScope) || errors.Is(err, ErrTenantScopeForbidden) || errors.Is(err, ErrPriorityScopeForbidden) {
			status = http.StatusForbidden
			code = "scheduled_action.scope_forbidden"
		}
		writeErrorCode(w, status, code, err)
		return
	}
	created, err := r.store.CreateScheduledActionPreview(action, now)
	if err != nil {
		status := http.StatusInternalServerError
		code := "scheduled_action.store_failed"
		if errors.Is(err, ErrScheduledActionCapacity) {
			status = http.StatusTooManyRequests
			code = "scheduled_action.capacity"
		} else if errors.Is(err, ErrScheduledActionCredential) {
			status = http.StatusForbidden
			code = "scheduled_action.scope_forbidden"
		}
		writeErrorCode(w, status, code, err)
		return
	}
	_ = r.store.AddEvent(Event{Kind: "scheduled_action.previewed", Message: "Scheduled adapter action previewed", Data: map[string]interface{}{"scheduled_action_id": created.ID}})
	writeJSON(w, http.StatusCreated, created)
}

func (r *Relay) handleScheduledActions(w http.ResponseWriter, req *http.Request) {
	record, _ := tokenRecord(req.Context())
	status := strings.TrimSpace(req.URL.Query().Get("status"))
	if status != "" && status != ScheduledActionPreview && status != ScheduledActionActive && !scheduledActionTerminal(status) {
		writeErrorCode(w, http.StatusBadRequest, "scheduled_action.invalid_status", errors.New("scheduled action status is invalid"))
		return
	}
	limit := maximumScheduledActionList
	if raw := strings.TrimSpace(req.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumScheduledActionList {
			writeErrorCode(w, http.StatusBadRequest, "scheduled_action.invalid_limit", fmt.Errorf("limit must be 1 to %d", maximumScheduledActionList))
			return
		}
		limit = parsed
	}
	list, err := r.store.ListScheduledActionsForCredential(record.AuthHash, status, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (r *Relay) handleScheduledAction(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimSpace(req.PathValue("id"))
	if !validScheduledActionID(id) {
		writeErrorCode(w, http.StatusNotFound, "scheduled_action.not_found", errors.New("scheduled action not found"))
		return
	}
	record, _ := tokenRecord(req.Context())
	action, err := r.store.GetScheduledActionForCredential(id, record.AuthHash)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeErrorCode(w, http.StatusNotFound, "scheduled_action.not_found", errors.New("scheduled action not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, action)
}

func (r *Relay) handleScheduledActionConfirm(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimSpace(req.PathValue("id"))
	if !validScheduledActionID(id) {
		writeErrorCode(w, http.StatusNotFound, "scheduled_action.not_found", errors.New("scheduled action not found"))
		return
	}
	record, _ := tokenRecord(req.Context())
	action, err := r.store.ConfirmScheduledAction(id, record.AuthHash, time.Now().UTC())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeErrorCode(w, http.StatusNotFound, "scheduled_action.not_found", errors.New("scheduled action not found"))
			return
		}
		if errors.Is(err, ErrScheduledActionState) {
			writeErrorCode(w, http.StatusConflict, "scheduled_action.confirmation_expired", err)
			return
		}
		if errors.Is(err, ErrScheduledActionCredential) {
			writeErrorCode(w, http.StatusForbidden, "scheduled_action.scope_forbidden", err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = r.store.AddEvent(Event{Kind: "scheduled_action.confirmed", Message: "Scheduled adapter action confirmed", Data: map[string]interface{}{"scheduled_action_id": action.ID}})
	r.signalDispatch()
	writeJSON(w, http.StatusOK, action)
}

func (r *Relay) handleScheduledActionCancel(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimSpace(req.PathValue("id"))
	if !validScheduledActionID(id) {
		writeErrorCode(w, http.StatusNotFound, "scheduled_action.not_found", errors.New("scheduled action not found"))
		return
	}
	record, _ := tokenRecord(req.Context())
	now := time.Now().UTC()
	action, err := r.store.CancelScheduledAction(id, record.AuthHash, now)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeErrorCode(w, http.StatusNotFound, "scheduled_action.not_found", errors.New("scheduled action not found"))
			return
		}
		if errors.Is(err, ErrScheduledActionState) {
			writeErrorCode(w, http.StatusConflict, "scheduled_action.final", err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	action = r.finalizeScheduledActionCancelledJob(action, now)
	event := Event{Kind: "scheduled_action.cancelled", Message: "Scheduled adapter action cancelled", Data: map[string]interface{}{"scheduled_action_id": action.ID}}
	if action.Status != ScheduledActionCancelled {
		event.Kind = "scheduled_action.reconciled"
		event.Message = "Scheduled adapter action reconciled before cancellation"
	}
	_ = r.store.AddEvent(event)
	writeJSON(w, http.StatusOK, action)
}

func (r *Relay) finalizeScheduledActionCancelledJob(action ScheduledAction, now time.Time) ScheduledAction {
	if action.CurrentJobID == "" {
		return action
	}
	job, err := r.store.GetJob(action.CurrentJobID)
	if err != nil || job.Status != JobCancelled {
		return action
	}
	executionMayHaveStarted := action.FailureCode == ScheduledActionFailureCancelAmbiguous ||
		action.FailureCode == ScheduledActionFailureDeliveryTimeoutAmbiguous
	reservationState := r.finalizeCancelledJob(job, executionMayHaveStarted)
	if reservationState == workerReservationPreDispatch && executionMayHaveStarted {
		resolved, resolveErr := r.store.resolveScheduledActionPreDispatchCancellation(action.ID, action.FailureCode, now)
		if resolveErr == nil {
			return resolved
		}
		if !errors.Is(resolveErr, ErrScheduledActionState) {
			r.logger.Printf("scheduled action %s pre-dispatch cancellation reconciliation failed: %v", action.ID, resolveErr)
		}
	}
	return action
}

func validScheduledActionStatus(status string) bool {
	return status == ScheduledActionPreview || status == ScheduledActionActive || scheduledActionTerminal(status)
}

func pruneScheduledActionsTx(tx *bolt.Tx, cutoff time.Time) (int, error) {
	removed := 0
	cursor := tx.Bucket(bucketScheduledActions).Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var action ScheduledAction
		if err := json.Unmarshal(value, &action); err != nil || !validStoredScheduledAction(action, string(key)) || !validScheduledActionStatus(action.Status) {
			return removed, errors.New("scheduled action record is invalid")
		}
		if !scheduledActionTerminal(action.Status) || !action.UpdatedAt.Before(cutoff) {
			continue
		}
		if !action.NextCheckAt.IsZero() {
			if err := tx.Bucket(bucketScheduledChecks).Delete(scheduledActionCheckKey(action.NextCheckAt, action.ID)); err != nil {
				return removed, err
			}
		}
		if err := cursor.Delete(); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func scheduledActionIndexConsistent(tx *bolt.Tx) error {
	if err := tx.Bucket(bucketScheduledChecks).ForEach(func(key, value []byte) error {
		at, ok := scheduledActionCheckTime(key)
		if !ok || !bytes.Equal(key[8:], value) {
			return errors.New("scheduled action check index is invalid")
		}
		var action ScheduledAction
		if err := getJSON(tx.Bucket(bucketScheduledActions), string(value), &action); err != nil || !action.NextCheckAt.Equal(at) {
			return errors.New("scheduled action check index is inconsistent")
		}
		return nil
	}); err != nil {
		return err
	}
	return tx.Bucket(bucketScheduledActions).ForEach(func(key, value []byte) error {
		var action ScheduledAction
		if err := json.Unmarshal(value, &action); err != nil || !validStoredScheduledAction(action, string(key)) {
			return errors.New("scheduled action record is invalid")
		}
		if action.NextCheckAt.IsZero() {
			return nil
		}
		indexed := tx.Bucket(bucketScheduledChecks).Get(scheduledActionCheckKey(action.NextCheckAt, action.ID))
		if !bytes.Equal(indexed, []byte(action.ID)) {
			return errors.New("scheduled action is missing its check index")
		}
		return nil
	})
}
