package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestQueueMetadataAvoidsDecodingUnselectedPayloads(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	high, err := store.CreateJob(SubmitRequest{ID: "high", OwnerSubject: "owner-a", Priority: 10, Payload: json.RawMessage(`{"prompt":"small"}`)})
	if err != nil {
		t.Fatal(err)
	}
	low, err := store.CreateJob(SubmitRequest{ID: "low", OwnerSubject: "owner-b", Priority: -10, Payload: json.RawMessage(`{"prompt":"large"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketJobs).Put([]byte(low.ID), []byte(`{"payload":`)); err != nil {
			return err
		}
		return tx.Bucket(bucketJobs).Put([]byte(high.ID), []byte(`{"payload":`))
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		total, owned, countErr := queueCounts(tx, "owner-a")
		if countErr != nil {
			return countErr
		}
		if total != 2 || owned != 1 {
			t.Fatalf("queue metadata count = %d/%d, want 2/1", total, owned)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, total, _, err := store.QueuedJobsFairPage(200, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(page) != 2 || page[0].ID != high.ID || page[1].ID != low.ID {
		t.Fatalf("bounded fair page = total %d jobs %#v", total, page)
	}
}

func TestQueueMetadataMigratesLegacyIDValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(SubmitRequest{ID: "legacy-queue", OwnerSubject: "legacy-owner", Priority: 7, Payload: json.RawMessage(`{"prompt":"legacy"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		queue := tx.Bucket(bucketQueue)
		key, _ := queue.Cursor().First()
		if err := queue.Put(key, []byte(job.ID)); err != nil {
			return err
		}
		return tx.Bucket(bucketStoreMeta).Delete(keyQueueIndexVersion)
	}); err != nil {
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
	queued, err := reopened.QueuedJobs(1)
	if err != nil || len(queued) != 1 || queued[0].ID != job.ID || queued[0].OwnerSubject != "legacy-owner" {
		t.Fatalf("legacy queue migration failed: jobs=%#v err=%v", queued, err)
	}
	if err := reopened.db.View(func(tx *bolt.Tx) error {
		_, value := tx.Bucket(bucketQueue).Cursor().First()
		if len(value) == 0 || value[0] != '{' {
			t.Fatalf("legacy queue value was not upgraded: %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedCancellationPersistsCompactIdempotentTombstone(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload := json.RawMessage(`{"prompt":"` + string(bytes.Repeat([]byte("x"), 1<<20)) + `"}`)
	request := SubmitRequest{ID: "cancel-large", OwnerSubject: "owner-a", Payload: payload}
	hash := string(bytes.Repeat([]byte("a"), 64))
	job, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "cancel-key", hash)
	if err != nil || replayed {
		t.Fatalf("admit failed: replay=%v err=%v", replayed, err)
	}
	cancelled, err := store.CancelJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != JobCancelled || len(cancelled.Payload) != 0 || cancelled.SealedPayload != nil || cancelled.Progress != nil {
		t.Fatalf("cancelled job retained bulk fields: %#v", cancelled)
	}
	replayedJob, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "cancel-key", hash)
	if err != nil || !replayed || replayedJob.ID != job.ID || replayedJob.Status != JobCancelled || len(replayedJob.Payload) != 0 {
		t.Fatalf("idempotent tombstone replay failed: replay=%v job=%#v err=%v", replayed, replayedJob, err)
	}
}

func TestJobHistoryResponseOmitsBulkBodiesAndCandidateDetails(t *testing.T) {
	job := Job{
		ID: "history", Payload: json.RawMessage(`{"secret":"request"}`), Result: json.RawMessage(`{"secret":"result"}`),
		SealedPayload: &SealedEnvelope{Ciphertext: "request-cipher"}, SealedResult: &SealedEnvelope{Ciphertext: "result-cipher"},
		Progress:        &JobProgress{Text: "many bytes", Detail: "provider prose", Percent: 50},
		RoutingDecision: &RoutingDecision{CandidateCount: 1, Candidates: []RoutingCandidateDecision{{NodeID: "node-a"}}},
	}
	summary := jobHistoryResponse(job)
	if len(summary.Payload) != 0 || len(summary.Result) != 0 || summary.SealedPayload != nil || summary.SealedResult != nil {
		t.Fatalf("history retained body fields: %#v", summary)
	}
	if summary.Progress == nil || summary.Progress.Percent != 50 || summary.Progress.Text != "" || summary.Progress.Detail != "" {
		t.Fatalf("history progress projection is wrong: %#v", summary.Progress)
	}
	if summary.RoutingDecision == nil || summary.RoutingDecision.CandidateCount != 1 || len(summary.RoutingDecision.Candidates) != 0 {
		t.Fatalf("history routing projection is wrong: %#v", summary.RoutingDecision)
	}
}

func TestJobHistoryEndpointNeverReturnsRetainedBodies(t *testing.T) {
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_012345678901234567890123456789"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "history-owner", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.store.CreateJob(SubmitRequest{OwnerSubject: "history-owner", Payload: json.RawMessage(`{"prompt":"history-secret"}`)}); err != nil {
		t.Fatal(err)
	}
	status, body := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/jobs?limit=1000", producer, nil)
	if status != http.StatusOK {
		t.Fatalf("history returned %d: %s", status, body)
	}
	if bytes.Contains(body, []byte("history-secret")) || bytes.Contains(body, []byte(`"payload"`)) || bytes.Contains(body, []byte(`"result"`)) {
		t.Fatalf("history response leaked a retained body: %s", body)
	}
}

func TestWorkerControlRateLimitsAreIdentityAndByteBounded(t *testing.T) {
	relay := &Relay{rate: map[string]*rateWindow{}, workerRate: map[string]*rateWindow{}}
	now := time.Now()
	for index := 0; index < maximumWorkerReconnects; index++ {
		if allowed, _ := relay.allowWorkerReconnect("a"); !allowed {
			t.Fatalf("legitimate reconnect %d was rejected", index)
		}
	}
	if allowed, _ := relay.allowWorkerReconnect("a"); allowed {
		t.Fatal("reconnect flood was accepted")
	}
	if allowed, _ := relay.allowWorkerReconnect("b"); !allowed {
		t.Fatal("another authenticated node consumed the first node's quota")
	}
	for index := 0; index < maximumRateLimitBuckets; index++ {
		relay.rate[fmt.Sprintf("client:%d", index)] = &rateWindow{started: now}
	}
	if allowed, capacity := relay.allowWorkerReconnect("c"); !allowed || capacity {
		t.Fatal("unauthenticated client limiter capacity blocked a worker reconnect")
	}

	window := heartbeatRateWindow{}
	if !allowHeartbeatWindow(&window, 1024, now) {
		t.Fatal("small heartbeat was rejected")
	}
	if allowHeartbeatWindow(&window, maximumHeartbeatWindowBytes, now) {
		t.Fatal("heartbeat byte amplification exceeded the connection budget")
	}
}
