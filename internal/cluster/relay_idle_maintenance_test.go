package cluster

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenanceCandidatesIgnoreTerminalJobPayloads(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	terminal := Job{
		ID:         "terminal-history",
		Status:     JobCompleted,
		Result:     json.RawMessage(`{"artifact":"` + string(bytes.Repeat([]byte{'x'}, 1<<20)) + `"}`),
		CreatedAt:  time.Now().UTC().Add(-time.Hour),
		FinishedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := store.SaveJob(terminal); err != nil {
		t.Fatal(err)
	}
	reservations, queued, err := store.maintenanceCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if reservations || queued {
		t.Fatalf("terminal history was treated as maintenance work: reservations=%t queued=%t", reservations, queued)
	}

	assignment := Assignment{
		ID:           "assignment-maintenance",
		JobID:        "reserved-job-maintenance",
		OwnerSubject: "owner",
		TenantID:     "tenant",
		Attempt:      1,
		ExpiresAt:    time.Now().UTC().Add(time.Minute),
	}
	if err := store.CreateReservationAdmitted(assignment, "secret", "owner", 10, 10); err != nil {
		t.Fatal(err)
	}
	reservations, queued, err = store.maintenanceCandidates()
	if err != nil || !reservations || queued {
		t.Fatalf("reservation presence = %t/%t, %v", reservations, queued, err)
	}

	if _, err := store.GarbageCollectReservations(time.Now().UTC().Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(SubmitRequest{
		OwnerSubject: "owner",
		Requirements: Requirements{Task: "generation"},
		Payload:      json.RawMessage(`{"prompt":"queued"}`),
	}); err != nil {
		t.Fatal(err)
	}
	reservations, queued, err = store.maintenanceCandidates()
	if err != nil || reservations || !queued {
		t.Fatalf("queue presence = %t/%t, %v", reservations, queued, err)
	}
}

func TestRelayIdleMaintenanceDoesNotWriteOrScanTerminalHistory(t *testing.T) {
	relay := newIdleMaintenanceTestRelay(t)
	largeResult := json.RawMessage(`{"artifact":"` + string(bytes.Repeat([]byte{'x'}, 4<<20)) + `"}`)
	if err := relay.store.SaveJob(Job{ID: "large-terminal-history", Status: JobCompleted, Result: largeResult, CreatedAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	before := relay.store.db.Stats()
	relay.runMaintenance(time.Now().UTC())
	after := relay.store.db.Stats()
	delta := after.Sub(&before)
	if delta.TxStats.Write != 0 {
		t.Fatalf("idle maintenance performed %d Bolt writes with only terminal history", delta.TxStats.Write)
	}
}

func TestRelayIdleIncludesLiveReservationsAndPipelineRuns(t *testing.T) {
	relay := newIdleMaintenanceTestRelay(t)
	if !relay.Idle() {
		t.Fatal("new relay was not idle")
	}
	if !relay.QuiesceForStop(false) {
		t.Fatal("idle relay could not quiesce")
	}
	if relay.beginAdmission() {
		relay.endAdmission()
		t.Fatal("quiesced relay still admitted work")
	}
	relay.ResumeAfterRejectedStop()
	if !relay.beginAdmission() {
		t.Fatal("resumed relay did not reopen admission")
	}
	relay.endAdmission()

	worker := newWorkerConnection(nil, 1)
	if !worker.reserve("side-effect-still-running") {
		t.Fatal("test reservation failed")
	}
	worker.markStoreTerminal("side-effect-still-running")
	relay.workers["node-a"] = worker
	if relay.Idle() {
		t.Fatal("terminalized store job hid a live worker reservation")
	}
	if relay.QuiesceForStop(false) {
		t.Fatal("busy relay quiesced without --force")
	}
	if !relay.beginAdmission() {
		t.Fatal("rejected relay quiesce did not reopen admission")
	}
	relay.endAdmission()
	worker.release("side-effect-still-running")
	if !relay.Idle() {
		t.Fatal("released worker reservation kept relay busy")
	}

	run := PipelineRun{ID: "active-pipeline", Pipeline: "test", Status: "running", CreatedAt: time.Now().UTC()}
	if err := relay.store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	if relay.Idle() {
		t.Fatal("active pipeline was omitted from relay idle state")
	}
	run.Status = "completed"
	run.FinishedAt = time.Now().UTC()
	if err := relay.store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	if !relay.Idle() {
		t.Fatal("terminal pipeline kept relay busy")
	}
}

func TestRelayMaintenanceRecoversStaleInFlightJob(t *testing.T) {
	relay := newIdleMaintenanceTestRelay(t)
	now := time.Now().UTC()
	job, err := relay.store.CreateJob(SubmitRequest{
		OwnerSubject: "owner",
		Requirements: Requirements{Task: "generation"},
		Payload:      json.RawMessage(`{"prompt":"stale"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = relay.store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	job.AssignedAt = now.Add(-2 * time.Hour)
	job.Status = JobRunning
	if err := relay.store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve(job.ID) {
		t.Fatal("test worker could not reserve job")
	}
	if !worker.beginDispatch(job.ID, job.Attempt) {
		t.Fatal("test worker could not begin job dispatch")
	}
	relay.workers["node-a"] = worker

	relay.runMaintenance(now)
	recovered, err := relay.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != JobFailed || recovered.Error == "" {
		t.Fatalf("stale in-flight job was not recovered: %#v", recovered)
	}
	running, capacity := worker.load()
	if running != 1 || capacity != 1 {
		t.Fatalf("ambiguous worker execution released capacity early: %d/%d", running, capacity)
	}
	if worker.reserve("replacement-before-result") {
		t.Fatal("timed-out worker slot was reused before execution ended")
	}
	if relay.hasStaleRecoveryCandidates() {
		t.Fatal("terminalized reservation still requested stale-job maintenance")
	}
	if _, err := relay.store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"late":true}`), nil, Usage{}, ""); err == nil {
		t.Fatal("late result changed a timed-out job")
	}
	if queued, err := relay.store.QueuedJobs(10); err != nil || len(queued) != 0 {
		t.Fatalf("timed-out job became eligible for automatic execution: %#v, %v", queued, err)
	}

	// RecoverStaleJobs always opens a Bolt write transaction. Once the only
	// in-flight job has been terminalized, another maintenance pass must take
	// the index-only preflight and avoid scanning retained job payloads.
	before := relay.store.db.Stats()
	relay.runMaintenance(now.Add(time.Second))
	after := relay.store.db.Stats()
	if delta := after.Sub(&before); delta.TxStats.Write != 0 {
		t.Fatalf("terminalized reservation caused %d repeated maintenance writes", delta.TxStats.Write)
	}

	worker.release(job.ID) // models the matching worker result or disconnect
	if !worker.reserve("replacement-after-result") {
		t.Fatal("worker capacity did not reopen after execution ended")
	}
}

func TestRelayMaintenanceRecoversStaleQueuedReservation(t *testing.T) {
	relay := newIdleMaintenanceTestRelay(t)
	job, err := relay.store.CreateJob(SubmitRequest{
		OwnerSubject: "owner",
		Requirements: Requirements{Task: "generation"},
		Sealed:       &SealedEnvelope{Algorithm: sealedAlgorithm},
	})
	if err != nil {
		t.Fatal(err)
	}
	job.AssignedNode = "node-a"
	job.CreatedAt = time.Now().UTC().Add(-time.Hour)
	if err := relay.store.SaveJob(job); err != nil {
		t.Fatal(err)
	}

	relay.runMaintenance(time.Now().UTC())
	recovered, err := relay.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != JobFailed || recovered.Error == "" {
		t.Fatalf("stale queued reservation was not recovered: %#v", recovered)
	}
}

func TestCancelledInFlightReservationRemainsOccupiedWithoutMaintenanceScan(t *testing.T) {
	relay := newIdleMaintenanceTestRelay(t)
	job, err := relay.store.CreateJob(SubmitRequest{
		OwnerSubject: "owner",
		Requirements: Requirements{Task: "generation"},
		Payload:      json.RawMessage(`{"prompt":"cancel"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = relay.store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorkerConnection(nil, 1)
	if !worker.reserve(job.ID) {
		t.Fatal("test worker could not reserve job")
	}
	if !worker.beginDispatch(job.ID, job.Attempt) {
		t.Fatal("test worker could not begin job dispatch")
	}
	relay.workers["node-a"] = worker

	cancelled, err := relay.store.CancelJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	relay.markWorkerReservationTerminal(cancelled.AssignedNode, cancelled.ID)
	if relay.hasStaleRecoveryCandidates() {
		t.Fatal("cancelled reservation still requested stale-job maintenance")
	}
	if worker.reserve("replacement-before-cancel-ack") {
		t.Fatal("cancelled worker slot was reused before execution ended")
	}

	before := relay.store.db.Stats()
	relay.runMaintenance(time.Now().UTC())
	after := relay.store.db.Stats()
	if delta := after.Sub(&before); delta.TxStats.Write != 0 {
		t.Fatalf("cancelled reservation caused %d repeated maintenance writes", delta.TxStats.Write)
	}
}

func TestRelayMaintenanceGarbageCollectsReservationWithoutJobs(t *testing.T) {
	relay := newIdleMaintenanceTestRelay(t)
	now := time.Now().UTC()
	assignment := Assignment{
		ID:           "assignment-expiring",
		JobID:        "reserved-job-expiring",
		OwnerSubject: "owner",
		TenantID:     "tenant",
		Attempt:      1,
		ExpiresAt:    now.Add(time.Minute),
	}
	if err := relay.store.CreateReservationAdmitted(assignment, "secret", "owner", 10, 10); err != nil {
		t.Fatal(err)
	}

	relay.runMaintenance(now.Add(2 * time.Minute))
	reservations, queued, err := relay.store.maintenanceCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if reservations || queued {
		t.Fatalf("expired reservation survived maintenance: reservations=%t queued=%t", reservations, queued)
	}
}

func BenchmarkRelayIdleMaintenanceWithLargeTerminalHistory(b *testing.B) {
	relay := newIdleMaintenanceTestRelay(b)
	populateLargeTerminalHistory(b, relay, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		relay.runMaintenance(time.Now().UTC())
	}
	b.ReportMetric(64, "history_MiB")
}

// This reference benchmark preserves the cost of the old unconditional path
// so maintainers can compare the index-only idle preflight with a complete
// retained-jobs scan on the same database shape.
func BenchmarkStoreFullStaleRecoveryWithLargeTerminalHistory(b *testing.B) {
	relay := newIdleMaintenanceTestRelay(b)
	populateLargeTerminalHistory(b, relay, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := relay.store.RecoverStaleJobs(time.Now().UTC(), relay.cfg.AssignmentTTL, relay.cfg.JobTimeout); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(64, "history_MiB")
}

func newIdleMaintenanceTestRelay(tb testing.TB) *Relay {
	tb.Helper()
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(tb.TempDir(), "relay.db"),
		AdminToken:    "admin_012345678901234567890123456789012345",
		AllowedTasks:  []string{"generation"},
		DispatchEvery: 250 * time.Millisecond,
		AssignmentTTL: 2 * time.Minute,
		JobTimeout:    15 * time.Minute,
	}, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = relay.Close() })
	return relay
}

func populateLargeTerminalHistory(tb testing.TB, relay *Relay, count int) {
	tb.Helper()
	result := json.RawMessage(`{"artifact":"` + string(bytes.Repeat([]byte{'x'}, 1<<20)) + `"}`)
	for index := 0; index < count; index++ {
		job := Job{ID: randomID("terminal"), Status: JobCompleted, Result: result, CreatedAt: time.Now().UTC().Add(-time.Hour)}
		if err := relay.store.SaveJob(job); err != nil {
			tb.Fatal(err)
		}
	}
}
