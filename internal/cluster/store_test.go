package cluster

import (
	"bytes"
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

func TestBrowserSessionBootstrapLockIsAtomicAndRebuiltAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", SessionID: "shared-session", BrowserFreshChat: true}
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
			assigned, assignErr := store.AssignBrowserJob(jobs[index].ID, fmt.Sprintf("node-%d", index), 40+index, false)
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
				t.Fatalf("two nodes bootstrapped the same browser session: %#v and %#v", winner.job, result.job)
			}
			winner = result
			continue
		}
		if !errors.Is(result.err, ErrBrowserSessionBusy) {
			t.Fatalf("second bootstrap failed with %v, want ErrBrowserSessionBusy", result.err)
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
	if _, err := store.AssignBrowserJob(jobs[loser.index].ID, "node-after-restart", 99, false); !errors.Is(err, ErrBrowserSessionBusy) {
		t.Fatalf("crash reconstruction lost the winning session lock: %v", err)
	}
	persistedWinner, err := store.GetJob(jobs[winner.index].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJob(persistedWinner.ID, persistedWinner.AssignedNode, persistedWinner.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{BrowserTabID: persistedWinner.Requirements.BrowserTabID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignBrowserJob(jobs[loser.index].ID, "node-after-completion", 99, false); err != nil {
		t.Fatalf("terminal completion did not release the browser session: %v", err)
	}
}

func TestBrowserSessionLockTreatsProviderlessAsProfileWildcardAndExemptsPerJobChats(t *testing.T) {
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
	explicitBrowser := create(Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt"})
	if _, err := store.AssignBrowserJob(explicitBrowser.ID, "node-b", 42, false); !errors.Is(err, ErrBrowserSessionBusy) {
		t.Fatalf("explicit browser job bypassed provider-less default-session lock: %v", err)
	}
	otherProfile := create(Requirements{Task: "generation", Provider: "browser", BrowserProfile: "gemini"})
	if _, err := store.AssignBrowserJob(otherProfile.ID, "node-c", 43, false); !errors.Is(err, ErrBrowserSessionBusy) {
		t.Fatalf("Gemini bypassed the provider-less wildcard lock: %v", err)
	}
	if _, err := store.CompleteJob(providerless.ID, "node-a", providerless.Attempt+1, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{BrowserTabID: 41}); err != nil {
		t.Fatal(err)
	}
	chatgpt := create(Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt"})
	if _, err := store.AssignBrowserJob(chatgpt.ID, "node-b", 44, false); err != nil {
		t.Fatalf("ChatGPT profile could not acquire its independent scope: %v", err)
	}
	gemini := create(Requirements{Task: "generation", Provider: "browser", BrowserProfile: "gemini"})
	if _, err := store.AssignBrowserJob(gemini.ID, "node-c", 45, false); err != nil {
		t.Fatalf("Gemini profile was incorrectly serialized behind ChatGPT: %v", err)
	}

	ephemeralRequirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", BrowserFreshChat: true, BrowserEphemeralChat: true}
	for index := 0; index < 2; index++ {
		job := create(ephemeralRequirements)
		if _, err := store.AssignBrowserJob(job.ID, fmt.Sprintf("ephemeral-node-%d", index), 50+index, false); err != nil {
			t.Fatalf("per-job chat %d was serialized: %v", index, err)
		}
	}
}

func TestBrowserSessionReservationPromotesAndRetainsLockUntilExecutionProof(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", SessionID: "encrypted-session"}
	first := Assignment{ID: "assignment-first", JobID: "job-first", NodeID: "node-a", Attempt: 1, ExpiresAt: now.Add(time.Minute), Requirements: requirements}
	if err := store.CreateReservationAdmitted(first, "secret-first", "producer-a", 10, 10); err != nil {
		t.Fatal(err)
	}
	second := Assignment{ID: "assignment-second", JobID: "job-second", NodeID: "node-b", Attempt: 1, ExpiresAt: now.Add(time.Minute), Requirements: requirements}
	if err := store.CreateReservationAdmitted(second, "secret-second", "producer-a", 10, 10); !errors.Is(err, ErrBrowserSessionBusy) {
		t.Fatalf("parallel reservation was admitted: %v", err)
	}
	job, err := store.ConsumeReservationAdmitted(first.ID, "secret-first", &SealedEnvelope{Algorithm: sealedAlgorithm}, "test", "", "producer-a", 0, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if busy, err := store.BrowserSessionBusy("producer-a", requirements, job.ID); err != nil || busy {
		t.Fatalf("promoted job blocked its own dispatch: busy=%v err=%v", busy, err)
	}
	if _, err := store.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if busy, err := store.BrowserSessionBusy("producer-a", requirements, ""); err != nil || !busy {
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
	if released, err := store.ReleaseBrowserSessionJobLock(job.ID); err != nil || !released {
		t.Fatalf("execution proof did not release promoted lock: released=%v err=%v", released, err)
	}
	if err := store.CreateReservationAdmitted(second, "secret-second", "producer-a", 10, 10); err != nil {
		t.Fatalf("released session could not be reserved again: %v", err)
	}
}

func TestProviderlessBrowserExecutionMetadataCompletesAndPersistsNodeScope(t *testing.T) {
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
	job, err = store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{BrowserTabID: 77})
	if err != nil || job.Status != JobCompleted || job.ExecutedBrowserTabID != 77 {
		t.Fatalf("provider-less browser result was rejected or lost: %#v err=%v", job, err)
	}
	if node, tab, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "node-a" || tab != 77 {
		t.Fatalf("provider-less node-scope placement = node %q tab %d ok %v", node, tab, ok)
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
	if err != nil || len(requeued) != 1 || requeued[0].Status != JobFailed {
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
	if normal.Status != JobFailed || sealed.Status != JobFailed {
		t.Fatalf("unexpected recovery states: %s %s", normal.Status, sealed.Status)
	}
}

func TestStoreTracksProgressOnlyForAssignedPlaintextJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "browser"}, Payload: json.RawMessage(`{}`)})
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
	job, err = store.AssignJob(job.ID, "browser-node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteJob(job.ID, "browser-node", job.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	if node, ok := store.RecentSessionNode("producer-a", "chat-42"); !ok || node != "browser-node" {
		t.Fatalf("session node was not found: %q %v", node, ok)
	}
	if _, ok := store.RecentSessionNode("producer-b", "chat-42"); ok {
		t.Fatal("session affinity leaked across producer identities")
	}
}

func TestAssignJobPersistsAndCannotChangeRelaySelectedBrowserTab(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", SessionID: "session-a"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := store.AssignJob(job.ID, "browser-node", 42)
	if err != nil || assigned.Requirements.BrowserTabID != 42 {
		t.Fatalf("selected browser tab was not persisted atomically: %#v %v", assigned, err)
	}
	assigned, err = store.CompleteJob(assigned.ID, "browser-node", assigned.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{BrowserTabID: 44})
	if err != nil || assigned.ExecutedBrowserTabID != 44 {
		t.Fatalf("actual browser execution tab was not persisted: %#v %v", assigned, err)
	}
	requirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", SessionID: "session-a", Model: "GPT-5.6 Sol"}
	if node, tabID, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "browser-node" || tabID != 44 {
		t.Fatalf("follow-up placement lost its exact browser tab: node=%q tab=%d ok=%v", node, tabID, ok)
	}
	followup, requiredNode := (&Relay{store: store}).withSessionAffinity(requirements, "producer-a")
	if followup.BrowserTabID != 44 || !followup.BrowserSessionRecovery || requiredNode != "browser-node" || len(followup.PreferredNodes) == 0 || followup.PreferredNodes[0] != "browser-node" {
		t.Fatalf("same-session follow-up did not inherit its node/tab placement: %#v node=%q", followup, requiredNode)
	}
	if followup.BrowserSessionKey != browserSessionRoutingKey("producer-a", requirements) {
		t.Fatalf("follow-up did not receive its opaque routing key: %#v", followup)
	}
	freshFollowup := requirements
	freshFollowup.BrowserFreshChat = true
	freshFollowup, freshRequiredNode := (&Relay{store: store}).withSessionAffinity(freshFollowup, "producer-a")
	if freshFollowup.BrowserTabID != 44 || freshRequiredNode != "browser-node" {
		t.Fatalf("per-session fresh-chat follow-up lost placement before the next heartbeat: %#v node=%q", freshFollowup, freshRequiredNode)
	}
	ephemeralFollowup := requirements
	ephemeralFollowup.BrowserFreshChat = true
	ephemeralFollowup.BrowserEphemeralChat = true
	ephemeralFollowup, ephemeralRequiredNode := (&Relay{store: store}).withSessionAffinity(ephemeralFollowup, "producer-a")
	if ephemeralFollowup.BrowserTabID != 0 || ephemeralRequiredNode != "" || len(ephemeralFollowup.PreferredNodes) != 0 || ephemeralFollowup.BrowserSessionKey != "" {
		t.Fatalf("per-job browser chat inherited durable placement: %#v node=%q", ephemeralFollowup, ephemeralRequiredNode)
	}
	session, ok := selectReadyBrowserSession([]BrowserSessionCapability{
		{TabID: 41, Profile: "chatgpt", State: "waiting", CurrentModel: "GPT-5.6 Sol"},
		{TabID: 44, Profile: "chatgpt", State: "session_bound", CurrentModel: "GPT-5.6 Sol"},
	}, followup)
	if !ok || session.TabID != 44 {
		t.Fatalf("same-session follow-up moved from its original tab: %#v %v", session, ok)
	}
	candidates := []Candidate{
		{Node: Node{ID: "other-node", Capabilities: Capabilities{BrowserSessions: []BrowserSessionCapability{{TabID: 44}}}}, Score: -100},
		{Node: Node{ID: "browser-node", Capabilities: Capabilities{BrowserSessions: []BrowserSessionCapability{{TabID: 44}}}}, Score: 100},
	}
	if node, ok := firstSessionCandidate(candidates, requiredNode); !ok || node.ID != "browser-node" {
		t.Fatalf("node-local tab id was reused on another worker: %#v %v", node, ok)
	}
	if _, ok := firstSessionCandidate(candidates[:1], requiredNode); ok {
		t.Fatal("browser session silently migrated to another node with the same numeric tab id")
	}
	bound, err := store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "browser", BrowserTabID: 7}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignJob(bound.ID, "browser-node", 8); err == nil {
		t.Fatal("assignment changed an existing browser-tab binding")
	}
}

func TestBrowserSessionPlacementUsesActualTabAcrossFailureRetentionAndProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt"}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.AssignBrowserJob(job.ID, "browser-node", 42, false)
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.CompleteJob(job.ID, "browser-node", job.Attempt, nil, nil, Usage{}, "provider_timeout", &ExecutionMetadata{BrowserTabID: 77})
	if err != nil || job.Status != JobFailed || job.ExecutedBrowserTabID != 77 {
		t.Fatalf("terminal browser error lost its actual execution tab: %#v %v", job, err)
	}
	if node, tabID, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "browser-node" || tabID != 77 {
		t.Fatalf("default-session placement was not recorded: node=%q tab=%d ok=%v", node, tabID, ok)
	}
	if _, _, ok := store.RecentSessionPlacement("producer-a", Requirements{Task: "generation", Provider: "browser", BrowserProfile: "gemini"}); ok {
		t.Fatal("ChatGPT session placement crossed into the Gemini profile")
	}
	if _, _, ok := store.RecentSessionPlacement("producer-b", requirements); ok {
		t.Fatal("browser session placement crossed producer ownership")
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
	if node, tabID, ok := store.RecentSessionPlacement("producer-a", requirements); !ok || node != "browser-node" || tabID != 77 {
		t.Fatalf("durable session placement depended on retained job history: node=%q tab=%d ok=%v", node, tabID, ok)
	}

	ephemeralRequirements := requirements
	ephemeralRequirements.SessionID = "per-job"
	ephemeralRequirements.BrowserFreshChat = true
	ephemeralRequirements.BrowserEphemeralChat = true
	ephemeral, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: ephemeralRequirements, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err = store.AssignBrowserJob(ephemeral.ID, "browser-node", 80, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteJob(ephemeral.ID, "browser-node", ephemeral.Attempt, json.RawMessage(`{"ok":true}`), nil, Usage{}, "", &ExecutionMetadata{BrowserTabID: 81}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := store.RecentSessionPlacement("producer-a", ephemeralRequirements); ok {
		t.Fatal("a disposable per-job chat became durable session affinity")
	}
}

func TestSealedBrowserAssignmentCannotMutateAuthenticatedTabContext(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requirements := Requirements{Task: "generation", Provider: "browser", BrowserProfile: "chatgpt", BrowserTabID: 42, BrowserSessionRecovery: true}
	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: requirements, Sealed: &SealedEnvelope{Algorithm: sealedAlgorithm}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignBrowserJob(job.ID, "browser-node", 0, true); err == nil {
		t.Fatal("dispatch changed an E2EE-authenticated browser tab after reservation")
	}
	assigned, err := store.AssignBrowserJob(job.ID, "browser-node", 42, true)
	if err != nil || !reflect.DeepEqual(assigned.Requirements, requirements) {
		t.Fatalf("dispatch did not preserve reserved browser requirements: %#v %v", assigned.Requirements, err)
	}
}

func TestCompleteJobIsBoundToAssignedWorkerAndNeverRetriesAnError(t *testing.T) {
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
	job, err = store.CompleteJob(job.ID, "assigned-node", job.Attempt, nil, nil, Usage{}, "browser_timeout")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobFailed || !strings.Contains(job.Error, "browser_timeout") {
		t.Fatalf("ambiguous worker error was retried or hidden: %#v", job)
	}
	queued, _ := store.QueuedJobs(10)
	if len(queued) != 0 {
		t.Fatal("failed worker completion was silently requeued")
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
			if test.wantStatus == JobFailed && (len(job.Result) != 0 || job.SealedResult != nil || !strings.HasPrefix(job.Error, "worker result rejected:")) {
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
