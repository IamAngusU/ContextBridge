package cluster

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"testing"
)

func TestProtocolManifestIsDeterministicAndNamesPublicBoundaries(t *testing.T) {
	first := CurrentProtocolManifest(1 << 20)
	second := CurrentProtocolManifest(1 << 20)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("protocol manifest is not deterministic")
	}
	if first.Schema != ProtocolManifestV1 || first.WireProtocolVersion != ProtocolVersion {
		t.Fatalf("unexpected protocol identity: %#v", first)
	}
	if len(first.JobContractVersions) != 1 || first.JobContractVersions[0] != JobContractV1 {
		t.Fatalf("unexpected contract versions: %#v", first.JobContractVersions)
	}
	if first.Limits.MaximumConfiguredJobPayloadBytes != 1<<20 || first.Limits.MaximumJobPayloadBytes != MaximumJobPayloadBytes || first.Limits.MaximumJobResultBytes != MaximumJobResultBytes {
		t.Fatalf("unexpected manifest limits: %#v", first.Limits)
	}
	if !sort.StringsAreSorted(first.AdmissionErrorCodes) || !sort.StringsAreSorted(first.RuntimeFailureCodes) {
		t.Fatalf("stable IDs are not deterministically sorted: %#v %#v", first.AdmissionErrorCodes, first.RuntimeFailureCodes)
	}
	assertUniqueStrings(t, first.AdmissionErrorCodes)
	assertUniqueStrings(t, first.RuntimeFailureCodes)
	if !containsString(first.Features, "worker_conformance_report_v1") {
		t.Fatalf("worker conformance feature is not advertised: %#v", first.Features)
	}
	if !containsString(first.Features, "durable_worker_drain_v1") || !containsString(first.AdmissionErrorCodes, AdmissionCodeNodeDraining) {
		t.Fatalf("worker drain contract is not advertised: %#v %#v", first.Features, first.AdmissionErrorCodes)
	}
	if !containsString(first.Features, "producer_resource_governance_v1") || !containsString(first.AdmissionErrorCodes, AdmissionCodeCapacityOwnerRate) {
		t.Fatalf("producer resource governance contract is not advertised: %#v %#v", first.Features, first.AdmissionErrorCodes)
	}
	if !containsString(first.Features, "failure_aware_routing_v1") || !containsString(first.Features, "prometheus_metrics_v1") || first.Limits.MaximumRoutingHealthRecords != MaximumRoutingHealthRecords || first.Limits.MaximumRoutingHealthPerOwner != MaximumRoutingHealthRecordsPerOwner {
		t.Fatalf("bounded routing health or metrics is not advertised: %#v %#v", first.Features, first.Limits)
	}
	if !containsString(first.Features, "relay_role_health_v1") {
		t.Fatalf("relay role health feature is not advertised: %#v", first.Features)
	}
	if !containsString(first.Features, "authoritative_job_events_v1") {
		t.Fatalf("authoritative job event feature is not advertised: %#v", first.Features)
	}
	if !containsString(first.Features, "authoritative_pipeline_events_v1") {
		t.Fatalf("authoritative pipeline event feature is not advertised: %#v", first.Features)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestProtocolManifestEndpointIsAuthenticatedAndUsesRelayLimit(t *testing.T) {
	const adminToken = "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: adminToken, MaxJobBytes: 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	if status, _ := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/protocol", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous protocol read returned %d", status)
	}
	status, body := relayHTTPTest(t, http.MethodGet, server.URL+"/v1/cluster/protocol", adminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("protocol read returned %d: %s", status, body)
	}
	var manifest ProtocolManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Limits.MaximumConfiguredJobPayloadBytes != 4096 {
		t.Fatalf("configured relay limit was not authoritative: %#v", manifest.Limits)
	}
}

func assertUniqueStrings(t *testing.T, values []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			t.Fatalf("invalid or duplicate stable identifier %q in %#v", value, values)
		}
		seen[value] = true
	}
}
