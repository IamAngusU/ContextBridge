package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestRelayConformanceExercisesOnlyReadAndDryRunSurfaces(t *testing.T) {
	relay, err := cluster.NewRelay(cluster.RelayConfig{
		Database:     filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:   "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"},
		MaxJobBytes:  4096,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := runRelayConformance(ctx, server.URL, "admin_012345678901234567890123456789012345")
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != relayConformanceV1 || !report.Compatible || len(report.Checks) != 8 {
		t.Fatalf("unexpected conformance report: %#v", report)
	}
	for _, check := range report.Checks {
		if !check.Passed || check.ID == "" || check.Detail == "" {
			t.Fatalf("invalid conformance check: %#v", check)
		}
	}

	var jobs []cluster.Job
	if err := clusterGET(ctx, server.URL+"/v1/cluster/jobs", "admin_012345678901234567890123456789012345", &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		raw, _ := json.Marshal(jobs)
		t.Fatalf("conformance mutated the queue: %s", raw)
	}
}

func TestConformanceHelpersRejectUnsortedOrDuplicateCatalogs(t *testing.T) {
	for _, values := range [][]string{nil, {}, {"b", "a"}, {"a", "a"}, {""}} {
		if sortedUniqueNonEmpty(values) {
			t.Fatalf("invalid catalog accepted: %#v", values)
		}
	}
	if !sortedUniqueNonEmpty([]string{"a", "b"}) {
		t.Fatal("valid stable catalog rejected")
	}
}
