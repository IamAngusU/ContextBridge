package cluster

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func liveActivityFixture(t *testing.T) (*Relay, *httptest.Server, string, TokenRecord, Job) {
	t.Helper()
	r, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: strings.Repeat("a", 40)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	token, record, err := r.store.CreateToken("producer", "activity-owner", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	job, err := r.store.CreateJob(SubmitRequest{OwnerSubject: "activity-owner", Requirements: Requirements{Provider: "adapter"},
		Payload: json.RawMessage(`{"output":{"activity":true}}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = r.store.AssignJob(job.ID, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.Handler())
	t.Cleanup(server.Close)
	return r, server, token, record, job
}

func liveManifest() json.RawMessage {
	return json.RawMessage(`{"schema":"contextbridge.resource-activity.v1","items":[{"id":"a1","kind":"file","action":"read","label":"README.md"}]}`)
}

func openActivityStream(t *testing.T, endpoint, token string) (*http.Response, *bufio.Scanner) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 || resp.Header.Get("X-ContextBridge-Event-Stream") != resourceActivityStreamMode || resp.Header.Get("Cache-Control") != "no-store, no-transform" {
		t.Fatalf("stream response = %s %v", resp.Status, resp.Header)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	return resp, scanner
}

func nextActivitySnapshot(t *testing.T, scanner *bufio.Scanner) JobResourceActivity {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id:") {
			t.Fatal("snapshot stream must not promise replay")
		}
		if strings.HasPrefix(line, "data: ") {
			var view JobResourceActivity
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &view); err != nil {
				t.Fatal(err)
			}
			return view
		}
	}
	t.Fatalf("expected activity snapshot: %v", scanner.Err())
	return JobResourceActivity{}
}

func TestLiveActivitySnapshotsReplaceReconnectAndFinish(t *testing.T) {
	r, server, token, _, job := liveActivityFixture(t)
	endpoint := server.URL + "/v1/cluster/jobs/" + job.ID + "/activity/stream"
	resp, scanner := openActivityStream(t, endpoint, token)
	if view := nextActivitySnapshot(t, scanner); view.EvidenceStatus != "pending" || view.EvidencePhase != "progress" {
		t.Fatalf("initial = %+v", view)
	}
	if _, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Activity: liveManifest()}); err != nil {
		t.Fatal(err)
	}
	if view := nextActivitySnapshot(t, scanner); view.Counts["file.read"] != 1 || view.ProgressSequence != 1 || view.Attempt != job.Attempt {
		t.Fatalf("live = %+v", view)
	}
	// Ordinary progress preserves evidence. It is a replacement snapshot, not an
	// additional receipt, and stale sequence numbers cannot replace it.
	if _, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Activity: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if view := nextActivitySnapshot(t, scanner); view.Counts["file.read"] != 1 || len(view.Resources) != 1 || view.ProgressSequence != 2 {
		t.Fatalf("replacement = %+v", view)
	}
	resp.Body.Close()
	_, reconnected := openActivityStream(t, endpoint, token)
	if view := nextActivitySnapshot(t, reconnected); view.ProgressSequence != 2 || len(view.Resources) != 1 {
		t.Fatalf("reconnect = %+v", view)
	}
	if _, err := r.store.CompleteJob(job.ID, "node-a", job.Attempt, activityResult(), nil, Usage{}, ""); err != nil {
		t.Fatal(err)
	}
	if view := nextActivitySnapshot(t, reconnected); view.EvidencePhase != "final" || len(view.Resources) != 3 || view.ProgressSequence != 0 {
		t.Fatalf("final = %+v", view)
	}
	for reconnected.Scan() {
		if strings.HasPrefix(reconnected.Text(), "data:") {
			t.Fatal("duplicate terminal snapshot")
		}
	}
	if err := reconnected.Err(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return len(r.eventStreamSlots) == 0 }, "stream slot leaked")
}

func TestLiveActivityRevocationAndScope(t *testing.T) {
	r, server, token, record, job := liveActivityFixture(t)
	endpoint := server.URL + "/v1/cluster/jobs/" + job.ID + "/activity/stream"
	other, _, err := r.store.CreateToken("producer", "another-owner", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token, suffix, cursor string
		status                int
	}{
		{"", "", "", 401}, {other, "", "", 404}, {token, "?after=0", "", 400}, {token, "", "1", 400},
	} {
		status, _, _ := readEventStream(t, endpoint+tc.suffix, tc.token, tc.cursor)
		if status != tc.status {
			t.Fatalf("stream status %d, want %d", status, tc.status)
		}
	}
	resp, scanner := openActivityStream(t, endpoint, token)
	nextActivitySnapshot(t, scanner)
	if err := r.store.RevokeToken(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Activity: liveManifest()}); err != nil {
		t.Fatal(err)
	}
	remaining, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(remaining), "README.md") {
		t.Fatal("revoked reader received new evidence")
	}
	waitFor(t, time.Second, func() bool { return len(r.eventStreamSlots) == 0 }, "revoked stream slot leaked")
}

func TestLiveActivityUsesSharedStreamCapacity(t *testing.T) {
	r, server, token, _, job := liveActivityFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withTokenRecord(req.Context(), TokenRecord{Role: "producer", Subject: "activity-owner"}))
	for i := 0; i < maximumEventStreamsPerSubject; i++ {
		release, ok := r.acquireExecutionEventStream(req)
		if !ok {
			t.Fatal("capacity unexpectedly exhausted")
		}
		t.Cleanup(release)
	}
	status, _, header := readEventStream(t, server.URL+"/v1/cluster/jobs/"+job.ID+"/activity/stream", token, "")
	if status != 503 || header.Get("Retry-After") != "1" {
		t.Fatal("per-subject shared bound not enforced")
	}
}

func TestLiveActivityStopsAfterExpiryOrTenantScopeChange(t *testing.T) {
	for _, mode := range []string{"expired", "scope_changed"} {
		t.Run(mode, func(t *testing.T) {
			r, server, token, _, job := liveActivityFixture(t)
			_, scanner := openActivityStream(t, server.URL+"/v1/cluster/jobs/"+job.ID+"/activity/stream", token)
			nextActivitySnapshot(t, scanner)
			record, _ := r.store.Authenticate(token)
			if mode == "expired" {
				record.ExpiresAt = time.Now().Add(-time.Minute)
			} else {
				record.ProducerLimits.AllowedTenants = []string{"another-tenant"}
			}
			if err := r.store.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketTokens), record.AuthHash, record) }); err != nil {
				t.Fatal(err)
			}
			if _, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Activity: liveManifest()}); err != nil {
				t.Fatal(err)
			}
			for scanner.Scan() {
				if strings.HasPrefix(scanner.Text(), "data:") {
					t.Fatal("authorization change did not stop data")
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLiveActivityRestartHonorsAmbiguousExecution(t *testing.T) {
	r, server, token, _, job := liveActivityFixture(t)
	if _, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Activity: liveManifest()}); err != nil {
		t.Fatal(err)
	}
	server.Close()
	path := r.store.db.Path()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRelay(RelayConfig{Database: path, AdminToken: strings.Repeat("a", 40)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	second := httptest.NewServer(reopened.Handler())
	t.Cleanup(second.Close)
	resp, scanner := openActivityStream(t, second.URL+"/v1/cluster/jobs/"+job.ID+"/activity/stream", token)
	view := nextActivitySnapshot(t, scanner)
	resp.Body.Close()
	// Existing recovery marks a potentially started job ambiguous. Telemetry
	// must not revive it or turn a partial read into a final success inventory.
	if view.State != "ambiguous" || view.EvidencePhase != "final" || view.EvidenceStatus != "not_reported" || len(view.Resources) != 0 {
		t.Fatalf("restart = %+v", view)
	}
}

func TestLiveActivityValidationPersistenceAndPrivacy(t *testing.T) {
	r, server, token, _, job := liveActivityFixture(t)
	for _, owner := range []string{"wrong-node", "node-a"} {
		attempt := job.Attempt
		if owner == "node-a" {
			attempt++
		}
		if _, err := r.store.UpdateJobProgress(job.ID, owner, attempt, JobProgress{Sequence: 1, Activity: liveManifest()}); err == nil {
			t.Fatal("foreign lease accepted")
		}
	}
	updated, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Activity: liveManifest()})
	if err != nil {
		t.Fatal(err)
	}
	updated.Progress.Activity[0] = 'x'
	stored, err := r.store.GetJob(job.ID)
	if err != nil || ProjectJobResourceActivity(stored).Counts["file.read"] != 1 {
		t.Fatal("persisted evidence corrupted")
	}
	for _, path := range []string{"/v1/cluster/jobs", "/v1/cluster/jobs/" + job.ID + "/events"} {
		_, body := relayHTTPTest(t, "GET", server.URL+path, token, nil)
		if strings.Contains(string(body), "README.md") || strings.Contains(string(body), "activity_status") {
			t.Fatalf("history leaked semantic activity: %s", body)
		}
	}
	for index, progress := range []JobProgress{{Activity: json.RawMessage(`{"schema":"bad"}`)}, {ActivityStatus: "invalid"}, {}} {
		progress.Sequence = uint64(index + 2)
		updated, err = r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, progress)
		if err != nil {
			t.Fatal(err)
		}
		if view := ProjectJobResourceActivity(updated); view.EvidenceStatus != "invalid" || len(view.Resources) != 0 {
			t.Fatalf("invalid was restored/accepted: %+v", view)
		}
	}
	updated, err = r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 5,
		Activity: json.RawMessage(`{"schema":"contextbridge.resource-activity.v1","items":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if view := ProjectJobResourceActivity(updated); view.EvidenceStatus != "reported" || len(view.Resources) != 0 {
		t.Fatalf("explicit clear = %+v", view)
	}
	// A final response without evidence must not label an earlier partial receipt
	// as a complete final inventory.
	updated, err = r.store.CompleteJob(job.ID, "node-a", job.Attempt, json.RawMessage(`{"output":{"provider":"adapter","text":"ok"}}`), nil, Usage{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if view := ProjectJobResourceActivity(updated); view.EvidenceStatus != "not_reported" || view.EvidencePhase != "final" {
		t.Fatalf("final = %+v", view)
	}
}

func TestLiveActivityCancellationClearsAndCloses(t *testing.T) {
	r, server, token, _, job := liveActivityFixture(t)
	if _, err := r.store.UpdateJobProgress(job.ID, "node-a", job.Attempt, JobProgress{Sequence: 1, Activity: liveManifest()}); err != nil {
		t.Fatal(err)
	}
	_, scanner := openActivityStream(t, server.URL+"/v1/cluster/jobs/"+job.ID+"/activity/stream", token)
	if view := nextActivitySnapshot(t, scanner); view.Counts["file.read"] != 1 {
		t.Fatal("missing running snapshot")
	}
	if _, err := r.store.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if view := nextActivitySnapshot(t, scanner); view.State != "cancelled" || len(view.Resources) != 0 {
		t.Fatalf("cancelled = %+v", view)
	}
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			t.Fatal("snapshot after terminal cancellation")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveActivityAdmissionCannotBeForgedInProgress(t *testing.T) {
	for _, tc := range []struct {
		payload  string
		provider string
		sealed   bool
	}{
		{`{}`, "adapter", false}, {`{"output":{"activity":true}}`, "ollama", false},
		{`{"output":{"activity":true}}`, "adapter", true},
	} {
		job := Job{Payload: json.RawMessage(tc.payload), Requirements: Requirements{Provider: tc.provider}, Progress: &JobProgress{Activity: liveManifest()}}
		if tc.sealed {
			job.SealedPayload = &SealedEnvelope{}
		}
		progress := normalizeJobActivityProgress(job, JobProgress{Sequence: 2, Activity: liveManifest(), ActivityStatus: "reported"})
		if len(progress.Activity) != 0 || progress.ActivityStatus != "" {
			t.Fatalf("unrequested plaintext activity: %+v", progress)
		}
	}
}
