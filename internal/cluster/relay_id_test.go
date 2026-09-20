package cluster

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRejectsUnsafeProducerJobIDs(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	valid128 := "a" + strings.Repeat("b", 127)
	if _, err := store.CreateJob(SubmitRequest{ID: valid128, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("exact 128-byte safe job id was rejected: %v", err)
	}
	for _, id := range []string{
		strings.Repeat("a", 129),
		"job/hidden",
		"job\\hidden",
		"job\x1b[31mspoof",
		"job\nspoof",
		"job..hidden",
		".hidden",
		"üjob",
	} {
		if _, err := store.CreateJob(SubmitRequest{ID: id, Payload: json.RawMessage(`{}`)}); err == nil {
			t.Fatalf("unsafe producer job id was accepted: %q", id)
		}
	}
}

func TestSubmitHTTPRejectsUnsafeProducerJobID(t *testing.T) {
	relay, err := NewRelay(RelayConfig{
		Database:     filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:   "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "id-test-producer", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(SubmitRequest{
		ID:           "job/terminal\x1b[31mspoof",
		Requirements: Requirements{Task: "generation"},
		Payload:      json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/cluster/jobs", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+producer)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe producer job id returned HTTP %d", response.StatusCode)
	}
}
