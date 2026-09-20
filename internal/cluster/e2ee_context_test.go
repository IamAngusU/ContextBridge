package cluster

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReservationRejectsTenantContextChange(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assignment := Assignment{
		ID: "assignment-a", JobID: "job-a", NodeID: "node-a", Attempt: 1, TenantID: "tenant-a",
		ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: Requirements{Task: "rag_query", SessionID: "session-a"},
	}
	if err := store.CreateReservationAdmitted(assignment, "secret-a", "producer-a", 10, 10); err != nil {
		t.Fatal(err)
	}
	sealed := &SealedEnvelope{Algorithm: sealedAlgorithm}
	if _, err := store.ConsumeReservationAdmitted(assignment.ID, "secret-a", sealed, "test", "tenant-b", "producer-a", 0, 1, 10); !errors.Is(err, ErrReservationContextMismatch) {
		t.Fatalf("changed tenant consumed E2EE reservation: %v", err)
	}
	job, err := store.ConsumeReservationAdmitted(assignment.ID, "secret-a", sealed, "test", "tenant-a", "producer-a", 0, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if job.OwnerSubject != "producer-a" || job.TenantID != "tenant-a" || job.Requirements.SessionID != "session-a" {
		t.Fatalf("reserved authenticated namespace changed on consumption: %#v", job)
	}
}

func TestE2EEAADRejectsEveryExecutionContextMutation(t *testing.T) {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	request := AssignmentRequest{
		TenantID: "tenant-a",
		Requirements: Requirements{
			Task: "generation", SessionID: "session-a", Provider: "adapter", AdapterProfile: "profile-one", Model: "model-a", Group: "group-a",
			RequiredTags: []string{"trusted"}, PreferredNodes: []string{"node-a"}, MinFreeVRAM: 1024, Vision: true,
		},
	}
	response := AssignmentResponse{
		Assignment: Assignment{
			ID: "assignment-a", JobID: "job-a", NodeID: "node-a", NodeName: "Node A", PublicKey: publicKey,
			Attempt: 1, OwnerSubject: "producer-a", TenantID: request.TenantID, ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: request.Requirements,
		},
		Secret: "assignment-secret",
	}
	response.Assignment.PolicyDecision = PolicyDecision{
		Schema: PolicyDecisionV1, Outcome: "allow", ReasonCodes: []string{PolicyCodeAllowed}, RuleID: "default",
		PolicyFingerprint: "sha256:" + strings.Repeat("1", 64), EgressClass: "remote", ProviderClassification: "remote", CostEnforcement: "unbounded_unknown",
		EvaluatedAt: time.Now().UTC(),
	}
	encryptionContext, err := ValidateAssignmentResponse(request, response, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"prompt":"secret"}`)
	envelope, producerShared, err := SealFor(publicKey, payload, JobAAD(encryptionContext))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{
		ID: response.Assignment.JobID, AssignedNode: response.Assignment.NodeID, Attempt: response.Assignment.Attempt, OwnerSubject: response.Assignment.OwnerSubject,
		TenantID: response.Assignment.TenantID, Requirements: response.Assignment.Requirements, PolicyDecision: response.Assignment.PolicyDecision, SealedPayload: envelope,
	}
	workerContext, err := job.EncryptionContextForNode("node-a")
	if err != nil {
		t.Fatal(err)
	}
	opened, workerShared, err := OpenWith(privateKey, envelope, JobAAD(workerContext))
	if err != nil || !bytes.Equal(opened, payload) || workerShared != producerShared {
		t.Fatalf("unchanged E2EE context did not round trip: %q, %v", opened, err)
	}

	mutations := []struct {
		name   string
		mutate func(*Job)
	}{
		{"job id", func(job *Job) { job.ID = "job-b" }},
		{"assigned node", func(job *Job) { job.AssignedNode = "node-b" }},
		{"assignment attempt", func(job *Job) { job.Attempt++ }},
		{"owner", func(job *Job) { job.OwnerSubject = "producer-b" }},
		{"tenant", func(job *Job) { job.TenantID = "tenant-b" }},
		{"task", func(job *Job) { job.Requirements.Task = "embedding" }},
		{"session", func(job *Job) { job.Requirements.SessionID = "session-b" }},
		{"provider", func(job *Job) { job.Requirements.Provider = "ollama" }},
		{"adapter profile", func(job *Job) { job.Requirements.AdapterProfile = "profile-two" }},
		{"model", func(job *Job) { job.Requirements.Model = "model-b" }},
		{"group", func(job *Job) { job.Requirements.Group = "group-b" }},
		{"required tags", func(job *Job) { job.Requirements.RequiredTags = []string{"untrusted"} }},
		{"preferred nodes", func(job *Job) { job.Requirements.PreferredNodes = []string{"node-b"} }},
		{"minimum VRAM", func(job *Job) { job.Requirements.MinFreeVRAM++ }},
		{"vision", func(job *Job) { job.Requirements.Vision = false }},
		{"embedding", func(job *Job) { job.Requirements.Embedding = true }},
		{"policy fingerprint", func(job *Job) { job.PolicyDecision.PolicyFingerprint = "sha256:" + strings.Repeat("2", 64) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			tampered := job
			test.mutate(&tampered)
			tamperedContext, contextErr := tampered.EncryptionContextForNode("node-a")
			if contextErr == nil {
				if _, _, openErr := OpenWith(privateKey, envelope, JobAAD(tamperedContext)); openErr == nil {
					t.Fatal("worker decrypted a payload after outer execution metadata changed")
				}
			}
		})
	}

	sealedResult, err := SealResponse(workerShared, []byte(`{"answer":"done"}`), ResultAAD(workerContext))
	if err != nil {
		t.Fatal(err)
	}
	tamperedResultContext := workerContext
	tamperedResultContext.TenantID = "tenant-b"
	if _, err := OpenResponse(producerShared, sealedResult, ResultAAD(tamperedResultContext)); err == nil {
		t.Fatal("producer decrypted a result under a changed execution namespace")
	}
}

func TestProducerRejectsChangedAssignmentContextBeforeEncryption(t *testing.T) {
	_, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	request := AssignmentRequest{TenantID: "tenant-a", Requirements: Requirements{Task: "generation", SessionID: "session-a", Model: "model-a"}}
	response := AssignmentResponse{Assignment: Assignment{
		ID: "assignment-a", JobID: "job-a", NodeID: "node-a", PublicKey: publicKey, Attempt: 1, OwnerSubject: "producer-a",
		TenantID: request.TenantID, ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: request.Requirements,
	}, Secret: "assignment-secret"}
	for _, test := range []struct {
		name   string
		mutate func(*Assignment)
	}{
		{"attempt", func(assignment *Assignment) { assignment.Attempt = 2 }},
		{"tenant", func(assignment *Assignment) { assignment.TenantID = "tenant-b" }},
		{"task", func(assignment *Assignment) { assignment.Requirements.Task = "embedding" }},
		{"session", func(assignment *Assignment) { assignment.Requirements.SessionID = "session-b" }},
		{"model", func(assignment *Assignment) { assignment.Requirements.Model = "model-b" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tampered := response
			test.mutate(&tampered.Assignment)
			if _, err := ValidateAssignmentResponse(request, tampered, time.Now().UTC()); err == nil {
				t.Fatal("producer accepted a changed reservation context")
			}
		})
	}
}

func TestProducerAcceptsOnlyRelaySelectedAdapterEndpointInAssignment(t *testing.T) {
	_, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	request := AssignmentRequest{Requirements: Requirements{
		Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Model: "Adapter Model A", Reasoning: "high",
		AdapterFreshSession: true, AdapterEphemeralSession: true,
	}}
	response := AssignmentResponse{Assignment: Assignment{
		ID: "assignment-adapter", JobID: "job-adapter", NodeID: "node-adapter", PublicKey: publicKey,
		Attempt: 1, OwnerSubject: "producer-a", ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: request.Requirements,
	}, Secret: "assignment-secret"}
	response.Assignment.Requirements.AdapterEndpointID = 42
	context, err := ValidateAssignmentResponse(request, response, time.Now().UTC())
	if err != nil || context.Requirements.AdapterEndpointID != 42 {
		t.Fatalf("relay-selected adapter endpoint was not authenticated: %#v %v", context, err)
	}
	changed := response
	changed.Assignment.Requirements.Model = "Adapter Model B"
	if _, err := ValidateAssignmentResponse(request, changed, time.Now().UTC()); err == nil {
		t.Fatal("adapter endpoint binding allowed another requested capability to change")
	}
	changed = response
	changed.Assignment.Requirements.AdapterEphemeralSession = false
	if _, err := ValidateAssignmentResponse(request, changed, time.Now().UTC()); err == nil {
		t.Fatal("adapter endpoint binding changed the authenticated fresh-session policy")
	}
}

func TestWorkerFailsBeforeLocalExecutionWhenEncryptedMetadataIsTampered(t *testing.T) {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	encryptionContext := EncryptionContext{
		JobID: "job-a", NodeID: "node-a", Attempt: 1, OwnerSubject: "producer-a", TenantID: "tenant-a",
		Requirements: Requirements{Task: "generation", Provider: "adapter", SessionID: "session-a"},
	}
	envelope, _, err := SealFor(publicKey, []byte(`{"prompt":"secret"}`), JobAAD(encryptionContext))
	if err != nil {
		t.Fatal(err)
	}
	var localCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		localCalls.Add(1)
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer local.Close()
	worker := &Worker{
		cfg:      WorkerConfig{LocalURL: local.URL, LocalToken: "local-token", AllowedTasks: []string{"generation", "embedding"}, AllowedProviders: []string{"adapter"}},
		identity: WorkerIdentity{NodeID: "node-a", PrivateKey: privateKey, PublicKey: publicKey},
		client:   local.Client(),
	}
	job := Job{
		ID: encryptionContext.JobID, AssignedNode: encryptionContext.NodeID, Attempt: encryptionContext.Attempt, OwnerSubject: encryptionContext.OwnerSubject, TenantID: encryptionContext.TenantID,
		Requirements: encryptionContext.Requirements, SealedPayload: envelope,
	}
	job.Requirements.Task = "embedding"
	if _, _, _, _, err := worker.execute(context.Background(), job, nil); err == nil || !strings.Contains(err.Error(), "decrypt job") {
		t.Fatalf("tampered worker job did not fail at authenticated decryption: %v", err)
	}
	if localCalls.Load() != 0 {
		t.Fatal("worker forwarded a tampered encrypted job to the local bridge")
	}
}
