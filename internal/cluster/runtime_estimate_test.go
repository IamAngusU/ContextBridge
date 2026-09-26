package cluster

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestHistoricalRuntimeEstimateConditionsRemainingOnElapsedTime(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-estimate"}
	for index, duration := range []uint64{1000, 2000, 3000, 4000, 5000} {
		recordRoutingPerformance(&node, requirements, "", duration, now.Add(time.Duration(index-10)*time.Minute), "idle:warm")
	}
	routeKey, _, _ := routingHealthKey(requirements)
	job := Job{
		Status: JobRunning, Requirements: requirements, AssignedNode: node.ID, StartedAt: now.Add(-2500 * time.Millisecond),
		RoutingDecision: &RoutingDecision{RouteKey: routeKey, SelectedNodeID: node.ID, Candidates: []RoutingCandidateDecision{{NodeID: node.ID, Eligible: true, PerformanceContext: "idle:warm"}}},
	}
	estimate := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now)
	if estimate.Status != "available" || estimate.Profile != "node_route_load" || estimate.Samples != 5 || estimate.ElapsedMS != 2500 || estimate.TotalP50MS != 3000 || estimate.TotalP90MS != 5000 || estimate.RemainingP50MS != 1500 || estimate.RemainingP90MS != 2500 {
		t.Fatalf("unexpected conditioned estimate: %#v", estimate)
	}

	job.StartedAt = now.Add(-6 * time.Second)
	estimate = EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now)
	if estimate.Status != "outside_typical_range" || !estimate.OutsideTypical || estimate.RemainingP50MS != 0 || estimate.RemainingP90MS != 0 {
		t.Fatalf("long-running job retained a false countdown: %#v", estimate)
	}
}

func TestHistoricalRuntimeEstimateRequiresFreshBoundedSuccessfulEvidence(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-estimate"}
	for index := 0; index < MinimumRuntimeEstimateSamples-1; index++ {
		recordRoutingPerformance(&node, requirements, "", 1000+uint64(index), now.Add(time.Duration(index)*time.Second), "")
	}
	job := Job{Status: JobAssigned, Requirements: requirements, AssignedNode: node.ID}
	if got := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now.Add(time.Minute)); got.Status != "unavailable" || got.Reason != "insufficient_comparable_history" {
		t.Fatalf("sparse evidence produced an estimate: %#v", got)
	}
	routeKey, _, _ := routingHealthKey(requirements)
	legacy := Node{ID: node.ID, RoutingPerformance: []RoutingPerformance{{
		RouteKey: routeKey, Samples: 100, EWMAComputeMS: 1500, LastCompletedAt: now,
	}}}
	if got := EstimateJobRuntimeAt(job, legacy, DefaultPlacementPolicy(), now.Add(time.Minute)); got.Status != "unavailable" {
		t.Fatalf("legacy EWMA without a duration distribution became a fabricated ETA: %#v", got)
	}
	recordRoutingPerformance(&node, requirements, "", 2000, now.Add(4*time.Second), "")
	if got := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now.Add(8*24*time.Hour)); got.Status != "unavailable" {
		t.Fatalf("stale evidence produced an estimate: %#v", got)
	}
	disabled := DefaultPlacementPolicy()
	disabled.PerformanceLearning = false
	if got := EstimateJobRuntimeAt(job, node, disabled, now.Add(time.Minute)); got.Reason != "learning_disabled" {
		t.Fatalf("disabled learning still produced evidence: %#v", got)
	}
	extremeMinimum := DefaultPlacementPolicy()
	extremeMinimum.MinimumSamples = math.MaxUint32
	if got := EstimateJobRuntimeAt(job, node, extremeMinimum, now.Add(time.Minute)); got.Status != "unavailable" {
		t.Fatalf("unrepresentable minimum sample policy overflowed into an estimate: %#v", got)
	}
	job.Status = JobCompleted
	if got := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now.Add(time.Minute)); got.Reason != "job_not_active" {
		t.Fatalf("terminal job was presented as an active estimate: %#v", got)
	}
}

func TestRoutingDurationHistoryIsBoundedAndCopied(t *testing.T) {
	values := []uint64{99, 0}
	for index := 1; index <= MaximumRoutingDurationSamples+10; index++ {
		values = append(values, uint64(index))
	}
	bounded := boundedDurationSamples(values)
	if len(bounded) != MaximumRoutingDurationSamples || bounded[0] != 11 || bounded[len(bounded)-1] != MaximumRoutingDurationSamples+10 {
		t.Fatalf("unexpected bounded duration history: %#v", bounded)
	}
	bounded[0] = 1
	if values[len(values)-MaximumRoutingDurationSamples] == 1 {
		t.Fatal("bounded duration history retained caller alias")
	}
}

func TestHistoricalRuntimeEstimateEndpointScopesProducerAndMinimizesEvidence(t *testing.T) {
	const adminToken = "admin_012345678901234567890123456789012345"
	now := time.Now().UTC()
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: adminToken, Placement: DefaultPlacementPolicy()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-estimate-api"}
	for index := 0; index < MinimumRuntimeEstimateSamples; index++ {
		recordRoutingPerformance(&node, requirements, "", uint64(index+1)*1000, now.Add(time.Duration(index-10)*time.Minute), "")
	}
	job := Job{ID: "job-estimate-api", OwnerSubject: "producer-a", Status: JobRunning, Requirements: requirements, AssignedNode: node.ID, StartedAt: now.Add(-1500 * time.Millisecond)}
	if err := relay.store.db.Update(func(tx *bolt.Tx) error {
		if err := putJSON(tx.Bucket(bucketNodes), node.ID, node); err != nil {
			return err
		}
		return putJSON(tx.Bucket(bucketJobs), job.ID, job)
	}); err != nil {
		t.Fatal(err)
	}
	producerA, _, err := relay.store.CreateToken("producer", "producer-a", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	producerB, _, err := relay.store.CreateToken("producer", "producer-b", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/cluster/jobs/"+job.ID+"/estimate", nil)
	request.Header.Set("Authorization", "Bearer "+producerB)
	response := httptest.NewRecorder()
	relay.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("other producer read runtime evidence with status %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/cluster/jobs/"+job.ID+"/estimate", nil)
	request.Header.Set("Authorization", "Bearer "+producerA)
	response = httptest.NewRecorder()
	relay.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "route_key") || strings.Contains(response.Body.String(), "qwen-test") {
		t.Fatalf("estimate endpoint leaked route internals or failed: %d %s", response.Code, response.Body.String())
	}
	var estimate HistoricalRuntimeEstimate
	if err := json.Unmarshal(response.Body.Bytes(), &estimate); err != nil || estimate.Status != "available" || estimate.Samples != MinimumRuntimeEstimateSamples {
		t.Fatalf("unexpected endpoint estimate: %#v err=%v", estimate, err)
	}
}
