package cluster

import (
	"encoding/json"
	"fmt"
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

func TestHistoricalRuntimeEstimatePrefersBoundedPipelineStepWorkloadEvidence(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-step-estimate"}
	routeKey, _, _ := routingHealthKey(requirements)
	jobFor := func(pipeline, step string, payloadBytes int, duration uint64, completedAt time.Time) Job {
		job := Job{
			Pipeline: pipeline, Step: step, Status: JobCompleted, Requirements: requirements, AssignedNode: node.ID,
			Payload:         json.RawMessage(strings.Repeat("x", payloadBytes)),
			RoutingDecision: &RoutingDecision{RouteKey: routeKey, SelectedNodeID: node.ID, Candidates: []RoutingCandidateDecision{{NodeID: node.ID, Eligible: true, PerformanceContext: "idle:warm"}}},
		}
		recordJobRoutingPerformance(&node, job, duration, completedAt)
		return job
	}
	for index := 0; index < MinimumRuntimeEstimateSamples; index++ {
		jobFor("release", "render", 1024, uint64(1000+index*100), now.Add(time.Duration(index-20)*time.Minute))
		jobFor("release", "summarize", 1024, uint64(9000+index*100), now.Add(time.Duration(index-10)*time.Minute))
	}

	active := Job{
		Pipeline: "release", Step: "render", Status: JobRunning, Requirements: requirements, AssignedNode: node.ID,
		Payload: json.RawMessage(strings.Repeat("x", 1024)), StartedAt: now.Add(-500 * time.Millisecond),
		RoutingDecision: &RoutingDecision{RouteKey: routeKey, SelectedNodeID: node.ID, Candidates: []RoutingCandidateDecision{{NodeID: node.ID, Eligible: true, PerformanceContext: "idle:warm"}}},
	}
	estimate := EstimateJobRuntimeAt(active, node, DefaultPlacementPolicy(), now)
	if estimate.Status != "available" || estimate.Profile != runtimeProfilePipelineStepWorkloadLoad || estimate.Samples != MinimumRuntimeEstimateSamples || estimate.TotalP90MS != 1400 {
		t.Fatalf("stable pipeline step did not use its own workload history: %#v", estimate)
	}

	active.Step = "new-step"
	estimate = EstimateJobRuntimeAt(active, node, DefaultPlacementPolicy(), now)
	if estimate.Status != "available" || estimate.Profile != runtimeProfileRouteWorkloadLoad || estimate.Samples != MinimumRuntimeEstimateSamples*2 {
		t.Fatalf("unknown step did not fall back to bounded route/workload history: %#v", estimate)
	}

	encoded, err := json.Marshal(node.RoutingPerformance)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "release") || strings.Contains(string(encoded), "render") || strings.Contains(string(encoded), "summarize") {
		t.Fatalf("runtime profile retained raw pipeline identity: %s", encoded)
	}
}

func TestHistoricalRuntimeEstimateSeparatesBoundedPayloadClasses(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-workload-estimate"}
	routeKey, _, _ := routingHealthKey(requirements)
	record := func(payloadBytes int, duration uint64, completedAt time.Time) Job {
		job := Job{
			Status: JobCompleted, Requirements: requirements, AssignedNode: node.ID,
			Payload:         json.RawMessage(strings.Repeat("x", payloadBytes)),
			RoutingDecision: &RoutingDecision{RouteKey: routeKey, SelectedNodeID: node.ID, Candidates: []RoutingCandidateDecision{{NodeID: node.ID, Eligible: true, PerformanceContext: "light:cold"}}},
		}
		recordJobRoutingPerformance(&node, job, duration, completedAt)
		return job
	}
	for index := 0; index < MinimumRuntimeEstimateSamples; index++ {
		record(1024, uint64(1000+index*100), now.Add(time.Duration(index-20)*time.Minute))
		record(128<<10, uint64(12000+index*100), now.Add(time.Duration(index-10)*time.Minute))
	}
	active := Job{
		Status: JobAssigned, Requirements: requirements, AssignedNode: node.ID,
		Payload:         json.RawMessage(strings.Repeat("x", 128<<10)),
		RoutingDecision: &RoutingDecision{RouteKey: routeKey, SelectedNodeID: node.ID, Candidates: []RoutingCandidateDecision{{NodeID: node.ID, Eligible: true, PerformanceContext: "light:cold"}}},
	}
	estimate := EstimateJobRuntimeAt(active, node, DefaultPlacementPolicy(), now)
	if estimate.Status != "available" || estimate.Profile != runtimeProfileRouteWorkloadLoad || estimate.Samples != MinimumRuntimeEstimateSamples || estimate.TotalP50MS < 12000 {
		t.Fatalf("large payload estimate was contaminated by small-payload history: %#v", estimate)
	}
}

func TestRuntimeWorkloadProfilesAreBoundedAndValidated(t *testing.T) {
	now := time.Now().UTC()
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-bounded-profiles"}
	routeKey, _, _ := routingHealthKey(requirements)
	for index := 0; index < MaximumRuntimeProfilesPerRoute+8; index++ {
		job := Job{
			Pipeline: "pipeline", Step: fmt.Sprintf("step-%02d", index), Requirements: requirements, AssignedNode: node.ID,
			Payload:         json.RawMessage(`{"prompt":"bounded"}`),
			RoutingDecision: &RoutingDecision{RouteKey: routeKey, SelectedNodeID: node.ID, Candidates: []RoutingCandidateDecision{{NodeID: node.ID, Eligible: true, PerformanceContext: "idle:warm"}}},
		}
		recordJobRoutingPerformance(&node, job, uint64(index+1)*1000, now.Add(time.Duration(index)*time.Second))
	}
	if len(node.RoutingPerformance) != 1 || len(node.RoutingPerformance[0].RuntimeProfiles) != MaximumRuntimeProfilesPerRoute {
		t.Fatalf("runtime workload profiles are not bounded: %#v", node.RoutingPerformance)
	}
	node.RoutingPerformance[0].RuntimeProfiles = append(node.RoutingPerformance[0].RuntimeProfiles,
		RoutingRuntimePerformance{ProfileKey: "not-a-digest", Kind: runtimeProfileRouteWorkloadLoad, RecentSuccessSamples: []RoutingDurationSample{{ComputeMS: 1, CompletedAt: now}}},
		RoutingRuntimePerformance{ProfileKey: strings.Repeat("a", 64), Kind: "forged-kind", RecentSuccessSamples: []RoutingDurationSample{{ComputeMS: 1, CompletedAt: now}}},
	)
	bounded := boundedRoutingPerformance(node.RoutingPerformance)
	if len(bounded) != 1 || len(bounded[0].RuntimeProfiles) != MaximumRuntimeProfilesPerRoute {
		t.Fatalf("invalid runtime profiles survived normalization: %#v", bounded)
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

func TestExpiredRuntimeSamplesCannotBeRevivedByOneFreshCompletion(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-estimate"}
	for index := 0; index < MinimumRuntimeEstimateSamples; index++ {
		// These deliberately extreme values must never influence a fresh
		// distribution after their individual timestamps expire.
		recordRoutingPerformance(&node, requirements, "", 90000+uint64(index), now.Add(-30*24*time.Hour+time.Duration(index)*time.Minute), "idle:warm")
	}
	job := Job{Status: JobAssigned, Requirements: requirements, AssignedNode: node.ID}
	if got := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now); got.Status != "unavailable" {
		t.Fatalf("expired distribution produced an estimate: %#v", got)
	}

	recordRoutingPerformance(&node, requirements, "", 1000, now.Add(-4*time.Minute), "idle:warm")
	if got := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now); got.Status != "unavailable" {
		t.Fatalf("one fresh completion revived expired route evidence: %#v", got)
	}
	routeKey, _, _ := routingHealthKey(requirements)
	job.RoutingDecision = &RoutingDecision{
		RouteKey: routeKey, SelectedNodeID: node.ID,
		Candidates: []RoutingCandidateDecision{{NodeID: node.ID, Eligible: true, PerformanceContext: "idle:warm"}},
	}
	if got := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now); got.Status != "unavailable" {
		t.Fatalf("one fresh completion revived expired load-profile evidence: %#v", got)
	}

	for index, duration := range []uint64{1100, 1200, 1300, 1400} {
		recordRoutingPerformance(&node, requirements, "", duration, now.Add(time.Duration(index-3)*time.Minute), "idle:warm")
	}
	estimate := EstimateJobRuntimeAt(job, node, DefaultPlacementPolicy(), now)
	if estimate.Status != "available" || estimate.Samples != 5 || estimate.TotalP50MS != 1200 || estimate.TotalP90MS != 1400 {
		t.Fatalf("fresh distribution was unavailable or contaminated by expired outliers: %#v", estimate)
	}
}

func TestRuntimeSampleFreshnessSurvivesStoreRestart(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "qwen-test"}
	node := Node{ID: "node-persisted"}
	for index := 0; index < MinimumRuntimeEstimateSamples; index++ {
		recordRoutingPerformance(&node, requirements, "", 90000, now.Add(-30*24*time.Hour+time.Duration(index)*time.Minute), "")
	}
	recordRoutingPerformance(&node, requirements, "", 1000, now, "")

	path := filepath.Join(t.TempDir(), "relay.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketNodes), node.ID, node)
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodes, err := store.ListNodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("persisted node unavailable after restart: nodes=%#v err=%v", nodes, err)
	}
	job := Job{Status: JobAssigned, Requirements: requirements, AssignedNode: node.ID}
	if got := EstimateJobRuntimeAt(job, nodes[0], DefaultPlacementPolicy(), now.Add(time.Minute)); got.Status != "unavailable" {
		t.Fatalf("restart revived expired duration evidence: %#v", got)
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

func TestTimestampedRoutingDurationHistoryIsBoundedAndCopied(t *testing.T) {
	now := time.Now().UTC()
	values := []RoutingDurationSample{{ComputeMS: 99}, {CompletedAt: now}}
	for index := 1; index <= MaximumRoutingDurationSamples+10; index++ {
		values = append(values, RoutingDurationSample{ComputeMS: uint64(index), CompletedAt: now.Add(time.Duration(index) * time.Second)})
	}
	bounded := boundedRoutingDurationSamples(values)
	if len(bounded) != MaximumRoutingDurationSamples || bounded[0].ComputeMS != 11 || bounded[len(bounded)-1].ComputeMS != MaximumRoutingDurationSamples+10 {
		t.Fatalf("unexpected bounded timestamped history: %#v", bounded)
	}
	bounded[0].ComputeMS = 1
	if values[len(values)-MaximumRoutingDurationSamples].ComputeMS == 1 {
		t.Fatal("bounded timestamped history retained caller alias")
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
		if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
			return err
		}
		return putExecutionScopeLookup(tx.Bucket(bucketJobOwnerLookup), job.ID, job.OwnerSubject, job.TenantID)
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
	if response.Code != http.StatusNotFound {
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
