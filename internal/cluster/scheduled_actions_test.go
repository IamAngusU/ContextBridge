package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func scheduledTestRefs() (string, string) {
	return "dst_" + strings.Repeat("a", 32), "ref_" + strings.Repeat("b", 32)
}

func scheduledTestLimits(uid string) ScheduledActionLimits {
	destination, _ := scheduledTestRefs()
	return ScheduledActionLimits{
		Schema: ScheduledActionPolicyV1,
		Targets: []ScheduledActionTarget{{
			AdapterUID: uid, AdapterProfile: "message-delivery", AdapterPrincipal: "message-adapter",
			ActionKinds: []string{"message.text"}, DestinationRefs: []string{destination},
		}},
		MaxActive: 3, MaxHorizonSeconds: 3600, MinIntervalSeconds: 60,
		MaxOccurrences: 4, MaxDeliveryWindowSeconds: 300,
	}
}

func scheduledTestRequest(t *testing.T, uid string, start time.Time) ScheduledActionRequest {
	t.Helper()
	destination, payload := scheduledTestRefs()
	return ScheduledActionRequest{
		Schema: ScheduledActionRequestV1, AdapterUID: uid,
		ActionKind: "message.text", DestinationRef: destination, PayloadRef: payload,
		StartAt: start, Timezone: "Europe/Berlin", DeliveryWindowSeconds: 120, Priority: 10,
	}
}

func TestScheduledActionPolicyIsOptInBoundedAndDSTExplicit(t *testing.T) {
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	if err := validateScheduledActionLimits(&limits); err != nil {
		t.Fatal(err)
	}
	if err := validateScheduledActionLimits(&ScheduledActionLimits{}); err == nil {
		t.Fatal("empty scheduled-action authority was accepted")
	}
	invalid := limits
	invalid.Targets = append([]ScheduledActionTarget(nil), limits.Targets...)
	invalid.Targets[0].DestinationRefs = []string{"+491701234567"}
	if err := validateScheduledActionLimits(&invalid); err == nil {
		t.Fatal("a raw telephone number was accepted as a destination reference")
	}
	if err := validateProducerLimits("observer", ProducerLimits{ScheduledActions: &limits}); err == nil {
		t.Fatal("a non-producer credential received scheduled-action authority")
	}
	if err := validateProducerLimits("producer", ProducerLimits{RequireE2EE: true, ScheduledActions: &limits}); err == nil {
		t.Fatal("relay-rendered action payloads were combined with require_e2ee")
	}
	if err := validateProducerLimits("producer", ProducerLimits{Providers: []string{"ollama"}, ScheduledActions: &limits}); err == nil {
		t.Fatal("scheduled adapter authority was combined with a provider allowlist that excludes adapter")
	}

	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 24, 10, 0, 0, 0, time.UTC)
	record := TokenRecord{
		ID: "tok_scheduled", Role: "producer", Subject: "channel-a", AuthHash: strings.Repeat("d", 64),
		ProducerLimits: ProducerLimits{MaxPriority: producerPriorityLimit(20), Providers: []string{"adapter"}, ScheduledActions: normalizeScheduledActionLimits(&limits)},
	}
	request := scheduledTestRequest(t, uid, now.In(location).Add(time.Hour))
	action, err := normalizeScheduledActionRequest(request, record, now)
	if err != nil || action.StartAt.Location() != time.UTC || action.LocalStart == "" || action.Occurrences != 1 {
		t.Fatalf("valid normalized action = %#v, %v", action, err)
	}
	request.TenantID = "invented-capacity-shard"
	if _, err := normalizeScheduledActionRequest(request, record, now); !errors.Is(err, ErrTenantScopeForbidden) {
		t.Fatalf("unscoped producer used a caller-selected tenant to shard capacity: %v", err)
	}
	request.StartAt = now.Add(time.Hour) // Z does not describe Europe/Berlin at this instant.
	request.TenantID = ""
	if _, err := normalizeScheduledActionRequest(request, record, now); err == nil || !strings.Contains(err.Error(), "offset") {
		t.Fatalf("timezone/offset mismatch was accepted: %v", err)
	}
	request = scheduledTestRequest(t, uid, now.In(location).Add(time.Hour))
	request.RepeatEverySeconds = 60
	request.Occurrences = 5
	if _, err := normalizeScheduledActionRequest(request, record, now); err == nil || !strings.Contains(err.Error(), "2 to 4") {
		t.Fatalf("occurrence ceiling was not enforced: %v", err)
	}
	overflowLimits := limits
	overflowLimits.MaxHorizonSeconds = maximumScheduledHorizonSeconds
	overflowLimits.MaxOccurrences = maximumScheduledOccurrences
	record.ProducerLimits.ScheduledActions = normalizeScheduledActionLimits(&overflowLimits)
	request = scheduledTestRequest(t, uid, now.In(location).Add(time.Hour))
	request.RepeatEverySeconds = maximumScheduledHorizonSeconds
	request.Occurrences = maximumScheduledOccurrences
	if _, err := normalizeScheduledActionRequest(request, record, now); err == nil || !strings.Contains(err.Error(), "last occurrence") {
		t.Fatalf("overflowing recurrence was accepted: %v", err)
	}
}

func TestScheduledActionTimezoneRejectsDSTGapAndPreservesFoldChoice(t *testing.T) {
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	limits.MaxHorizonSeconds = 7 * 24 * 60 * 60
	record := TokenRecord{
		ID: "tok_scheduled", Role: "producer", Subject: "channel-a", AuthHash: strings.Repeat("d", 64),
		ProducerLimits: ProducerLimits{MaxPriority: producerPriorityLimit(20), Providers: []string{"adapter"}, ScheduledActions: normalizeScheduledActionLimits(&limits)},
	}

	gapNow := time.Date(2026, time.March, 28, 0, 0, 0, 0, time.UTC)
	for _, localGap := range []string{"2026-03-29T02:30:00+01:00", "2026-03-29T02:30:00+02:00"} {
		start, err := time.Parse(time.RFC3339, localGap)
		if err != nil {
			t.Fatal(err)
		}
		request := scheduledTestRequest(t, uid, start)
		if _, err := normalizeScheduledActionRequest(request, record, gapNow); err == nil || !strings.Contains(err.Error(), "offset") {
			t.Fatalf("nonexistent Europe/Berlin wall time %s was accepted: %v", localGap, err)
		}
	}

	foldNow := time.Date(2026, time.October, 24, 0, 0, 0, 0, time.UTC)
	var starts []time.Time
	for _, localFold := range []string{"2026-10-25T02:30:00+02:00", "2026-10-25T02:30:00+01:00"} {
		start, err := time.Parse(time.RFC3339, localFold)
		if err != nil {
			t.Fatal(err)
		}
		action, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, start), record, foldNow)
		if err != nil {
			t.Fatalf("valid Europe/Berlin fold choice %s was rejected: %v", localFold, err)
		}
		if action.LocalStart != localFold || action.Timezone != "Europe/Berlin" {
			t.Fatalf("fold choice was not preserved: %#v", action)
		}
		starts = append(starts, action.StartAt)
	}
	if starts[1].Sub(starts[0]) != time.Hour {
		t.Fatalf("the two repeated wall-clock instants collapsed: %s and %s", starts[0], starts[1])
	}
}

func TestScheduledActionPreviewConfirmationIsolationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	token, _, err := store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	record, ok := store.Authenticate(token)
	if !ok {
		t.Fatal("new producer credential did not authenticate")
	}
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	action, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(time.Minute)), record, now)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateScheduledActionPreview(action, now)
	if err != nil || preview.CredentialHash != "" || preview.Status != ScheduledActionPreview {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	if _, err := store.GetScheduledActionForCredential(preview.ID, strings.Repeat("e", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("another credential observed a preview: %v", err)
	}
	confirmed, err := store.ConfirmScheduledAction(preview.ID, record.AuthHash, now.Add(time.Second))
	if err != nil || confirmed.Status != ScheduledActionActive || !confirmed.PreviewExpiresAt.IsZero() {
		t.Fatalf("confirmed = %#v, %v", confirmed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.GetScheduledActionForCredential(preview.ID, record.AuthHash)
	if err != nil || restored.Status != ScheduledActionActive || restored.NextRunAt.IsZero() {
		t.Fatalf("restart lost the active action: %#v, %v", restored, err)
	}
	if err := store.db.View(scheduledActionIndexConsistent); err != nil {
		t.Fatalf("restart left an inconsistent due index: %v", err)
	}
}

func TestScheduledActionPreviewRevalidatesCredentialInWriteTransaction(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	token, tokenRecord, err := store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Authenticate(token)
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	action, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(time.Minute)), record, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RevokeTokenWithHash(tokenRecord.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateScheduledActionPreview(action, now); !errors.Is(err, ErrScheduledActionCredential) {
		t.Fatalf("revoked credential created a preview after authentication: %v", err)
	}
}

func TestScheduledActionWhoAmIAdvertisesOnlyOptedInAuthority(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	scoped, _, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, _, err := relay.store.CreateTokenWithLimits("producer", "channel-b", nil, time.Hour, ProducerLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, token string
		want        bool
	}{{"scoped", scoped, true}, {"ordinary", ordinary, false}} {
		t.Run(test.name, func(t *testing.T) {
			response := adapterPresenceRequest(t, relay, http.MethodGet, "/v1/cluster/whoami", test.token, nil)
			var identity struct {
				Permissions []string `json:"permissions"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &identity) != nil {
				t.Fatalf("whoami: %d %s", response.Code, response.Body.String())
			}
			got := contains(identity.Permissions, "scheduled-actions:write-own")
			if got != test.want {
				t.Fatalf("scheduled action permission = %t, want %t: %#v", got, test.want, identity.Permissions)
			}
		})
	}
}

func TestScheduledActionHTTPDispatchIsScopedAtomicAndContentMinimized(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin,
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	uid := adapterPresenceUID(relay.authority.ClusterID, "channel-a", "message-primary")
	limits := scheduledTestLimits(uid)
	token, tokenRecord, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour,
		ProducerLimits{Providers: []string{"adapter"}, MaxQueuedJobs: 4, MaxJobsPerHour: 10, MaxPriority: producerPriorityLimit(20), ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	presenceToken, _, err := relay.store.CreateToken("producer", "channel-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterHeartbeat{
		Schema: AdapterPresenceV1, AdapterID: "message-primary", InstanceID: "host-a", DisplayName: "Message adapter",
		Kind: "control", Version: "1.0.0", State: "ready", Capabilities: []string{"scheduled-action"}, LeaseSeconds: 60,
	}
	if response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", presenceToken, heartbeat); response.Code != http.StatusOK {
		t.Fatalf("adapter heartbeat: %d %s", response.Code, response.Body.String())
	}
	identityResponse := adapterPresenceRequest(t, relay, http.MethodGet, "/v1/cluster/whoami", presenceToken, nil)
	var presenceIdentity struct {
		Permissions []string `json:"permissions"`
	}
	if identityResponse.Code != http.StatusOK || json.Unmarshal(identityResponse.Body.Bytes(), &presenceIdentity) != nil || contains(presenceIdentity.Permissions, "scheduled-actions:write-own") {
		t.Fatalf("least-privilege presence credential gained scheduled-action authority: %d %s", identityResponse.Code, identityResponse.Body.String())
	}
	location, _ := time.LoadLocation("Europe/Berlin")
	input := scheduledTestRequest(t, uid, time.Now().In(location).Add(time.Minute))
	response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/scheduled-actions/preview", token, input)
	if response.Code != http.StatusCreated {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "credential_hash") || strings.Contains(response.Body.String(), "adapter_principal") || strings.Contains(response.Body.String(), "message-adapter") {
		t.Fatalf("preview disclosed internal credential or routing identity: %s", response.Body.String())
	}
	for _, zeroField := range []string{"confirmed_at", "current_due_at", "current_expires_at"} {
		if strings.Contains(response.Body.String(), `"`+zeroField+`"`) {
			t.Fatalf("preview serialized zero lifecycle field %s: %s", zeroField, response.Body.String())
		}
	}
	var preview ScheduledAction
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	other, _, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour,
		ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	if denied := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/scheduled-actions/"+preview.ID+"/confirm", other, map[string]interface{}{}); denied.Code != http.StatusNotFound {
		t.Fatalf("another credential confirmed the preview: %d %s", denied.Code, denied.Body.String())
	}
	response = adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/scheduled-actions/"+preview.ID+"/confirm", token, map[string]interface{}{})
	if response.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", response.Code, response.Body.String())
	}
	forceScheduledActionDue(t, relay.store, preview.ID, time.Now().UTC().Add(-time.Second))
	relay.runScheduledActions(time.Now().UTC())
	record, ok := relay.store.Authenticate(token)
	if !ok || record.ID != tokenRecord.ID {
		t.Fatal("producer unexpectedly lost authority")
	}
	dispatched, err := relay.store.GetScheduledActionForCredential(preview.ID, record.AuthHash)
	if err != nil || dispatched.CurrentJobID == "" || dispatched.CurrentOccurrence != 1 {
		t.Fatalf("due action was not dispatched: %#v, %v", dispatched, err)
	}
	job, err := relay.store.GetJob(dispatched.CurrentJobID)
	if err != nil || job.MaxAttempts != 1 || job.Requirements.Provider != "adapter" || job.Requirements.AdapterProfile != "message-delivery" || job.Requirements.AdapterPrincipal != "message-adapter" || job.Requirements.Task != "scheduled_action" {
		t.Fatalf("scheduled adapter job = %#v, %v", job, err)
	}
	if strings.Contains(string(job.Payload), "+49") || strings.Contains(string(job.Payload), "hello") {
		t.Fatalf("scheduled payload contained a raw destination or message: %s", job.Payload)
	}
	var payload struct {
		Metadata struct {
			Action scheduledAdapterActionPayload `json:"contextbridge_scheduled_action"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.Metadata.Action.ScheduleID != preview.ID || payload.Metadata.Action.AdapterUID != uid || payload.Metadata.Action.PayloadRef == "" {
		t.Fatalf("adapter envelope = %#v, %v", payload, err)
	}
	if _, _, err := relay.store.DispatchScheduledAction(preview.ID, SubmitRequest{}, 20, time.Now().UTC()); err == nil {
		t.Fatal("the same occurrence was dispatched twice")
	}
}

func TestScheduledActionTaskCannotBeSubmittedDirectly(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	token, _, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}})
	if err != nil {
		t.Fatal(err)
	}
	record, ok := relay.store.Authenticate(token)
	if !ok {
		t.Fatal("producer credential did not authenticate")
	}
	forged, _, err := relay.prepareAdmission(SubmitRequest{
		Source: "forged-schedule", Requirements: Requirements{Task: "scheduled_action", Provider: "adapter", AdapterProfile: "message-delivery"},
		Payload: json.RawMessage(`{"prompt":"bypass confirmation"}`), MaxAttempts: 1,
	}, record, admissionSubmit)
	if admissionErrorCode(err) != AdmissionCodeTaskReserved || forged.Requirements.Task != "" {
		t.Fatalf("producer forged relay-internal scheduled action: %#v %v", forged, err)
	}
	if _, err := relay.store.CreateJob(SubmitRequest{
		Requirements: Requirements{Task: scheduledActionTask, Provider: "adapter", AdapterProfile: "message-delivery"},
		Payload:      json.RawMessage(`{"prompt":"bypass store admission"}`),
	}); !errors.Is(err, ErrScheduledActionTaskReserved) {
		t.Fatalf("lower store boundary accepted a forged scheduled action: %v", err)
	}
	internal, err := buildScheduledActionSubmit(ScheduledAction{
		ID: "sact_" + strings.Repeat("a", 32), AdapterProfile: "message-delivery",
		ActionKind: "message.text", DestinationRef: "dst_" + strings.Repeat("b", 32),
		PayloadRef: "ref_" + strings.Repeat("c", 32), NextRunAt: time.Now().UTC(), DeliveryWindowSeconds: 60,
	})
	if err != nil || !internal.scheduledActionInternal {
		t.Fatalf("internal scheduled action did not receive its in-memory capability: %#v %v", internal, err)
	}
	encoded, err := json.Marshal(internal)
	if err != nil || strings.Contains(string(encoded), "scheduledActionInternal") || strings.Contains(string(encoded), "scheduled_action_internal") {
		t.Fatalf("internal scheduled-action capability reached JSON: %s, %v", encoded, err)
	}
	var roundTrip SubmitRequest
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.scheduledActionInternal {
		t.Fatal("JSON round trip forged the internal scheduled-action capability")
	}
	if _, err := relay.store.CreateJob(roundTrip); !errors.Is(err, ErrScheduledActionTaskReserved) {
		t.Fatalf("serialized internal envelope bypassed the store boundary: %v", err)
	}
	internal.Requirements.Task = "generation"
	if _, err := relay.store.CreateJob(internal); err == nil || !strings.Contains(err.Error(), "exact scheduled_action task") {
		t.Fatalf("internal capability was accepted for another task: %v", err)
	}
}

func TestScheduledActionActiveLimitCannotBeBypassedWithAnotherCredential(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	limits.MaxActive = 1
	firstToken, _, err := store.CreateTokenWithLimits("producer", "same-owner", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := store.CreateTokenWithLimits("producer", "same-owner", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := store.Authenticate(firstToken)
	second, _ := store.Authenticate(secondToken)
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	firstAction, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(time.Minute)), first, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateScheduledActionPreview(firstAction, now); err != nil {
		t.Fatal(err)
	}
	secondAction, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(2*time.Minute)), second, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateScheduledActionPreview(secondAction, now); !errors.Is(err, ErrScheduledActionCapacity) {
		t.Fatalf("a second credential bypassed the owner/tenant limit: %v", err)
	}
}

func TestScheduledActionHistoryLimitCannotBeBypassedWithAnotherCredential(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	firstToken, _, err := store.CreateTokenWithLimits("producer", "same-owner", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := store.CreateTokenWithLimits("producer", "same-owner", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := store.Authenticate(firstToken)
	second, _ := store.Authenticate(secondToken)
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	template, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(time.Minute)), first, now)
	if err != nil {
		t.Fatal(err)
	}
	template.Status = ScheduledActionCancelled
	template.CreatedAt = now.Add(-time.Hour)
	template.UpdatedAt = template.CreatedAt
	if err := store.db.Update(func(tx *bolt.Tx) error {
		for index := 0; index < maximumScheduledActionRecordsPerOwner; index++ {
			record := template
			record.ID = fmt.Sprintf("sact_%032x", index+1)
			if err := putJSON(tx.Bucket(bucketScheduledActions), record.ID, record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	request, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(2*time.Minute)), second, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateScheduledActionPreview(request, now); !errors.Is(err, ErrScheduledActionCapacity) {
		t.Fatalf("another credential bypassed the owner history limit: %v", err)
	}
}

func TestScheduledActionCancelAfterPossibleSideEffectIsUnknown(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	token, _, err := store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Authenticate(token)
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	input, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(time.Minute)), record, now)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateScheduledActionPreview(input, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmScheduledAction(preview.ID, record.AuthHash, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	forceScheduledActionDue(t, store, preview.ID, now.Add(-time.Second))
	action, dispatch, err := store.AdvanceScheduledAction(preview.ID, now)
	if err != nil || !dispatch {
		t.Fatalf("action not ready for dispatch: %#v %t %v", action, dispatch, err)
	}
	request, err := buildScheduledActionSubmit(action)
	if err != nil {
		t.Fatal(err)
	}
	request.OwnerSubject = record.Subject
	request.Requirements.AdapterPrincipal = action.AdapterPrincipal
	tampered := request
	tampered.Payload = json.RawMessage(`{"source":"scheduled-action","prompt":"run a different side effect"}`)
	if _, _, err := store.DispatchScheduledAction(action.ID, tampered, 20, now); !errors.Is(err, ErrScheduledActionCredential) {
		t.Fatalf("tampered scheduled payload reached admission: %v", err)
	}
	dispatched, job, err := store.DispatchScheduledAction(action.ID, request, 20, now)
	if err != nil || dispatched.CurrentJobID != job.ID {
		t.Fatalf("dispatch failed: %#v %#v %v", dispatched, job, err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), job.ID, &job); err != nil {
			return err
		}
		job.Status = JobRunning
		job.AssignedNode = "node-a"
		job.UpdatedAt = now
		return putJSON(tx.Bucket(bucketJobs), job.ID, job)
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.CancelScheduledAction(action.ID, record.AuthHash, now.Add(time.Second))
	if err != nil || cancelled.Status != ScheduledActionUnknown || cancelled.FailureCode != ScheduledActionFailureCancelAmbiguous {
		t.Fatalf("possible side effect was reported as safely cancelled: %#v %v", cancelled, err)
	}
}

func TestScheduledActionDirectJobCancellationPreservesAmbiguity(t *testing.T) {
	now := time.Now().UTC()
	destination, payload := scheduledTestRefs()
	for index, test := range []struct {
		name        string
		assignedAt  time.Time
		wantStatus  string
		wantFailure string
	}{
		{name: "queued cancellation is definitive", wantStatus: ScheduledActionFailed, wantFailure: ScheduledActionFailureJobCancelled},
		{name: "assigned cancellation is ambiguous", assignedAt: now.Add(-time.Second), wantStatus: ScheduledActionUnknown, wantFailure: ScheduledActionFailureCancelAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			job := Job{
				ID: fmt.Sprintf("scheduled-direct-cancel-%d", index), Status: JobCancelled,
				AssignedAt: test.assignedAt, CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: now,
			}
			action := ScheduledAction{
				Schema: ScheduledActionV1, ID: fmt.Sprintf("sact_%032x", index+9500), Status: ScheduledActionActive,
				OwnerSubject: "channel-a", CredentialID: "tok_test", CredentialHash: strings.Repeat("d", 64),
				AdapterUID: "adp_" + strings.Repeat("c", 32), AdapterProfile: "message-delivery", AdapterPrincipal: "message-adapter",
				ActionKind: "message.text", DestinationRef: destination, PayloadRef: payload,
				StartAt: now.Add(-time.Minute), LocalStart: now.Add(-time.Minute).Format(time.RFC3339), Timezone: "UTC",
				Occurrences: 1, DeliveryWindowSeconds: 120, CurrentOccurrence: 1, CurrentJobID: job.ID,
				CurrentDueAt: now.Add(-time.Minute), CurrentExpiresAt: now.Add(time.Minute), NextRunAt: now.Add(-time.Minute),
				NextCheckAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Second),
			}
			if err := store.db.Update(func(tx *bolt.Tx) error {
				if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
					return err
				}
				return putScheduledActionTx(tx, nil, action)
			}); err != nil {
				t.Fatal(err)
			}
			result, dispatch, err := store.AdvanceScheduledAction(action.ID, now)
			if err != nil || dispatch || result.Status != test.wantStatus || result.FailureCode != test.wantFailure {
				t.Fatalf("directly cancelled linked job reconciled unsafely: %#v, dispatch=%t, err=%v", result, dispatch, err)
			}
		})
	}
}

func TestScheduledActionCancelReconcilesJustFinishedOccurrence(t *testing.T) {
	for _, test := range []struct {
		name        string
		occurrences int
		wantStatus  string
	}{
		{name: "finished one-shot", occurrences: 1, wantStatus: ScheduledActionCompleted},
		{name: "cancel remaining interval", occurrences: 3, wantStatus: ScheduledActionCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			uid := "adp_" + strings.Repeat("c", 32)
			limits := scheduledTestLimits(uid)
			token, _, err := store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
			if err != nil {
				t.Fatal(err)
			}
			record, _ := store.Authenticate(token)
			location, _ := time.LoadLocation("Europe/Berlin")
			now := time.Now().UTC()
			request := scheduledTestRequest(t, uid, now.In(location).Add(time.Minute))
			if test.occurrences > 1 {
				request.RepeatEverySeconds = 60
				request.Occurrences = test.occurrences
			}
			input, err := normalizeScheduledActionRequest(request, record, now)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := store.CreateScheduledActionPreview(input, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ConfirmScheduledAction(preview.ID, record.AuthHash, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			forceScheduledActionDue(t, store, preview.ID, now.Add(-time.Second))
			action, dispatch, err := store.AdvanceScheduledAction(preview.ID, now)
			if err != nil || !dispatch {
				t.Fatalf("action not ready for dispatch: %#v %t %v", action, dispatch, err)
			}
			jobRequest, err := buildScheduledActionSubmit(action)
			if err != nil {
				t.Fatal(err)
			}
			jobRequest.OwnerSubject = record.Subject
			jobRequest.Requirements.AdapterPrincipal = action.AdapterPrincipal
			_, job, err := store.DispatchScheduledAction(action.ID, jobRequest, 20, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.db.Update(func(tx *bolt.Tx) error {
				if err := getJSON(tx.Bucket(bucketJobs), job.ID, &job); err != nil {
					return err
				}
				if err := deleteQueueEntry(tx, job.ID); err != nil {
					return err
				}
				job.Status = JobCompleted
				job.UpdatedAt = now.Add(time.Second)
				job.FinishedAt = job.UpdatedAt
				return putJSON(tx.Bucket(bucketJobs), job.ID, job)
			}); err != nil {
				t.Fatal(err)
			}
			cancelled, err := store.CancelScheduledAction(action.ID, record.AuthHash, now.Add(2*time.Second))
			if err != nil || cancelled.Status != test.wantStatus || cancelled.CompletedOccurrences != 1 || cancelled.CurrentOccurrence != 0 || cancelled.CurrentJobID != "" || cancelled.LastJobID != job.ID {
				t.Fatalf("finished occurrence cancellation = %#v, %v", cancelled, err)
			}
			due, err := store.DueScheduledActions(now.Add(24*time.Hour), maximumScheduledActionBatch)
			if err != nil || len(due) != 0 {
				t.Fatalf("terminal action remained due: %#v, %v", due, err)
			}
		})
	}
}

func TestScheduledActionCancellationReconcilesLiveWorkerReservation(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	job := Job{
		ID: "scheduled-cancelled-job", Status: JobCancelled, AssignedNode: "node-a", Attempt: 1,
		CreatedAt: time.Now().UTC().Add(-time.Minute), UpdatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
	}
	if err := relay.store.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketJobs), job.ID, job)
	}); err != nil {
		t.Fatal(err)
	}
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve(job.ID) || !worker.beginDispatch(job.ID, job.Attempt) {
		t.Fatal("test worker reservation was not dispatched")
	}
	relay.mu.Lock()
	relay.workers[job.AssignedNode] = worker
	relay.mu.Unlock()
	relay.finalizeScheduledActionCancelledJob(ScheduledAction{
		CurrentJobID: job.ID, FailureCode: ScheduledActionFailureCancelAmbiguous,
	}, time.Now().UTC())
	worker.stateMu.Lock()
	reservation, exists := worker.inFlight[job.ID]
	worker.stateMu.Unlock()
	if !exists || !reservation.storeTerminal || !reservation.dispatchStarted {
		t.Fatalf("scheduled cancellation did not preserve and terminalize live execution: %#v", reservation)
	}
}

func TestScheduledActionPreDispatchCancellationIsDefinitive(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	now := time.Now().UTC()
	destination, payload := scheduledTestRefs()
	tests := []struct {
		name, code, wantStatus, wantCode string
	}{
		{"explicit cancel", ScheduledActionFailureCancelAmbiguous, ScheduledActionCancelled, ""},
		{"delivery timeout", ScheduledActionFailureDeliveryTimeoutAmbiguous, ScheduledActionFailed, ScheduledActionFailureDeliveryWindowExpired},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := Job{
				ID: fmt.Sprintf("scheduled-predispatch-%d", index), Status: JobCancelled,
				AssignedNode: fmt.Sprintf("node-%d", index), Attempt: 1,
				CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: now,
			}
			action := ScheduledAction{
				Schema: ScheduledActionV1, ID: fmt.Sprintf("sact_%032x", index+9000), Status: ScheduledActionUnknown,
				OwnerSubject: "channel-a", CredentialID: "tok_test", CredentialHash: strings.Repeat("d", 64),
				AdapterUID: "adp_" + strings.Repeat("c", 32), AdapterProfile: "message-delivery", AdapterPrincipal: "message-adapter",
				ActionKind: "message.text", DestinationRef: destination, PayloadRef: payload,
				StartAt: now.Add(-time.Minute), LocalStart: now.Add(-time.Minute).Format(time.RFC3339), Timezone: "UTC",
				Occurrences: 1, DeliveryWindowSeconds: 120, CurrentJobID: job.ID,
				FailureCode: test.code, LastOutcome: "unknown", CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
			}
			if err := relay.store.db.Update(func(tx *bolt.Tx) error {
				if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
					return err
				}
				return putScheduledActionTx(tx, nil, action)
			}); err != nil {
				t.Fatal(err)
			}
			worker := newWorkerConnection(nil, 1)
			if !worker.reserve(job.ID) {
				t.Fatal("test worker reservation failed")
			}
			relay.mu.Lock()
			relay.workers[job.AssignedNode] = worker
			relay.mu.Unlock()
			resolved := relay.finalizeScheduledActionCancelledJob(action, now)
			if resolved.Status != test.wantStatus || resolved.FailureCode != test.wantCode {
				t.Fatalf("pre-dispatch proof was not reflected: %#v", resolved)
			}
			stored, err := relay.store.GetScheduledActionForCredential(action.ID, action.CredentialHash)
			if err != nil || stored.Status != test.wantStatus || stored.FailureCode != test.wantCode {
				t.Fatalf("definitive scheduled state was not durable: %#v %v", stored, err)
			}
		})
	}
}

func TestScheduledActionFailsClosedForDisabledAdapterAndRevokedCredential(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	uid := adapterPresenceUID(relay.authority.ClusterID, "channel-a", "message-primary")
	limits := scheduledTestLimits(uid)
	token, tokenRecord, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour,
		ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterHeartbeat{Schema: AdapterPresenceV1, AdapterID: "message-primary", InstanceID: "host-a", DisplayName: "Message adapter", Kind: "control", Version: "1", State: "ready", Capabilities: []string{"scheduled-action"}, LeaseSeconds: 60}
	if response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", token, heartbeat); response.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", response.Code, response.Body.String())
	}
	first, hash := createConfirmedScheduledTestAction(t, relay, token, uid)
	if _, err := relay.store.SetAdapterEnabled(uid, false, "relay-admin", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	forceScheduledActionDue(t, relay.store, first.ID, time.Now().UTC().Add(-time.Second))
	relay.runScheduledActions(time.Now().UTC())
	waiting, err := relay.store.GetScheduledActionForCredential(first.ID, hash)
	if err != nil || waiting.CurrentJobID != "" || waiting.WaitingReason == "" || waiting.Status != ScheduledActionActive {
		t.Fatalf("disabled adapter did not fail closed: %#v, %v", waiting, err)
	}

	if _, err := relay.store.SetAdapterEnabled(uid, true, "relay-admin", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	second, secondHash := createConfirmedScheduledTestAction(t, relay, token, uid)
	if _, _, err := relay.store.RevokeTokenWithHash(tokenRecord.ID); err != nil {
		t.Fatal(err)
	}
	forceScheduledActionDue(t, relay.store, second.ID, time.Now().UTC().Add(-time.Second))
	relay.runScheduledActions(time.Now().UTC())
	failed, err := relay.store.GetScheduledActionForCredential(second.ID, secondHash)
	if err != nil || failed.Status != ScheduledActionFailed || failed.FailureCode != ScheduledActionFailureCredentialInactive || failed.CurrentJobID != "" {
		t.Fatalf("revoked credential dispatched work: %#v, %v", failed, err)
	}
}

func TestScheduledActionAmbiguityStopsIntervalWithoutReplay(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	uid := adapterPresenceUID(relay.authority.ClusterID, "channel-a", "message-primary")
	limits := scheduledTestLimits(uid)
	token, _, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterHeartbeat{Schema: AdapterPresenceV1, AdapterID: "message-primary", InstanceID: "host-a", DisplayName: "Message adapter", Kind: "control", Version: "1", State: "ready", Capabilities: []string{"scheduled-action"}, LeaseSeconds: 60}
	if response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", token, heartbeat); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	location, _ := time.LoadLocation("Europe/Berlin")
	input := scheduledTestRequest(t, uid, time.Now().In(location).Add(time.Minute))
	input.RepeatEverySeconds = 60
	input.Occurrences = 3
	response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/scheduled-actions/preview", token, input)
	var preview ScheduledAction
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &preview) != nil {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	if response = adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/scheduled-actions/"+preview.ID+"/confirm", token, map[string]interface{}{}); response.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", response.Code, response.Body.String())
	}
	forceScheduledActionDue(t, relay.store, preview.ID, time.Now().UTC().Add(-time.Second))
	relay.runScheduledActions(time.Now().UTC())
	record, _ := relay.store.Authenticate(token)
	action, _ := relay.store.GetScheduledActionForCredential(preview.ID, record.AuthHash)
	if action.CurrentJobID == "" {
		t.Fatal("interval occurrence was not dispatched")
	}
	if err := relay.store.db.Update(func(tx *bolt.Tx) error {
		var job Job
		if err := getJSON(tx.Bucket(bucketJobs), action.CurrentJobID, &job); err != nil {
			return err
		}
		if err := deleteQueueEntry(tx, job.ID); err != nil {
			return err
		}
		job.Status = JobFailed
		job.FailureCode = FailureExecutionStateAmbiguous
		job.Error = FailureExecutionStateAmbiguous
		job.FinishedAt = time.Now().UTC()
		job.UpdatedAt = job.FinishedAt
		return putJSON(tx.Bucket(bucketJobs), job.ID, job)
	}); err != nil {
		t.Fatal(err)
	}
	forceScheduledCheckNow(t, relay.store, preview.ID)
	if _, _, err := relay.store.AdvanceScheduledAction(preview.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	final, err := relay.store.GetScheduledActionForCredential(preview.ID, record.AuthHash)
	if err != nil || final.Status != ScheduledActionUnknown || final.FailureCode != FailureExecutionStateAmbiguous || final.CompletedOccurrences != 0 || !final.NextRunAt.Equal(action.NextRunAt) {
		t.Fatalf("ambiguous side effect was replayable: %#v, %v", final, err)
	}
}

func TestScheduledActionRelayRestartAfterDispatchStopsReplay(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	token, _, err := store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Authenticate(token)
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	request := scheduledTestRequest(t, uid, now.In(location).Add(time.Minute))
	request.RepeatEverySeconds = 60
	request.Occurrences = 3
	input, err := normalizeScheduledActionRequest(request, record, now)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateScheduledActionPreview(input, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmScheduledAction(preview.ID, record.AuthHash, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	forceScheduledActionDue(t, store, preview.ID, now.Add(-time.Second))
	action, dispatch, err := store.AdvanceScheduledAction(preview.ID, now)
	if err != nil || !dispatch {
		t.Fatalf("action not ready for dispatch: %#v %t %v", action, dispatch, err)
	}
	jobRequest, err := buildScheduledActionSubmit(action)
	if err != nil {
		t.Fatal(err)
	}
	jobRequest.OwnerSubject = record.Subject
	jobRequest.Requirements.AdapterPrincipal = action.AdapterPrincipal
	dispatched, job, err := store.DispatchScheduledAction(action.ID, jobRequest, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), job.ID, &job); err != nil {
			return err
		}
		job.Status = JobRunning
		job.AssignedNode = "node-a"
		job.AssignedAt = now
		job.UpdatedAt = now
		return putJSON(tx.Bucket(bucketJobs), job.ID, job)
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.RecoverRelayRestart("relay restarted")
	if err != nil || len(updated) != 1 || updated[0].ID != job.ID || updated[0].FailureCode != FailureExecutionStateAmbiguous {
		t.Fatalf("restart recovery = %#v, %v", updated, err)
	}
	forceScheduledCheckNow(t, store, action.ID)
	if _, _, err := store.AdvanceScheduledAction(action.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetScheduledActionForCredential(action.ID, record.AuthHash)
	if err != nil || final.Status != ScheduledActionUnknown || final.FailureCode != FailureExecutionStateAmbiguous || final.CompletedOccurrences != 0 || final.CurrentJobID != "" || final.LastJobID != job.ID || !final.NextRunAt.Equal(dispatched.NextRunAt) {
		t.Fatalf("restart left an ambiguous occurrence replayable: %#v, %v", final, err)
	}
	due, err := store.DueScheduledActions(time.Now().UTC().Add(24*time.Hour), maximumScheduledActionBatch)
	if err != nil || len(due) != 0 {
		t.Fatalf("terminal ambiguous action remained scheduled: %#v, %v", due, err)
	}
	jobCount := 0
	if err := store.db.View(func(tx *bolt.Tx) error {
		jobCount = tx.Bucket(bucketJobs).Stats().KeyN
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("restart created a duplicate external side effect job: %d jobs", jobCount)
	}
}

func TestScheduledActionIntervalSkipsExpiredOccurrences(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	token, _, err := store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Authenticate(token)
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	request := scheduledTestRequest(t, uid, now.In(location).Add(time.Minute))
	request.RepeatEverySeconds = 60
	request.Occurrences = 3
	request.DeliveryWindowSeconds = 30
	input, err := normalizeScheduledActionRequest(request, record, now)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateScheduledActionPreview(input, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmScheduledAction(preview.ID, record.AuthHash, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	forceScheduledActionDue(t, store, preview.ID, now.Add(-70*time.Second))
	action, dispatch, err := store.AdvanceScheduledAction(preview.ID, now)
	if err != nil || !dispatch || action.SkippedOccurrences != 1 || action.NextRunAt.After(now) {
		t.Fatalf("expired first occurrence was not skipped into the live second window: %#v %t %v", action, dispatch, err)
	}
	requestJob, err := buildScheduledActionSubmit(action)
	if err != nil {
		t.Fatal(err)
	}
	requestJob.OwnerSubject = record.Subject
	requestJob.Requirements.AdapterPrincipal = action.AdapterPrincipal
	dispatched, _, err := store.DispatchScheduledAction(action.ID, requestJob, 20, now)
	if err != nil || dispatched.CurrentOccurrence != 2 {
		t.Fatalf("wrong occurrence dispatched after a skip: %#v %v", dispatched, err)
	}
}

func TestScheduledActionDispatchIsAtomicUnderConcurrency(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	uid := "adp_" + strings.Repeat("c", 32)
	limits := scheduledTestLimits(uid)
	token, _, err := store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Authenticate(token)
	location, _ := time.LoadLocation("Europe/Berlin")
	now := time.Now().UTC()
	input, err := normalizeScheduledActionRequest(scheduledTestRequest(t, uid, now.In(location).Add(time.Minute)), record, now)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateScheduledActionPreview(input, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmScheduledAction(preview.ID, record.AuthHash, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	forceScheduledActionDue(t, store, preview.ID, now.Add(-time.Second))
	action, dispatch, err := store.AdvanceScheduledAction(preview.ID, now)
	if err != nil || !dispatch {
		t.Fatalf("action was not ready: %#v %t %v", action, dispatch, err)
	}
	jobRequest, err := buildScheduledActionSubmit(action)
	if err != nil {
		t.Fatal(err)
	}
	jobRequest.OwnerSubject = record.Subject
	jobRequest.Requirements.AdapterPrincipal = action.AdapterPrincipal

	errorsSeen := make(chan error, 2)
	var group sync.WaitGroup
	for index := 0; index < 2; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _, dispatchErr := store.DispatchScheduledAction(action.ID, jobRequest, 20, now)
			errorsSeen <- dispatchErr
		}()
	}
	group.Wait()
	close(errorsSeen)
	succeeded, rejected := 0, 0
	for dispatchErr := range errorsSeen {
		switch {
		case dispatchErr == nil:
			succeeded++
		case errors.Is(dispatchErr, ErrScheduledActionState):
			rejected++
		default:
			t.Fatalf("unexpected concurrent dispatch result: %v", dispatchErr)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent dispatch was not exactly-once at the relay boundary: succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestScheduledActionRequiresDeclaredAdapterCapability(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	uid := adapterPresenceUID(relay.authority.ClusterID, "channel-a", "message-primary")
	limits := scheduledTestLimits(uid)
	token, _, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterHeartbeat{Schema: AdapterPresenceV1, AdapterID: "message-primary", InstanceID: "host-a", DisplayName: "Message adapter", Kind: "control", Version: "1", State: "ready", LeaseSeconds: 60}
	if response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", token, heartbeat); response.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", response.Code, response.Body.String())
	}
	action, credentialHash := createConfirmedScheduledTestAction(t, relay, token, uid)
	now := time.Now().UTC()
	forceScheduledActionDue(t, relay.store, action.ID, now.Add(-time.Second))
	relay.runScheduledActions(now)
	waiting, err := relay.store.GetScheduledActionForCredential(action.ID, credentialHash)
	if err != nil || waiting.CurrentJobID != "" || waiting.WaitingReason != "adapter_unavailable" {
		t.Fatalf("undeclared capability authorized dispatch: %#v %v", waiting, err)
	}
	heartbeat.Capabilities = []string{"scheduled-action"}
	if response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", token, heartbeat); response.Code != http.StatusOK {
		t.Fatalf("capability heartbeat: %d %s", response.Code, response.Body.String())
	}
	relay.runScheduledActions(now.Add(2 * time.Second))
	dispatched, err := relay.store.GetScheduledActionForCredential(action.ID, credentialHash)
	if err != nil || dispatched.CurrentJobID == "" {
		t.Fatalf("declared capability did not become eligible: %#v %v", dispatched, err)
	}
}

func TestScheduledActionExpiredPresenceLeaseDoesNotDispatch(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"scheduled_action"}, MaxJobBytes: 64 << 10, MaxQueuedJobs: 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	uid := adapterPresenceUID(relay.authority.ClusterID, "channel-a", "message-primary")
	limits := scheduledTestLimits(uid)
	token, _, err := relay.store.CreateTokenWithLimits("producer", "channel-a", nil, time.Hour, ProducerLimits{Providers: []string{"adapter"}, ScheduledActions: &limits})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterHeartbeat{Schema: AdapterPresenceV1, AdapterID: "message-primary", InstanceID: "host-a", DisplayName: "Message adapter", Kind: "control", Version: "1", State: "ready", Capabilities: []string{"scheduled-action"}, LeaseSeconds: 60}
	if response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", token, heartbeat); response.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", response.Code, response.Body.String())
	}
	action, credentialHash := createConfirmedScheduledTestAction(t, relay, token, uid)
	future := time.Now().UTC().Add(2 * time.Minute)
	forceScheduledActionDue(t, relay.store, action.ID, future.Add(-time.Second))
	relay.runScheduledActions(future)
	waiting, err := relay.store.GetScheduledActionForCredential(action.ID, credentialHash)
	if err != nil || waiting.CurrentJobID != "" || waiting.Status != ScheduledActionActive || waiting.WaitingReason != "adapter_unavailable" {
		t.Fatalf("expired presence lease authorized dispatch: %#v %v", waiting, err)
	}
}

func TestScheduledActionRetentionPrunesOnlyOldTerminalRecords(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	destination, payload := scheduledTestRefs()
	base := ScheduledAction{
		Schema: ScheduledActionV1, OwnerSubject: "channel-a", CredentialID: "tok_test", CredentialHash: strings.Repeat("d", 64),
		AdapterUID: "adp_" + strings.Repeat("c", 32), AdapterProfile: "message-delivery", AdapterPrincipal: "message-adapter",
		ActionKind: "message.text", DestinationRef: destination, PayloadRef: payload, StartAt: now.Add(-40 * 24 * time.Hour),
		LocalStart: now.Add(-40 * 24 * time.Hour).Format(time.RFC3339), Timezone: "UTC", Occurrences: 1,
		DeliveryWindowSeconds: 120, CreatedAt: now.Add(-40 * 24 * time.Hour), UpdatedAt: now.Add(-40 * 24 * time.Hour),
	}
	old := base
	old.ID = "sact_" + strings.Repeat("1", 32)
	old.Status = ScheduledActionCancelled
	active := base
	active.ID = "sact_" + strings.Repeat("2", 32)
	active.Status = ScheduledActionActive
	active.StartAt = now.Add(time.Hour)
	active.NextRunAt = active.StartAt
	active.NextCheckAt = active.StartAt
	active.CreatedAt = now.Add(-40 * 24 * time.Hour)
	active.UpdatedAt = active.CreatedAt
	if err := store.db.Update(func(tx *bolt.Tx) error {
		if err := putScheduledActionTx(tx, nil, old); err != nil {
			return err
		}
		return putScheduledActionTx(tx, nil, active)
	}); err != nil {
		t.Fatal(err)
	}
	policy := RetentionPolicy{MaxAge: 30 * 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1}
	result, err := store.PruneRetention(now, policy)
	if err != nil || result.ScheduledActions != 1 {
		t.Fatalf("scheduled-action retention result = %#v, %v", result, err)
	}
	if _, err := store.GetScheduledActionForCredential(old.ID, old.CredentialHash); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old terminal action survived retention: %v", err)
	}
	if retained, err := store.GetScheduledActionForCredential(active.ID, active.CredentialHash); err != nil || retained.Status != ScheduledActionActive {
		t.Fatalf("active action was pruned: %#v %v", retained, err)
	}
	if err := store.db.View(scheduledActionIndexConsistent); err != nil {
		t.Fatalf("retention left an inconsistent scheduled-action index: %v", err)
	}
}

func TestScheduledActionRetentionProtectsCurrentJobUntilActionAgesOut(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	current := retentionJob("scheduled-current", JobCancelled, now.Add(-2*time.Hour), 1)
	newer := retentionJob("unrelated-newer", JobCompleted, now.Add(-time.Hour), 2)
	putRetentionJob(t, store, current)
	putRetentionJob(t, store, newer)
	destination, payload := scheduledTestRefs()
	action := ScheduledAction{
		Schema: ScheduledActionV1, ID: "sact_" + strings.Repeat("7", 32), Status: ScheduledActionActive,
		OwnerSubject: "channel-a", CredentialID: "tok_test", CredentialHash: strings.Repeat("d", 64),
		AdapterUID: "adp_" + strings.Repeat("c", 32), AdapterProfile: "message-delivery", AdapterPrincipal: "message-adapter",
		ActionKind: "message.text", DestinationRef: destination, PayloadRef: payload,
		StartAt: now.Add(-2 * time.Hour), LocalStart: now.Add(-2 * time.Hour).Format(time.RFC3339), Timezone: "UTC",
		Occurrences: 1, DeliveryWindowSeconds: 120, CurrentJobID: current.ID,
		NextCheckAt: now.Add(time.Minute), CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Minute),
	}
	if err := store.db.Update(func(tx *bolt.Tx) error { return putScheduledActionTx(tx, nil, action) }); err != nil {
		t.Fatal(err)
	}
	policy := RetentionPolicy{MaxAge: 30 * 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1}
	result, err := store.PruneRetention(now, policy)
	if err != nil || result.Jobs != 0 {
		t.Fatalf("current scheduled job was pruned: %#v %v", result, err)
	}
	if _, err := store.GetJob(current.ID); err != nil {
		t.Fatalf("current scheduled job is missing: %v", err)
	}
	previous := action
	action.Status = ScheduledActionCancelled
	action.UpdatedAt = now.Add(-40 * 24 * time.Hour)
	if err := store.db.Update(func(tx *bolt.Tx) error { return putScheduledActionTx(tx, &previous, action) }); err != nil {
		t.Fatal(err)
	}
	result, err = store.PruneRetention(now, policy)
	if err != nil || result.Jobs != 1 || result.ScheduledActions != 1 {
		t.Fatalf("aged action and linked job were not pruned together: %#v %v", result, err)
	}
	if _, err := store.GetJob(current.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aged linked job survived retention: %v", err)
	}
}

func createConfirmedScheduledTestAction(t *testing.T, relay *Relay, token, uid string) (ScheduledAction, string) {
	t.Helper()
	location, _ := time.LoadLocation("Europe/Berlin")
	input := scheduledTestRequest(t, uid, time.Now().In(location).Add(time.Minute))
	response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/scheduled-actions/preview", token, input)
	var action ScheduledAction
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &action) != nil {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	response = adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/scheduled-actions/"+action.ID+"/confirm", token, map[string]interface{}{})
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &action) != nil {
		t.Fatalf("confirm: %d %s", response.Code, response.Body.String())
	}
	record, ok := relay.store.Authenticate(token)
	if !ok {
		t.Fatal("token did not authenticate")
	}
	return action, record.AuthHash
}

func forceScheduledActionDue(t *testing.T, store *Store, id string, due time.Time) {
	t.Helper()
	if err := store.db.Update(func(tx *bolt.Tx) error {
		var action ScheduledAction
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil {
			return err
		}
		previous := action
		action.StartAt = due.UTC()
		action.NextRunAt = due.UTC()
		action.NextCheckAt = due.UTC()
		action.UpdatedAt = time.Now().UTC()
		return putScheduledActionTx(tx, &previous, action)
	}); err != nil {
		t.Fatal(err)
	}
}

func forceScheduledCheckNow(t *testing.T, store *Store, id string) {
	t.Helper()
	if err := store.db.Update(func(tx *bolt.Tx) error {
		var action ScheduledAction
		if err := getJSON(tx.Bucket(bucketScheduledActions), id, &action); err != nil {
			return err
		}
		previous := action
		action.NextCheckAt = time.Now().UTC().Add(-time.Millisecond)
		return putScheduledActionTx(tx, &previous, action)
	}); err != nil {
		t.Fatal(err)
	}
}
