package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

func FuzzIdempotentAdmissionProperties(f *testing.F) {
	for _, seed := range []struct{ first, changed string }{
		{"same request", "different request"},
		{"", "x"},
		{"unicode-世界", "unicode-世畀"},
	} {
		f.Add(seed.first, seed.changed)
	}
	f.Fuzz(func(t *testing.T, firstPrompt, changedPrompt string) {
		if len(firstPrompt) > 4096 || len(changedPrompt) > 4096 {
			t.Skip()
		}
		store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		decision, err := EvaluateExecutionPolicy(ExecutionPolicyConfig{}, "", Requirements{Task: "generation"}, time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		key := "fuzz-key"
		hash := func(owner, prompt string) string {
			sum := sha256.Sum256([]byte(owner + "\x00" + prompt))
			return fmt.Sprintf("%x", sum[:])
		}
		request := func(owner, prompt string) SubmitRequest {
			payload, marshalErr := json.Marshal(map[string]string{"prompt": prompt})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			return SubmitRequest{OwnerSubject: owner, Requirements: Requirements{Task: "generation"},
				PolicyDecision: decision, Payload: payload}
		}

		firstRequest := request("producer-a", firstPrompt)
		first, replayed, err := store.CreateJobAdmittedIdempotent(firstRequest, 20, 20, key, hash("producer-a", firstPrompt))
		if err != nil || replayed || first.ID == "" {
			t.Fatalf("first admission failed: replayed=%v job=%#v err=%v", replayed, first, err)
		}
		retry, replayed, err := store.CreateJobAdmittedIdempotent(firstRequest, 20, 20, key, hash("producer-a", firstPrompt))
		if err != nil || !replayed || retry.ID != first.ID {
			t.Fatalf("identical retry was not stable: replayed=%v first=%q retry=%q err=%v", replayed, first.ID, retry.ID, err)
		}
		if changedPrompt != firstPrompt {
			changed := request("producer-a", changedPrompt)
			if _, _, err := store.CreateJobAdmittedIdempotent(changed, 20, 20, key, hash("producer-a", changedPrompt)); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed request with the same producer/key did not conflict: %v", err)
			}
		}
		other := request("producer-b", changedPrompt)
		independent, replayed, err := store.CreateJobAdmittedIdempotent(other, 20, 20, key, hash("producer-b", changedPrompt))
		if err != nil || replayed || independent.ID == first.ID {
			t.Fatalf("different producer was not independent: replayed=%v first=%q other=%q err=%v", replayed, first.ID, independent.ID, err)
		}
	})
}

func TestCleanLabelPreservesUTF8AtByteBoundary(t *testing.T) {
	for _, limit := range []int{3, 4, 5} {
		got := cleanLabel("ab😀cd", limit)
		if !utf8.ValidString(got) || len(got) > limit || got != "ab" {
			t.Fatalf("cleanLabel at %d bytes = %q", limit, got)
		}
	}
	if got := cleanLabel("ab😀cd", 6); got != "ab😀" {
		t.Fatalf("cleanLabel at rune boundary = %q", got)
	}
}

func TestAdapterSessionBootstrapLockIsAtomicAndRebuiltAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", SessionID: "shared-session", AdapterFreshSession: true}
	jobs := make([]Job, 2)
	for index := range jobs {
		jobs[index], err = store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{"prompt":"hello"}`)})
		if err != nil {
			t.Fatal(err)
		}
	}
	type assignmentResult struct {
		index int
		job   Job
		err   error
	}
	start := make(chan struct{})
	results := make(chan assignmentResult, len(jobs))
	var group sync.WaitGroup
	for index := range jobs {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			assigned, assignErr := store.AssignAdapterJob(jobs[index].ID, fmt.Sprintf("node-%d", index), 40+index, false)
			results <- assignmentResult{index: index, job: assigned, err: assignErr}
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	winner, loser := assignmentResult{index: -1}, assignmentResult{index: -1}
	for result := range results {
		if result.err == nil {
			if winner.index >= 0 {
				t.Fatalf("two nodes bootstrapped the same adapter session: %#v and %#v", winner.job, result.job)
			}
			winner = result
			continue
		}
		if !errors.Is(result.err, ErrAdapterSessionBusy) {
			t.Fatalf("second bootstrap failed with %v, want ErrAdapterSessionBusy", result.err)
		}
		loser = result
	}
	if winner.index < 0 || loser.index < 0 {
		t.Fatalf("bootstrap race did not produce exactly one winner: winner=%#v loser=%#v", winner, loser)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AssignAdapterJob(jobs[loser.index].ID, "node-after-restart", 99, false); !errors.Is(err, ErrAdapterSessionBusy) {
		t.Fatalf("crash reconstruction lost the winning session lock: %v", err)
	}
	persistedWinner, err := store.GetJob(jobs[winner.index].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJob(persistedWinner.ID, persistedWinner.AssignedNode, persistedWinner.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{AdapterEndpointID: persistedWinner.Requirements.AdapterEndpointID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignAdapterJob(jobs[loser.index].ID, "node-after-completion", 99, false); err != nil {
		t.Fatalf("terminal completion did not release the adapter session: %v", err)
	}
}

func TestAdapterSessionLockTreatsProviderlessAsProfileWildcardAndExemptsPerJobChats(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	create := func(requirements Requirements) Job {
		t.Helper()
		job, createErr := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return job
	}

	providerless := create(Requirements{Task: "generation"})
	if _, err := store.AssignJob(providerless.ID, "node-a"); err != nil {
		t.Fatal(err)
	}
	explicitAdapter := create(Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one"})
	if _, err := store.AssignAdapterJob(explicitAdapter.ID, "node-b", 42, false); !errors.Is(err, ErrAdapterSessionBusy) {
		t.Fatalf("explicit adapter job bypassed provider-less default-session lock: %v", err)
	}
	otherProfile := create(Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two"})
	if _, err := store.AssignAdapterJob(otherProfile.ID, "node-c", 43, false); !errors.Is(err, ErrAdapterSessionBusy) {
		t.Fatalf("Profile Two bypassed the provider-less wildcard lock: %v", err)
	}
	if _, err := store.CompleteJob(providerless.ID, "node-a", providerless.Attempt+1, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{AdapterEndpointID: 41}); err != nil {
		t.Fatal(err)
	}
	remoteA := create(Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one"})
	if _, err := store.AssignAdapterJob(remoteA.ID, "node-b", 44, false); err != nil {
		t.Fatalf("Profile One profile could not acquire its independent scope: %v", err)
	}
	remoteB := create(Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two"})
	if _, err := store.AssignAdapterJob(remoteB.ID, "node-c", 45, false); err != nil {
		t.Fatalf("Profile Two profile was incorrectly serialized behind Profile One: %v", err)
	}

	ephemeralRequirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", AdapterFreshSession: true, AdapterEphemeralSession: true}
	for index := 0; index < 2; index++ {
		job := create(ephemeralRequirements)
		if _, err := store.AssignAdapterJob(job.ID, fmt.Sprintf("ephemeral-node-%d", index), 50+index, false); err != nil {
			t.Fatalf("per-job chat %d was serialized: %v", index, err)
		}
	}
}

func TestAdapterSessionReservationPromotesAndRetainsLockUntilExecutionProof(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", SessionID: "encrypted-session"}
	first := Assignment{ID: "assignment-first", JobID: "job-first", NodeID: "node-a", Attempt: 1, ExpiresAt: now.Add(time.Minute), Requirements: requirements}
	if err := store.CreateReservationAdmitted(first, "secret-first", "producer-a", 10, 10); err != nil {
		t.Fatal(err)
	}
	second := Assignment{ID: "assignment-second", JobID: "job-second", NodeID: "node-b", Attempt: 1, ExpiresAt: now.Add(time.Minute), Requirements: requirements}
	if err := store.CreateReservationAdmitted(second, "secret-second", "producer-a", 10, 10); !errors.Is(err, ErrAdapterSessionBusy) {
		t.Fatalf("parallel reservation was admitted: %v", err)
	}
	job, err := store.ConsumeReservationAdmitted(first.ID, "secret-first", &SealedEnvelope{Algorithm: sealedAlgorithm}, "test", "", "producer-a", 0, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if busy, err := store.AdapterSessionBusy("producer-a", requirements, job.ID); err != nil || busy {
		t.Fatalf("promoted job blocked its own dispatch: busy=%v err=%v", busy, err)
	}
	if _, err := store.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if busy, err := store.AdapterSessionBusy("producer-a", requirements, ""); err != nil || !busy {
		t.Fatalf("cancellation released a potentially dispatched execution: busy=%v err=%v", busy, err)
	}
	if _, err := store.PruneRetention(now.Add(48*time.Hour), RetentionPolicy{
		MaxAge: 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetJob(job.ID); err != nil {
		t.Fatalf("retention pruned the proof record for a locked cancelled execution: %v", err)
	}
	if released, err := store.ReleaseAdapterSessionJobLock(job.ID); err != nil || !released {
		t.Fatalf("execution proof did not release promoted lock: released=%v err=%v", released, err)
	}
	if err := store.CreateReservationAdmitted(second, "secret-second", "producer-a", 10, 10); err != nil {
		t.Fatalf("released session could not be reserved again: %v", err)
	}
}

func TestProviderlessAdapterExecutionMetadataCompletesAndPersistsNodeScope(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requirements := Requirements{Task: "generation", SessionID: "auto-route-session"}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{AdapterEndpointID: 77})
	if err != nil || job.Status != JobCompleted || job.ExecutedAdapterEndpointID != 77 {
		t.Fatalf("provider-less adapter result was rejected or lost: %#v err=%v", job, err)
	}
	if node, endpoint, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "node-a" || endpoint != 77 {
		t.Fatalf("provider-less node-scope placement = node %q endpoint %d ok %v", node, endpoint, ok)
	}
}

func TestTokenIdentityRejectsSharedEmptyProducerAndDisplayControls(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, test := range []struct {
		role    string
		subject string
		groups  []string
	}{
		{role: "producer"},
		{role: "node"},
		{role: "producer", subject: "producer\u202eadmin"},
		{role: "producer", subject: "producer", groups: []string{"safe", "bad\nline"}},
	} {
		if _, _, err := store.CreateToken(test.role, test.subject, test.groups, time.Hour); err == nil {
			t.Fatalf("accepted invalid token identity: %#v", test)
		}
	}
	if _, _, err := store.CreateToken("producer", "producer-a", []string{"default"}, time.Hour); err != nil {
		t.Fatalf("valid producer identity rejected: %v", err)
	}
}

func TestIdempotentJobAdmissionIsDurableProducerScopedAndConflictSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	request := SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"once"}`)}
	hash := strings.Repeat("a", 64)
	first, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "invoice-42", hash)
	if err != nil || replayed {
		t.Fatalf("first admission = replayed %v, err %v", replayed, err)
	}
	second, replayed, err := store.CreateJobAdmittedIdempotent(request, 1, 1, "invoice-42", hash)
	if err != nil || !replayed || second.ID != first.ID {
		t.Fatalf("exact retry = %#v, replayed %v, err %v", second, replayed, err)
	}
	if _, _, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "invoice-42", strings.Repeat("b", 64)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request reused a key: %v", err)
	}
	request.OwnerSubject = "producer-b"
	other, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "invoice-42", strings.Repeat("b", 64))
	if err != nil || replayed || other.ID == first.ID {
		t.Fatalf("another producer did not receive an independent key scope: %#v replayed=%v err=%v", other, replayed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request.OwnerSubject = "producer-a"
	afterRestart, replayed, err := store.CreateJobAdmittedIdempotent(request, 1, 1, "invoice-42", hash)
	if err != nil || !replayed || afterRestart.ID != first.ID {
		t.Fatalf("restart lost idempotency binding: %#v replayed=%v err=%v", afterRestart, replayed, err)
	}
	queued, err := store.QueuedJobs(10)
	if err != nil || len(queued) != 2 {
		t.Fatalf("retries created duplicate queue entries: %d, %v", len(queued), err)
	}
}

func TestIdempotentReservationConsumptionReplaysAfterOneTimeSecretIsConsumed(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assignment := Assignment{
		ID: "assignment-idempotent", JobID: "job-idempotent", NodeID: "node-a", OwnerSubject: "producer-a", Attempt: 1,
		ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: Requirements{Task: "generation"},
	}
	if err := store.CreateReservationAdmitted(assignment, "one-time-secret", "producer-a", 10, 10); err != nil {
		t.Fatal(err)
	}
	envelope := &SealedEnvelope{Algorithm: sealedAlgorithm, Ciphertext: "opaque"}
	hash := strings.Repeat("c", 64)
	first, replayed, err := store.ConsumeReservationAdmittedIdempotent(assignment.ID, "one-time-secret", envelope, "test", "", "producer-a", 0, 1, 10, 10, "sealed-42", hash)
	if err != nil || replayed {
		t.Fatalf("first sealed admission = replayed %v, err %v", replayed, err)
	}
	second, replayed, err := store.ConsumeReservationAdmittedIdempotent(assignment.ID, "one-time-secret", envelope, "test", "", "producer-a", 0, 1, 1, 1, "sealed-42", hash)
	if err != nil || !replayed || second.ID != first.ID {
		t.Fatalf("sealed retry did not resolve consumed reservation: %#v replayed=%v err=%v", second, replayed, err)
	}
}

func TestReservationPromotionRejectsStalePolicyAuthorization(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := PolicyDecision{
		Schema: PolicyDecisionV1, Outcome: "allow", ReasonCodes: []string{PolicyCodeAllowed}, RuleID: "default",
		PolicyFingerprint: "sha256:" + strings.Repeat("1", 64), EgressClass: "local", ProviderClassification: "local",
		CostEnforcement: "not_requested", EvaluatedAt: time.Now().UTC(),
	}
	assignment := Assignment{
		ID: "assignment-policy", JobID: "job-policy", NodeID: "node-a", OwnerSubject: "producer-a", Attempt: 1,
		ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: Requirements{Task: "generation", Provider: "ollama"}, PolicyDecision: policy,
	}
	if err := store.CreateReservationAdmitted(assignment, "secret", "producer-a", 10, 10); err != nil {
		t.Fatal(err)
	}
	changed := policy
	changed.PolicyFingerprint = "sha256:" + strings.Repeat("2", 64)
	sealed := &SealedEnvelope{Algorithm: sealedAlgorithm, Ciphertext: "opaque"}
	if _, err := store.ConsumeReservationAdmittedWithPolicy(assignment.ID, "secret", sealed, "test", "", "producer-a", 0, 1, 10, changed, 10); !errors.Is(err, ErrReservationContextMismatch) {
		t.Fatalf("stale policy consumed reservation: %v", err)
	}
	fresh := policy
	fresh.EvaluatedAt = policy.EvaluatedAt.Add(time.Second)
	job, err := store.ConsumeReservationAdmittedWithPolicy(assignment.ID, "secret", sealed, "test", "", "producer-a", 0, 1, 10, fresh, 10)
	if err != nil || !job.PolicyDecision.EquivalentAuthorization(policy) {
		t.Fatalf("equivalent policy did not promote reservation: %#v %v", job.PolicyDecision, err)
	}
}

func TestEstimateVRAMIgnoresLegacyNodeWideMeasurements(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index := 0; index < 3; index++ {
		job, createErr := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Model: "model-a"}, Payload: json.RawMessage(`{}`)})
		if createErr != nil {
			t.Fatal(createErr)
		}
		assigned, assignErr := store.AssignJob(job.ID, "node-a")
		if assignErr != nil {
			t.Fatal(assignErr)
		}
		if _, completeErr := store.CompleteJob(job.ID, "node-a", assigned.Attempt, json.RawMessage(`{}`), nil, Usage{PeakVRAMBytes: 20 << 30}, ""); completeErr != nil {
			t.Fatal(completeErr)
		}
	}
	if got := store.EstimateVRAM(Requirements{Task: "generation", Model: "model-a"}); got != 0 {
		t.Fatalf("legacy node-wide VRAM was reused as a job estimate: %d", got)
	}
	for index := 0; index < 3; index++ {
		job, createErr := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Model: "model-b"}, Payload: json.RawMessage(`{}`)})
		if createErr != nil {
			t.Fatal(createErr)
		}
		assigned, assignErr := store.AssignJob(job.ID, "node-b")
		if assignErr != nil {
			t.Fatal(assignErr)
		}
		if _, completeErr := store.CompleteJob(job.ID, "node-b", assigned.Attempt, json.RawMessage(`{}`), nil, Usage{ResourceScope: "job", PeakVRAMBytes: 4 << 30}, ""); completeErr != nil {
			t.Fatal(completeErr)
		}
	}
	if got, want := store.EstimateVRAM(Requirements{Task: "generation", Model: "model-b"}), uint64((4<<30)+(4<<30)/10); got != want {
		t.Fatalf("job-attributed VRAM estimate = %d, want %d", got, want)
	}
}

func TestNodesWithSameDisplayNameRemainDistinct(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"node_alpha123", "node_beta456"} {
		if err := store.UpsertNode(Node{ID: id, Name: "Shared PC"}); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := store.ListNodes()
	if err != nil || len(nodes) != 2 || nodes[0].ID == nodes[1].ID {
		t.Fatalf("same-name nodes collided: %#v, %v", nodes, err)
	}
}

func TestNodeDrainIsDurableAndLinearizesAssignment(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node := Node{ID: "node-maintenance", Name: "Maintenance PC", Connected: true, State: "online", LastSeen: time.Now().UTC(), Capabilities: Capabilities{MaxConcurrent: 2}}
	if err := store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	active, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	active, err = store.AssignJob(active.ID, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	drained, err := store.SetNodeDraining(node.ID, true)
	if err != nil || !drained.Draining || drained.State != "draining" {
		t.Fatalf("node did not enter durable drain: %#v, %v", drained, err)
	}
	if _, err := store.CompleteJob(active.ID, node.ID, active.Attempt, json.RawMessage(`{}`), nil, Usage{}, ""); err != nil {
		t.Fatalf("drain interrupted an existing assignment: %v", err)
	}
	queued, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(queued.ID, node.ID); !errors.Is(err, ErrNodeDraining) {
		t.Fatalf("post-drain assignment returned %v, want ErrNodeDraining", err)
	}
	reservation := Assignment{ID: "assignment-draining", JobID: "job-draining", NodeID: node.ID, Attempt: 1, OwnerSubject: "producer-a", ExpiresAt: time.Now().UTC().Add(time.Minute), Requirements: Requirements{Task: "generation"}}
	if err := store.CreateReservationAdmitted(reservation, "reservation-secret", "producer-a", 10, 10); !errors.Is(err, ErrNodeDraining) {
		t.Fatalf("post-drain E2EE reservation returned %v, want ErrNodeDraining", err)
	}
	// A worker heartbeat does not own this operator-controlled field.
	node.Capabilities.Running = 0
	if err := store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetNode(node.ID)
	if err != nil || !persisted.Draining || persisted.State != "draining" {
		t.Fatalf("heartbeat cleared drain state: %#v, %v", persisted, err)
	}
	resumed, err := store.SetNodeDraining(node.ID, false)
	if err != nil || resumed.Draining || resumed.State != "online" {
		t.Fatalf("node did not resume: %#v, %v", resumed, err)
	}
	if _, err := store.AssignJob(queued.ID, node.ID); err != nil {
		t.Fatalf("resumed node rejected assignment: %v", err)
	}
}

func TestStorePersistsQueueAndOneTimePairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	low, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"low"}`), Priority: -10})
	if err != nil {
		t.Fatal(err)
	}
	high, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"high"}`), Priority: 50})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.QueuedJobs(10)
	if err != nil || len(queued) != 2 || queued[0].ID != high.ID {
		t.Fatalf("priority queue failed: %#v, %v", queued, err)
	}
	if _, err := store.AssignJob(high.ID, "node-one"); err != nil {
		t.Fatal(err)
	}
	queued, _ = store.QueuedJobs(10)
	if len(queued) != 1 || queued[0].ID != low.ID {
		t.Fatal("assigned job remained in queue")
	}
	requeued, err := store.RequeueNode("node-one", "disconnect")
	if err != nil || len(requeued) != 1 || requeued[0].Status != JobFailed || requeued[0].FailureCode != FailureExecutionStateAmbiguous {
		t.Fatal("ambiguous disconnected job did not fail closed")
	}
	queued, _ = store.QueuedJobs(10)
	if len(queued) != 1 || queued[0].ID != low.ID {
		t.Fatal("disconnected in-flight job was silently requeued")
	}
	_, publicKey, _ := NewIdentity()
	pair, err := store.CreatePairing(PairRequest{NodeName: "workstation", PublicKey: publicKey}, "https://relay.test/#pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecidePairing(pair.UserCode, true); err != nil {
		t.Fatal(err)
	}
	state, _, token, err := store.PollPairing(pair.DeviceCode)
	if err != nil || state != "approved" || token == "" {
		t.Fatalf("pairing failed: %s %v", state, err)
	}
	issuedToken := token
	state, _, token, err = store.PollPairing(pair.DeviceCode)
	if err != nil || state != "authorization_pending" || token != "" {
		t.Fatal("node token must only be returned once")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.GetJob(low.ID)
	if err != nil || job.ID != low.ID {
		t.Fatal("job did not survive restart")
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(issuedToken)) {
		t.Fatal("database exposed a raw token")
	}
}

func TestStoreRecoversStaleJobsWithEncryptionBoundary(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	normal, _ := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), MaxAttempts: 2})
	normal, _ = store.AssignJob(normal.ID, "node-a")
	normal.AssignedAt = time.Now().Add(-time.Hour)
	_ = store.SaveJob(normal)
	sealed, _ := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Sealed: &SealedEnvelope{Algorithm: sealedAlgorithm}, MaxAttempts: 1})
	sealed.AssignedNode, sealed.CreatedAt = "node-b", time.Now().Add(-time.Hour)
	_ = store.SaveJob(sealed)
	recovered, err := store.RecoverStaleJobs(time.Now(), time.Minute, time.Minute)
	if err != nil || len(recovered) != 2 {
		t.Fatalf("stale recovery failed: %v %#v", err, recovered)
	}
	normal, _ = store.GetJob(normal.ID)
	sealed, _ = store.GetJob(sealed.ID)
	if normal.Status != JobFailed || sealed.Status != JobFailed || normal.FailureCode != FailureExecutionTimeoutAmbiguous || sealed.FailureCode != FailureEncryptedReservationExpired {
		t.Fatalf("unexpected recovery states: %s %s", normal.Status, sealed.Status)
	}
}

func TestStoreTracksProgressOnlyForAssignedPlaintextJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "adapter"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(job.ID, "node-a", job.Attempt); err != nil {
		t.Fatal(err)
	}
	progress := JobProgress{Sequence: 1, Text: "partial", Phase: "generating", Busy: true}
	if _, err := store.UpdateJobProgress(job.ID, "node-a", job.Attempt, progress); err != nil {
		t.Fatal(err)
	}
	job, err = store.GetJob(job.ID)
	if err != nil || job.Progress == nil || job.Progress.Text != "partial" || job.Progress.Sequence != 1 {
		t.Fatalf("progress was not stored: %#v %v", job.Progress, err)
	}
	if _, err := store.UpdateJobProgress(job.ID, "different-node", job.Attempt, JobProgress{Sequence: 2, Text: "wrong"}); err == nil {
		t.Fatal("a different node must not update job progress")
	}
	job, err = store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if job.Progress == nil || job.Progress.Sequence != 2 || job.Progress.Phase != "final" || job.Progress.Busy {
		t.Fatalf("successful completion did not finalize progress: %#v", job.Progress)
	}
}

func TestStoreKeepsSessionAffinityPrivateToProducer(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation", SessionID: "chat-42"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "adapter-node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJob(job.ID, "adapter-node", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	if node, ok := store.RecentSessionNode("producer-a", "chat-42"); !ok || node != "adapter-node" {
		t.Fatalf("session node was not found: %q %v", node, ok)
	}
	if _, ok := store.RecentSessionNode("producer-b", "chat-42"); ok {
		t.Fatal("session affinity leaked across producer identities")
	}
}

func TestAssignJobPersistsAndCannotChangeRelaySelectedAdapterEndpoint(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", SessionID: "session-a"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := store.AssignJob(job.ID, "adapter-node", 42)
	if err != nil || assigned.Requirements.AdapterEndpointID != 42 {
		t.Fatalf("selected adapter endpoint was not persisted atomically: %#v %v", assigned, err)
	}
	assigned, err = store.CompleteJob(assigned.ID, "adapter-node", assigned.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{AdapterEndpointID: 44})
	if err != nil || assigned.ExecutedAdapterEndpointID != 44 {
		t.Fatalf("actual adapter execution endpoint was not persisted: %#v %v", assigned, err)
	}
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", SessionID: "session-a", Model: "Adapter Model A"}
	if node, endpointID, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "adapter-node" || endpointID != 44 {
		t.Fatalf("follow-up placement lost its exact adapter endpoint: node=%q endpoint=%d ok=%v", node, endpointID, ok)
	}
	followup, requiredNode := (&Relay{store: store}).withSessionAffinity(requirements, "producer-a")
	if followup.AdapterEndpointID != 44 || !followup.AdapterSessionRecovery || requiredNode != "adapter-node" || len(followup.PreferredNodes) == 0 || followup.PreferredNodes[0] != "adapter-node" {
		t.Fatalf("same-session follow-up did not inherit its node/endpoint placement: %#v node=%q", followup, requiredNode)
	}
	if followup.AdapterSessionKey != adapterSessionRoutingKey("producer-a", requirements) {
		t.Fatalf("follow-up did not receive its opaque routing key: %#v", followup)
	}
	freshFollowup := requirements
	freshFollowup.AdapterFreshSession = true
	freshFollowup, freshRequiredNode := (&Relay{store: store}).withSessionAffinity(freshFollowup, "producer-a")
	if freshFollowup.AdapterEndpointID != 44 || freshRequiredNode != "adapter-node" {
		t.Fatalf("per-session fresh-session follow-up lost placement before the next heartbeat: %#v node=%q", freshFollowup, freshRequiredNode)
	}
	ephemeralFollowup := requirements
	ephemeralFollowup.AdapterFreshSession = true
	ephemeralFollowup.AdapterEphemeralSession = true
	ephemeralFollowup, ephemeralRequiredNode := (&Relay{store: store}).withSessionAffinity(ephemeralFollowup, "producer-a")
	if ephemeralFollowup.AdapterEndpointID != 0 || ephemeralRequiredNode != "" || len(ephemeralFollowup.PreferredNodes) != 0 || ephemeralFollowup.AdapterSessionKey != "" {
		t.Fatalf("per-job adapter chat inherited durable placement: %#v node=%q", ephemeralFollowup, ephemeralRequiredNode)
	}
	session, ok := selectReadyAdapterSession([]AdapterSessionCapability{
		{EndpointID: 41, Profile: "profile-one", State: "waiting", CurrentModel: "Adapter Model A"},
		{EndpointID: 44, Profile: "profile-one", State: "session_bound", CurrentModel: "Adapter Model A"},
	}, followup)
	if !ok || session.EndpointID != 44 {
		t.Fatalf("same-session follow-up moved from its original endpoint: %#v %v", session, ok)
	}
	candidates := []Candidate{
		{Node: Node{ID: "other-node", Capabilities: Capabilities{AdapterSessions: []AdapterSessionCapability{{EndpointID: 44}}}}, Score: -100},
		{Node: Node{ID: "adapter-node", Capabilities: Capabilities{AdapterSessions: []AdapterSessionCapability{{EndpointID: 44}}}}, Score: 100},
	}
	if node, ok := firstSessionCandidate(candidates, requiredNode); !ok || node.ID != "adapter-node" {
		t.Fatalf("node-local endpoint id was reused on another worker: %#v %v", node, ok)
	}
	if _, ok := firstSessionCandidate(candidates[:1], requiredNode); ok {
		t.Fatal("adapter session silently migrated to another node with the same numeric endpoint id")
	}
	bound, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "adapter", AdapterEndpointID: 7}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(bound.ID, "adapter-node", 8); err == nil {
		t.Fatal("assignment changed an existing adapter-endpoint binding")
	}
}

func TestAdapterSessionPlacementUsesActualEndpointAcrossFailureRetentionAndProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one"}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignAdapterJob(job.ID, "adapter-node", 42, false)
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.CompleteJob(job.ID, "adapter-node", job.Attempt, nil, nil, Usage{}, "provider_timeout", &ExecutionMetadata{AdapterEndpointID: 77})
	if err != nil || job.Status != JobFailed || job.ExecutedAdapterEndpointID != 77 {
		t.Fatalf("terminal adapter error lost its actual execution endpoint: %#v %v", job, err)
	}
	if node, endpointID, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "adapter-node" || endpointID != 77 {
		t.Fatalf("default-session placement was not recorded: node=%q endpoint=%d ok=%v", node, endpointID, ok)
	}
	if _, _, ok := store.RecentSessionPlacement("producer-a", Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-two"}); ok {
		t.Fatal("Profile One session placement crossed into the Profile Two profile")
	}
	if _, _, ok := store.RecentSessionPlacement("producer-b", requirements); ok {
		t.Fatal("adapter session placement crossed producer ownership")
	}
	pruneNow := time.Now().UTC().Add(2 * time.Second)
	putRetentionJob(t, store, retentionJob("newer-retained-job", JobCompleted, pruneNow.Add(-time.Second), 1))
	if _, err := store.PruneRetention(pruneNow, RetentionPolicy{
		MaxAge: 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetJob(job.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture job was not pruned: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if node, endpointID, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "adapter-node" || endpointID != 77 {
		t.Fatalf("durable session placement depended on retained job history: node=%q endpoint=%d ok=%v", node, endpointID, ok)
	}

	ephemeralRequirements := requirements
	ephemeralRequirements.SessionID = "per-job"
	ephemeralRequirements.AdapterFreshSession = true
	ephemeralRequirements.AdapterEphemeralSession = true
	ephemeral, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: ephemeralRequirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err = store.AssignAdapterJob(ephemeral.ID, "adapter-node", 80, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteJob(ephemeral.ID, "adapter-node", ephemeral.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{AdapterEndpointID: 81}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := store.RecentSessionPlacement("producer-a", ephemeralRequirements); ok {
		t.Fatal("a disposable per-job chat became durable session affinity")
	}
}

func TestSealedAdapterAssignmentCannotMutateAuthenticatedEndpointContext(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requirements := Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", AdapterEndpointID: 42, AdapterSessionRecovery: true}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Sealed: &SealedEnvelope{Algorithm: sealedAlgorithm}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignAdapterJob(job.ID, "adapter-node", 0, true); err == nil {
		t.Fatal("dispatch changed an E2EE-authenticated adapter endpoint after reservation")
	}
	assigned, err := store.AssignAdapterJob(job.ID, "adapter-node", 42, true)
	if err != nil || !reflect.DeepEqual(assigned.Requirements, requirements) {
		t.Fatalf("dispatch did not preserve reserved adapter requirements: %#v %v", assigned.Requirements, err)
	}
}

func TestCompleteJobIsBoundToAssignedWorkerAndNeverRetriesAmbiguousError(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "assigned-node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteJob(job.ID, "foreign-node", job.Attempt, json.RawMessage(`{"forged":true}`), nil, Usage{}, ""); err == nil {
		t.Fatal("foreign worker completed another worker's job")
	}
	job, _ = store.GetJob(job.ID)
	if job.Status != JobAssigned {
		t.Fatalf("foreign completion mutated job state: %s", job.Status)
	}
	job, err = store.CompleteJob(job.ID, "assigned-node", job.Attempt, nil, nil, Usage{}, "adapter_timeout")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobFailed || !strings.Contains(job.Error, "adapter_timeout") || job.FailureCode != FailureAdapterTimeout {
		t.Fatalf("ambiguous worker error was retried or hidden: %#v", job)
	}
	queued, _ := store.QueuedJobs(10)
	if len(queued) != 0 {
		t.Fatal("failed worker completion was silently requeued")
	}
}

func TestCompleteJobRetriesOnlyProvenPreExecutionWorkerRefusal(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	requeued, err := store.CompleteJobWithFailure(first.ID, "node-a", first.Attempt, nil, nil, Usage{CostStatus: CostUnknown, CostUnknownJobs: 1}, "worker capacity exceeded", FailureWorkerCapacity)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != JobQueued || requeued.Attempt != 1 || requeued.AssignedNode != "" || requeued.Error != "" || requeued.FailureCode != "" || requeued.RoutingDecision != nil {
		t.Fatalf("proven pre-execution refusal was not cleanly requeued: %#v", requeued)
	}
	queued, err := store.QueuedJobs(10)
	if err != nil || len(queued) != 1 || queued[0].ID != job.ID {
		t.Fatalf("retry queue state = %#v, %v", queued, err)
	}
	second, err := store.AssignJob(job.ID, "node-b")
	if err != nil || second.Attempt != 2 {
		t.Fatalf("second assignment = %#v, %v", second, err)
	}
	if _, err := store.CompleteJob(first.ID, "node-a", first.Attempt, json.RawMessage(`{"stale":true}`), nil, Usage{}, ""); err == nil {
		t.Fatal("stale first-attempt completion changed the replacement assignment")
	}
	if _, err := store.MarkRunning(second.ID, "node-b", second.Attempt); err != nil {
		t.Fatal(err)
	}
	terminal, err := store.CompleteJobWithFailure(second.ID, "node-b", second.Attempt, nil, nil, Usage{}, "worker is stopping", FailureWorkerStopping)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != JobFailed || terminal.FailureCode != FailureWorkerStopping || terminal.Attempt != 2 {
		t.Fatalf("post-start refusal was retried despite ambiguous execution: %#v", terminal)
	}
	queued, _ = store.QueuedJobs(10)
	if len(queued) != 0 {
		t.Fatalf("post-start failure returned to queue: %#v", queued)
	}
}

func TestPreExecutionRetryRejectsEvidenceAdapterAndExhaustedAttempts(t *testing.T) {
	tests := []struct {
		name         string
		requirements Requirements
		maxAttempts  int
		usage        Usage
		failureCode  string
	}{
		{name: "execution evidence", requirements: Requirements{Task: "generation"}, maxAttempts: 2, usage: Usage{ComputeMS: 1}, failureCode: FailureWorkerCapacity},
		{name: "adapter session", requirements: Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile"}, maxAttempts: 2, failureCode: FailureWorkerCapacity},
		{name: "attempt limit", requirements: Requirements{Task: "generation"}, maxAttempts: 1, failureCode: FailureWorkerCapacity},
		{name: "unstructured worker prose", requirements: Requirements{Task: "generation"}, maxAttempts: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			job, err := store.CreateJob(SubmitRequest{Requirements: test.requirements, Payload: json.RawMessage(`{}`), MaxAttempts: test.maxAttempts})
			if err != nil {
				t.Fatal(err)
			}
			job, err = store.AssignJob(job.ID, "node-a")
			if err != nil {
				t.Fatal(err)
			}
			job, err = store.CompleteJobWithFailure(job.ID, "node-a", job.Attempt, nil, nil, test.usage, "worker capacity exceeded", test.failureCode)
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != JobFailed || job.FailureCode != FailureWorkerCapacity {
				t.Fatalf("unsafe retry class was accepted: %#v", job)
			}
			queued, _ := store.QueuedJobs(10)
			if len(queued) != 0 {
				t.Fatalf("unsafe retry class returned to queue: %#v", queued)
			}
		})
	}
}

func TestCompleteSealedJobDoesNotPersistWorkerErrorDetail(t *testing.T) {
	const secret = "DECRYPTED-PROMPT-MARKER-9f3d"
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{
		Requirements: Requirements{Task: "generation"},
		Sealed:       &SealedEnvelope{Algorithm: sealedAlgorithm},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignJob(job.ID, "assigned-node")
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.CompleteJobWithFailure(job.ID, "assigned-node", job.Attempt, nil, nil, Usage{}, FailureAdapterTimeout+": provider echoed "+secret, "invented-worker-code")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) || strings.Contains(job.Error, secret) {
		t.Fatalf("sealed failure detail reached durable relay state: %s", raw)
	}
	if job.Status != JobFailed || job.FailureCode != FailureAdapterTimeout || job.Error != sealedJobFailureMessage(FailureAdapterTimeout) {
		t.Fatalf("sealed failure lost stable metadata: %#v", job)
	}
}

func TestCompleteJobRejectsStaleAttemptFromSameWorker(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`), MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	first.Status = JobQueued
	if err := store.SaveJob(first); err != nil {
		t.Fatal(err)
	}
	current, err := store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if current.Attempt != first.Attempt+1 {
		t.Fatalf("assignment attempt did not advance: first=%d current=%d", first.Attempt, current.Attempt)
	}
	if _, err := store.CompleteJob(job.ID, "node-a", first.Attempt, json.RawMessage(`{"stale":true}`), nil, Usage{}, ""); err == nil {
		t.Fatal("stale result from the same worker completed the current assignment")
	}
	latest, err := store.GetJob(job.ID)
	if err != nil || latest.Status != JobAssigned || latest.Attempt != current.Attempt || len(latest.Result) != 0 {
		t.Fatalf("stale completion mutated current assignment: %#v, %v", latest, err)
	}
	completed, err := store.CompleteJob(job.ID, "node-a", current.Attempt, json.RawMessage(`{"current":true}`), nil, Usage{}, "")
	if err != nil || completed.Status != JobCompleted {
		t.Fatalf("current assignment could not complete: %#v, %v", completed, err)
	}
}

func TestListJobsForOwnerAppliesOwnerBeforeLimit(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Now().UTC().Add(-time.Hour)
	for index, id := range []string{"owner-a-oldest", "owner-a-newest"} {
		job, err := store.CreateJob(SubmitRequest{ID: id, OwnerSubject: "owner-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		job.CreatedAt = base.Add(time.Duration(index) * time.Second)
		if err := store.SaveJob(job); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 5; index++ {
		if _, err := store.CreateJob(SubmitRequest{ID: fmt.Sprintf("owner-b-%d", index), OwnerSubject: "owner-b", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := store.ListJobsForOwner(2, "", "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != "owner-a-newest" || jobs[1].ID != "owner-a-oldest" {
		t.Fatalf("newer foreign jobs consumed the owner history limit: %#v", jobs)
	}
}

func TestListJobsForOwnerMigratesLegacyIndexAndNeverDecodesForeignRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	foreignNewestID := "foreign-0199"
	err = db.Update(func(tx *bolt.Tx) error {
		jobs, err := tx.CreateBucket(bucketJobs)
		if err != nil {
			return err
		}
		index, err := tx.CreateBucket(bucketJobIndex)
		if err != nil {
			return err
		}
		put := func(job Job) error {
			if err := putJSON(jobs, job.ID, job); err != nil {
				return err
			}
			return index.Put(jobIndexKey(job), []byte(job.ID))
		}
		for number := 0; number < 200; number++ {
			job := Job{ID: fmt.Sprintf("foreign-%04d", number), OwnerSubject: "foreign-owner", Status: JobCompleted, CreatedAt: base.Add(time.Duration(number+100) * time.Second)}
			if err := put(job); err != nil {
				return err
			}
		}
		for number, id := range []string{"mine-old", "mine-new"} {
			job := Job{ID: id, OwnerSubject: "my-owner", Status: JobCompleted, CreatedAt: base.Add(time.Duration(number) * time.Second)}
			if err := put(job); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Corrupt the newest foreign record only after the one-time migration. An
	// owner-scoped query must not decode it; the old global-index scan did.
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).Put([]byte(foreignNewestID), []byte(`{"broken":`))
	}); err != nil {
		t.Fatal(err)
	}
	owned, err := store.ListJobsForOwner(2, "", "my-owner")
	if err != nil {
		t.Fatalf("owner index decoded a foreign record: %v", err)
	}
	if len(owned) != 2 || owned[0].ID != "mine-new" || owned[1].ID != "mine-old" {
		t.Fatalf("migrated owner history is wrong: %#v", owned)
	}
	if _, err := store.ListJobs(1, ""); err == nil {
		t.Fatal("global history did not encounter the deliberately corrupt newest record")
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if !bytes.Equal(tx.Bucket(bucketStoreMeta).Get(keyJobOwnerIndexVersion), jobOwnerIndexVersion) {
			return fmt.Errorf("owner index migration marker is missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveJobAtomicallyMovesOwnerAndCreatedAtIndexes(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{ID: "move-owner-index", OwnerSubject: "owner-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	oldGlobal, oldOwner := jobIndexKey(job), jobOwnerIndexKey(job)
	job.OwnerSubject = "owner-b"
	job.CreatedAt = job.CreatedAt.Add(time.Hour)
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketJobIndex).Get(oldGlobal) != nil || tx.Bucket(bucketJobOwnerIndex).Get(oldOwner) != nil {
			return errors.New("stale job index remains")
		}
		if tx.Bucket(bucketJobIndex).Get(jobIndexKey(job)) == nil || tx.Bucket(bucketJobOwnerIndex).Get(jobOwnerIndexKey(job)) == nil {
			return errors.New("new job index is missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldOwnerJobs, err := store.ListJobsForOwner(10, "", "owner-a")
	if err != nil || len(oldOwnerJobs) != 0 {
		t.Fatalf("old owner still sees moved job: %#v, %v", oldOwnerJobs, err)
	}
	newOwnerJobs, err := store.ListJobsForOwner(10, "", "owner-b")
	if err != nil || len(newOwnerJobs) != 1 || newOwnerJobs[0].ID != job.ID {
		t.Fatalf("new owner cannot see moved job: %#v, %v", newOwnerJobs, err)
	}
}

func TestCreateJobPersistsInternalPipelineMetadataAtomically(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreateJob(SubmitRequest{
		ID: "pipeline-job", OwnerSubject: "owner-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`),
		Pipeline: "review", Step: "summarize", ParentID: "run-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetJob(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Pipeline != "review" || stored.Step != "summarize" || stored.ParentID != "run-123" {
		t.Fatalf("pipeline metadata was not part of the admitted job transaction: %#v", stored)
	}
}

func TestCompleteJobEnforcesResultEnvelopeParity(t *testing.T) {
	validSealedResult := &SealedEnvelope{
		Algorithm:  sealedAlgorithm,
		Nonce:      encode(make([]byte, 12)),
		Ciphertext: encode(make([]byte, 16)),
	}
	tests := []struct {
		name       string
		sealedJob  bool
		result     json.RawMessage
		sealed     *SealedEnvelope
		wantStatus string
	}{
		{name: "plaintext valid", result: json.RawMessage(`{"ok":true}`), wantStatus: JobCompleted},
		{name: "plaintext malformed", result: json.RawMessage(`{"ok":`), wantStatus: JobFailed},
		{name: "plaintext returned sealed", sealed: validSealedResult, wantStatus: JobFailed},
		{name: "encrypted valid", sealedJob: true, sealed: validSealedResult, wantStatus: JobCompleted},
		{name: "encrypted returned plaintext", sealedJob: true, result: json.RawMessage(`{"leak":true}`), wantStatus: JobFailed},
		{name: "encrypted missing result", sealedJob: true, wantStatus: JobFailed},
		{name: "encrypted malformed nonce", sealedJob: true, sealed: &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: encode(make([]byte, 11)), Ciphertext: encode(make([]byte, 16))}, wantStatus: JobFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			request := SubmitRequest{Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)}
			if test.sealedJob {
				request.Payload = nil
				request.Sealed = &SealedEnvelope{Algorithm: sealedAlgorithm}
			}
			job, err := store.CreateJob(request)
			if err != nil {
				t.Fatal(err)
			}
			job, err = store.AssignJob(job.ID, "node-a")
			if err != nil {
				t.Fatal(err)
			}
			job, err = store.CompleteJob(job.ID, "node-a", job.Attempt, test.result, test.sealed, Usage{}, "")
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != test.wantStatus {
				t.Fatalf("completion status = %s, want %s (%s)", job.Status, test.wantStatus, job.Error)
			}
			if test.wantStatus == JobFailed && (len(job.Result) != 0 || job.SealedResult != nil || !strings.HasPrefix(job.Error, "worker result rejected:") || job.FailureCode != FailureWorkerResultRejected) {
				t.Fatalf("rejected result was retained or error hidden: %#v", job)
			}
		})
	}
}

func TestWorkerResultSizeBoundary(t *testing.T) {
	objectOfSize := func(size int64) json.RawMessage {
		return json.RawMessage(`{"x":"` + strings.Repeat("a", int(size)-8) + `"}`)
	}
	if err := validateWorkerResult(Job{}, objectOfSize(MaximumJobResultBytes), nil, ""); err != nil {
		t.Fatalf("exact plaintext result limit was rejected: %v", err)
	}
	if err := validateWorkerResult(Job{}, objectOfSize(MaximumJobResultBytes+1), nil, ""); err == nil {
		t.Fatal("plaintext result one byte beyond the limit was accepted")
	}
	sealedJob := Job{SealedPayload: &SealedEnvelope{Algorithm: sealedAlgorithm}}
	exact := &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: encode(make([]byte, 12)), Ciphertext: encode(make([]byte, int(MaximumJobResultBytes+16)))}
	if err := validateWorkerResult(sealedJob, nil, exact, ""); err != nil {
		t.Fatalf("exact encrypted result limit was rejected: %v", err)
	}
	over := &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: exact.Nonce, Ciphertext: encode(make([]byte, int(MaximumJobResultBytes+17)))}
	if err := validateWorkerResult(sealedJob, nil, over, ""); err == nil {
		t.Fatal("encrypted result one byte beyond the limit was accepted")
	}
}
