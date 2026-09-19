package cluster

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestRetentionPrunesOnlyDetailedTerminalHistoryAndKeepsLifetimeTotals(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	active := []Job{
		retentionJob("active-reserved", JobReserved, now.Add(-90*24*time.Hour), 1),
		retentionJob("active-queued", JobQueued, now.Add(-90*24*time.Hour), 2),
		retentionJob("active-assigned", JobAssigned, now.Add(-90*24*time.Hour), 3),
		retentionJob("active-running", JobRunning, now.Add(-90*24*time.Hour), 4),
		retentionJob("active-unknown", "future-state", now.Add(-90*24*time.Hour), 5),
	}
	terminal := []Job{
		retentionJob("terminal-old", JobCompleted, now.Add(-40*24*time.Hour), 10),
		retentionJob("terminal-third", JobFailed, now.Add(-3*time.Hour), 11),
		retentionJob("terminal-second", JobCancelled, now.Add(-2*time.Hour), 12),
		retentionJob("terminal-newest", JobCompleted, now.Add(-time.Hour), 13),
	}
	for _, job := range append(active, terminal...) {
		putRetentionJob(t, store, job)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketNodes), "node-history", Node{ID: "node-history", ComputeMS: 9876, CostUSD: 1.25})
	}); err != nil {
		t.Fatal(err)
	}

	for index := 0; index < 5; index++ {
		eventTime := now.Add(-time.Duration(index) * time.Hour)
		if index == 4 {
			eventTime = now.Add(-40 * 24 * time.Hour)
		}
		if err := store.AddEvent(Event{ID: "event-" + string(rune('0'+index)), Time: eventTime, Kind: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	runs := []PipelineRun{
		{ID: "run-active", Status: "running", CreatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: "run-unknown", Status: "paused", CreatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: "run-old", Status: "failed", CreatedAt: now.Add(-40 * 24 * time.Hour), FinishedAt: now.Add(-40 * 24 * time.Hour)},
		{ID: "run-third", Status: "completed", CreatedAt: now.Add(-3 * time.Hour), FinishedAt: now.Add(-3 * time.Hour)},
		{ID: "run-second", Status: "cancelled", CreatedAt: now.Add(-2 * time.Hour), FinishedAt: now.Add(-2 * time.Hour)},
		{ID: "run-newest", Status: "failed", CreatedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Hour)},
	}
	for _, run := range runs {
		if err := store.SavePipelineRun(run); err != nil {
			t.Fatal(err)
		}
	}

	before, err := store.Overview()
	if err != nil {
		t.Fatal(err)
	}
	policy := RetentionPolicy{MaxAge: 30 * 24 * time.Hour, MaxTerminalJobs: 2, MaxEvents: 2, MaxTerminalPipelineRuns: 2, MaxSessionPlacements: 2}
	removed, err := store.PruneRetention(now, policy)
	if err != nil {
		t.Fatal(err)
	}
	if removed != (RetentionResult{Jobs: 2, Events: 3, PipelineRuns: 2}) {
		t.Fatalf("unexpected retention result: %#v", removed)
	}

	for _, job := range active {
		if _, err := store.GetJob(job.ID); err != nil {
			t.Fatalf("active job %s was removed: %v", job.ID, err)
		}
	}
	for _, id := range []string{"terminal-second", "terminal-newest"} {
		if _, err := store.GetJob(id); err != nil {
			t.Fatalf("new terminal job %s was removed: %v", id, err)
		}
	}
	for _, job := range terminal[:2] {
		if _, err := store.GetJob(job.ID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pruned terminal job %s is still readable: %v", job.ID, err)
		}
		assertRetentionIndexesRemoved(t, store, job)
	}
	if queued, err := store.QueuedJobs(10); err != nil || len(queued) != 1 || queued[0].ID != "active-queued" {
		t.Fatalf("active queue changed: %#v, %v", queued, err)
	}

	after, err := store.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.JobsByState, after.JobsByState) {
		t.Fatalf("lifetime job totals changed after pruning: before=%v after=%v", before.JobsByState, after.JobsByState)
	}
	if !reflect.DeepEqual(before.Usage, after.Usage) {
		t.Fatalf("lifetime usage or node metrics changed after pruning: before=%#v after=%#v", before.Usage, after.Usage)
	}

	events, err := store.ListEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventIDs(events); !reflect.DeepEqual(got, []string{"event-0", "event-1"}) {
		t.Fatalf("retained events = %v", got)
	}
	retainedRuns, err := store.ListPipelineRuns(20)
	if err != nil {
		t.Fatal(err)
	}
	if got := pipelineRunIDs(retainedRuns); !reflect.DeepEqual(got, []string{"run-newest", "run-second", "run-active", "run-unknown"}) {
		t.Fatalf("retained pipeline runs = %v", got)
	}

	again, err := store.PruneRetention(now, policy)
	if err != nil {
		t.Fatal(err)
	}
	if again != (RetentionResult{}) {
		t.Fatalf("second sweep was not idempotent: %#v", again)
	}
	afterAgain, err := store.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.JobsByState, afterAgain.JobsByState) || !reflect.DeepEqual(after.Usage, afterAgain.Usage) {
		t.Fatal("idempotent sweep double-counted archived job totals")
	}
}

func TestRetentionBoundsSessionPlacementsByAgeAndCount(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	placements := map[string]sessionPlacement{
		"old":    {NodeID: "node-old", UpdatedAt: now.Add(-40 * 24 * time.Hour)},
		"third":  {NodeID: "node-third", UpdatedAt: now.Add(-3 * time.Hour)},
		"second": {NodeID: "node-second", UpdatedAt: now.Add(-2 * time.Hour)},
		"newest": {NodeID: "node-newest", UpdatedAt: now.Add(-time.Hour)},
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		for key, placement := range placements {
			if err := putJSON(tx.Bucket(bucketSessionPlacements), key, placement); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	policy := RetentionPolicy{
		MaxAge: 30 * 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1,
		MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 2,
	}
	removed, err := store.PruneRetention(now, policy)
	if err != nil {
		t.Fatal(err)
	}
	if removed.SessionPlacements != 2 {
		t.Fatalf("session placement retention removed %d entries, want 2", removed.SessionPlacements)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketSessionPlacements)
		for _, key := range []string{"second", "newest"} {
			if bucket.Get([]byte(key)) == nil {
				t.Fatalf("fresh retained placement %q was removed", key)
			}
		}
		for _, key := range []string{"old", "third"} {
			if bucket.Get([]byte(key)) != nil {
				t.Fatalf("expired/excess placement %q survived", key)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	again, err := store.PruneRetention(now, policy)
	if err != nil || again.SessionPlacements != 0 {
		t.Fatalf("session placement sweep was not idempotent: %#v %v", again, err)
	}
}

func TestRetentionReleasesIdempotencyKeyWithPrunedJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"first"}`)}
	first, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "retained-key", strings.Repeat("a", 64))
	if err != nil || replayed {
		t.Fatalf("create idempotent job: replayed=%v err=%v", replayed, err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := store.db.Update(func(tx *bolt.Tx) error {
		var job Job
		if err := getJSON(tx.Bucket(bucketJobs), first.ID, &job); err != nil {
			return err
		}
		job.Status = JobCompleted
		job.UpdatedAt = old
		job.FinishedAt = old
		return putJSON(tx.Bucket(bucketJobs), job.ID, job)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneRetention(time.Now().UTC(), RetentionPolicy{
		MaxAge: 24 * time.Hour, MaxTerminalJobs: 10, MaxEvents: 10, MaxTerminalPipelineRuns: 10, MaxSessionPlacements: 10,
	}); err != nil {
		t.Fatal(err)
	}
	request.Payload = json.RawMessage(`{"prompt":"after retention"}`)
	second, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "retained-key", strings.Repeat("b", 64))
	if err != nil || replayed || second.ID == first.ID {
		t.Fatalf("pruned key was not reusable: %#v replayed=%v err=%v", second, replayed, err)
	}
}

func TestRelayPrunesRetentionAtStartupAndWhenPeriodicSweepIsDue(t *testing.T) {
	database := filepath.Join(t.TempDir(), "relay.db")
	store, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	startupOld := retentionJob("startup-old", JobCompleted, now.Add(-48*time.Hour), 1)
	active := retentionJob("startup-active", JobQueued, now.Add(-48*time.Hour), 2)
	putRetentionJob(t, store, startupOld)
	putRetentionJob(t, store, active)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	relay, err := NewRelay(RelayConfig{
		Database:             database,
		AdminToken:           "admin-token-long-enough-for-retention-test",
		RetentionMaxAge:      24 * time.Hour,
		MaxTerminalJobs:      10,
		MaxEvents:            10,
		MaxTerminalRuns:      10,
		MaxSessionPlacements: 10,
		RetentionSweep:       time.Duration(MinimumRetentionSweepSeconds) * time.Second,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if _, err := relay.store.GetJob(startupOld.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup retention did not remove old terminal job: %v", err)
	}
	if _, err := relay.store.GetJob(active.ID); err != nil {
		t.Fatalf("startup retention removed active job: %v", err)
	}

	periodicOld := retentionJob("periodic-old", JobFailed, now.Add(-48*time.Hour), 3)
	putRetentionJob(t, relay.store, periodicOld)
	relay.nextRetention = time.Time{}
	relay.dispatch()
	if _, err := relay.store.GetJob(periodicOld.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("due periodic retention did not remove terminal job: %v", err)
	}
	if _, err := relay.store.GetJob(active.ID); err != nil {
		t.Fatalf("periodic retention removed active job: %v", err)
	}
}

func TestRetentionRejectsDisabledOrUnboundedPolicy(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	valid := RetentionPolicy{MaxAge: 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1}
	tests := []RetentionPolicy{
		{MaxAge: 0, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1},
		{MaxAge: time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1},
		{MaxAge: valid.MaxAge, MaxTerminalJobs: 0, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1},
		{MaxAge: valid.MaxAge, MaxTerminalJobs: 1, MaxEvents: 0, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1},
		{MaxAge: valid.MaxAge, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 0, MaxSessionPlacements: 1},
		{MaxAge: valid.MaxAge, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 0},
	}
	for _, policy := range tests {
		if _, err := store.PruneRetention(time.Now(), policy); err == nil {
			t.Fatalf("unbounded retention policy was accepted: %#v", policy)
		}
	}
}

func TestConcurrentRetentionSweepsArchiveATerminalJobOnce(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	putRetentionJob(t, store, retentionJob("concurrent-old", JobCompleted, now.Add(-48*time.Hour), 17))
	policy := RetentionPolicy{MaxAge: 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1}

	const sweepers = 12
	var wait sync.WaitGroup
	wait.Add(sweepers)
	results := make(chan RetentionResult, sweepers)
	errorsFound := make(chan error, sweepers)
	for index := 0; index < sweepers; index++ {
		go func() {
			defer wait.Done()
			result, err := store.PruneRetention(now, policy)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	removedJobs := 0
	for result := range results {
		removedJobs += result.Jobs
	}
	if removedJobs != 1 {
		t.Fatalf("concurrent sweeps reported %d job removals, want 1", removedJobs)
	}
	overview, err := store.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if overview.JobsByState[JobCompleted] != 1 || overview.Usage.InputTokens != 17 {
		t.Fatalf("terminal job was not archived exactly once: %#v", overview)
	}
}

func TestRetentionLifetimeTotalsSurviveStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	putRetentionJob(t, store, retentionJob("durable-old", JobFailed, now.Add(-48*time.Hour), 23))
	policy := RetentionPolicy{MaxAge: 24 * time.Hour, MaxTerminalJobs: 1, MaxEvents: 1, MaxTerminalPipelineRuns: 1, MaxSessionPlacements: 1}
	if _, err := store.PruneRetention(now, policy); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	overview, err := reopened.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if overview.JobsByState[JobFailed] != 1 || overview.Usage.InputTokens != 23 {
		t.Fatalf("pruned lifetime totals did not survive reopen: %#v", overview)
	}
}

func retentionJob(id, status string, at time.Time, tokens uint64) Job {
	return Job{
		ID: id, OwnerSubject: "owner", Status: status,
		Payload: json.RawMessage(`{"prompt":"payload-` + id + `"}`),
		Result:  json.RawMessage(`{"output":"result-` + id + `"}`),
		Usage: Usage{
			InputTokens: tokens, OutputTokens: tokens + 1, TotalTokens: tokens*2 + 1,
			EquivalentCostUSD: float64(tokens) / 100, SavedCostUSD: float64(tokens) / 200,
		},
		CreatedAt: at.Add(-time.Minute), UpdatedAt: at, FinishedAt: at,
	}
}

func putRetentionJob(t *testing.T, store *Store, job Job) {
	t.Helper()
	if err := store.db.Update(func(tx *bolt.Tx) error {
		if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
			return err
		}
		if err := tx.Bucket(bucketJobIndex).Put(jobIndexKey(job), []byte(job.ID)); err != nil {
			return err
		}
		if err := putJobOwnerIndex(tx.Bucket(bucketJobOwnerIndex), job); err != nil {
			return err
		}
		if job.Status == JobQueued {
			return tx.Bucket(bucketQueue).Put(queueKey(job), []byte(job.ID))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertRetentionIndexesRemoved(t *testing.T, store *Store, job Job) {
	t.Helper()
	if err := store.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketJobIndex).Get(jobIndexKey(job)) != nil {
			return errors.New("job index remains")
		}
		if tx.Bucket(bucketJobOwnerIndex).Get(jobOwnerIndexKey(job)) != nil {
			return errors.New("job owner index remains")
		}
		queue := tx.Bucket(bucketQueue).Cursor()
		for key, value := queue.First(); key != nil; key, value = queue.Next() {
			if string(value) == job.ID {
				return errors.New("queue index remains")
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("indexes for %s were not removed: %v", job.ID, err)
	}
}

func eventIDs(events []Event) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

func pipelineRunIDs(runs []PipelineRun) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return ids
}
