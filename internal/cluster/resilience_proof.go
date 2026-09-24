package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const ResilienceProofV1 = "contextbridge.resilience-proof.v1"

// ResilienceProofReport is a content-free, locally reproducible proof of a
// small set of durable coordination invariants. It does not contact a provider
// or claim to prove network, operating-system, or multi-relay behavior.
type ResilienceProofReport struct {
	Schema               string                 `json:"schema"`
	ContextBridgeVersion string                 `json:"contextbridge_version"`
	Passed               bool                   `json:"passed"`
	GeneratedAt          time.Time              `json:"generated_at"`
	DurationMS           int64                  `json:"duration_ms"`
	Scope                string                 `json:"scope"`
	Checks               []ResilienceProofCheck `json:"checks"`
}

type ResilienceProofCheck struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// RunResilienceProof exercises isolated temporary Bolt stores. The checks are
// deliberately independent so one failure cannot make later rows look passed
// merely because a shared setup happened to survive.
func RunResilienceProof(contextBridgeVersion string) (ResilienceProofReport, error) {
	started := time.Now()
	root, err := os.MkdirTemp("", "contextbridge-resilience-proof-")
	if err != nil {
		return ResilienceProofReport{}, err
	}
	defer os.RemoveAll(root)
	report := ResilienceProofReport{
		Schema: ResilienceProofV1, ContextBridgeVersion: cleanLabel(contextBridgeVersion, 40),
		Passed: true, GeneratedAt: time.Now().UTC(), Scope: "isolated local durable-store invariants; no network, no provider, no model, and no AI request",
	}
	add := func(id string, check func(string) (string, error)) {
		detail, checkErr := check(filepath.Join(root, id+".db"))
		passed := checkErr == nil
		if checkErr != nil {
			detail = proofDetail(checkErr.Error())
		}
		report.Checks = append(report.Checks, ResilienceProofCheck{ID: id, Passed: passed, Detail: proofDetail(detail)})
		if !passed {
			report.Passed = false
		}
	}

	add("authority.monotonic_restart", proveMonotonicAuthority)
	add("admission.idempotent_restart", proveIdempotentAdmissionRestart)
	add("execution.restart_ambiguity", proveRestartAmbiguity)
	add("assignment.stale_fence", proveStaleFenceRejection)
	add("result.durable_single_terminal", proveDurableTerminalResult)
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

func proveMonotonicAuthority(path string) (string, error) {
	firstStore, err := OpenStore(path)
	if err != nil {
		return "", err
	}
	first, err := firstStore.AcquireRelayAuthority()
	if err != nil {
		_ = firstStore.Close()
		return "", err
	}
	if err := firstStore.Close(); err != nil {
		return "", err
	}
	secondStore, err := OpenStore(path)
	if err != nil {
		return "", err
	}
	defer secondStore.Close()
	second, err := secondStore.AcquireRelayAuthority()
	if err != nil {
		return "", err
	}
	if !first.Valid() || second.ClusterID != first.ClusterID || second.Epoch != first.Epoch+1 {
		return "", fmt.Errorf("authority did not preserve cluster identity and advance exactly once")
	}
	return fmt.Sprintf("stable cluster identity; epoch %d -> %d", first.Epoch, second.Epoch), nil
}

func proveIdempotentAdmissionRestart(path string) (string, error) {
	request := SubmitRequest{ID: "proof-idempotent", OwnerSubject: "proof-producer", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{"prompt":"proof"}`)}
	hash := proofHash("same-request")
	store, err := OpenStore(path)
	if err != nil {
		return "", err
	}
	first, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "proof-key", hash)
	if err != nil || replayed {
		_ = store.Close()
		return "", fmt.Errorf("first admission: replayed=%v err=%w", replayed, err)
	}
	if err := store.Close(); err != nil {
		return "", err
	}
	store, err = OpenStore(path)
	if err != nil {
		return "", err
	}
	defer store.Close()
	second, replayed, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "proof-key", hash)
	if err != nil || !replayed || second.ID != first.ID {
		return "", fmt.Errorf("exact retry did not return the original durable job")
	}
	if _, _, err := store.CreateJobAdmittedIdempotent(request, 10, 10, "proof-key", proofHash("changed-request")); !errors.Is(err, ErrIdempotencyConflict) {
		return "", fmt.Errorf("changed request reused an idempotency key: %v", err)
	}
	queued, err := store.QueuedJobs(10)
	if err != nil || len(queued) != 1 || queued[0].ID != first.ID {
		return "", fmt.Errorf("idempotent retry changed queue cardinality")
	}
	return "lost-response retry returned one original queued job; changed request rejected", nil
}

func proveRestartAmbiguity(path string) (string, error) {
	store, err := OpenStore(path)
	if err != nil {
		return "", err
	}
	authority, err := store.AcquireRelayAuthority()
	if err != nil {
		_ = store.Close()
		return "", err
	}
	job, err := store.CreateJob(SubmitRequest{ID: "proof-ambiguous", OwnerSubject: "proof-producer", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err == nil {
		job, err = store.AssignJobFencedWithDecision(job.ID, "proof-node", proofRoutingDecision(), authority)
	}
	if err == nil {
		job, err = store.MarkRunningFenced(job.ID, "proof-node", job.Attempt, job.AssignmentFence)
	}
	if err != nil {
		_ = store.Close()
		return "", err
	}
	if err := store.Close(); err != nil {
		return "", err
	}
	store, err = OpenStore(path)
	if err != nil {
		return "", err
	}
	defer store.Close()
	updated, err := store.RecoverRelayRestart("proof relay restart")
	if err != nil || len(updated) != 1 || updated[0].ID != job.ID {
		return "", fmt.Errorf("restart recovery did not isolate the running execution")
	}
	stored, err := store.GetJob(job.ID)
	if err != nil || stored.Status != JobFailed || stored.FailureCode != FailureExecutionStateAmbiguous {
		return "", fmt.Errorf("running execution was not terminally ambiguous")
	}
	queued, err := store.QueuedJobs(10)
	if err != nil || len(queued) != 0 {
		return "", fmt.Errorf("ambiguous execution became eligible for replay")
	}
	events, err := store.ListJobEvents(job.ID, 0, 100)
	if err != nil || countProofEvent(events.Events, "job.ambiguous") != 1 {
		return "", fmt.Errorf("ambiguity lacks exactly one authoritative event")
	}
	return "running work became terminally ambiguous and was not requeued", nil
}

func proveStaleFenceRejection(path string) (string, error) {
	store, err := OpenStore(path)
	if err != nil {
		return "", err
	}
	defer store.Close()
	oldAuthority, err := store.AcquireRelayAuthority()
	if err != nil {
		return "", err
	}
	currentAuthority, err := store.AcquireRelayAuthority()
	if err != nil {
		return "", err
	}
	job, err := store.CreateJob(SubmitRequest{ID: "proof-fence", OwnerSubject: "proof-producer", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err == nil {
		job, err = store.AssignJobFencedWithDecision(job.ID, "proof-node", proofRoutingDecision(), currentAuthority)
	}
	if err != nil {
		return "", err
	}
	stale := *job.AssignmentFence
	stale.RelayEpoch = oldAuthority.Epoch
	if _, err := store.MarkRunningFenced(job.ID, "proof-node", job.Attempt, &stale); !errors.Is(err, ErrAssignmentFenceMismatch) {
		return "", fmt.Errorf("stale epoch was not rejected: %v", err)
	}
	unchanged, err := store.GetJob(job.ID)
	if err != nil || unchanged.Status != JobAssigned {
		return "", fmt.Errorf("stale transition mutated durable job state")
	}
	return fmt.Sprintf("epoch %d rejected after epoch %d assignment; state unchanged", oldAuthority.Epoch, currentAuthority.Epoch), nil
}

func proveDurableTerminalResult(path string) (string, error) {
	store, err := OpenStore(path)
	if err != nil {
		return "", err
	}
	authority, err := store.AcquireRelayAuthority()
	if err != nil {
		_ = store.Close()
		return "", err
	}
	job, err := store.CreateJob(SubmitRequest{ID: "proof-terminal", OwnerSubject: "proof-producer", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err == nil {
		job, err = store.AssignJobFencedWithDecision(job.ID, "proof-node", proofRoutingDecision(), authority)
	}
	if err == nil {
		job, err = store.MarkRunningFenced(job.ID, "proof-node", job.Attempt, job.AssignmentFence)
	}
	result := json.RawMessage(`{"output":{"mode":"text","text":"proof"}}`)
	if err == nil {
		job, err = store.CompleteJobWithFailureFenced(job.ID, "proof-node", job.Attempt, job.AssignmentFence, result, nil, Usage{}, "", "")
	}
	if err != nil || job.Status != JobCompleted {
		_ = store.Close()
		return "", fmt.Errorf("complete durable job: %w", err)
	}
	if _, err := store.CompleteJobWithFailureFenced(job.ID, "proof-node", job.Attempt, job.AssignmentFence, result, nil, Usage{}, "", ""); err == nil {
		_ = store.Close()
		return "", fmt.Errorf("second terminal commit was accepted")
	}
	if err := store.Close(); err != nil {
		return "", err
	}
	store, err = OpenStore(path)
	if err != nil {
		return "", err
	}
	defer store.Close()
	stored, err := store.GetJob(job.ID)
	if err != nil || stored.Status != JobCompleted || string(stored.Result) != string(result) {
		return "", fmt.Errorf("terminal result did not survive restart exactly")
	}
	events, err := store.ListJobEvents(job.ID, 0, 100)
	if err != nil || countProofEvent(events.Events, "job.completed") != 1 {
		return "", fmt.Errorf("terminal result lacks exactly one authoritative completion event")
	}
	return "one terminal commit and exact result survived store restart", nil
}

func proofHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func proofRoutingDecision() RoutingDecision {
	return RoutingDecision{
		ID: "proof-route", SelectedNodeID: "proof-node",
		Candidates: []RoutingCandidateDecision{{NodeID: "proof-node", Eligible: true}},
	}
}

func countProofEvent(events []JobEvent, wanted string) int {
	count := 0
	for _, event := range events {
		if event.Type == wanted {
			count++
		}
	}
	return count
}

func proofDetail(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, strings.TrimSpace(value))
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}
