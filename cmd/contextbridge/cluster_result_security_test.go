package main

import (
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestClusterCompletedResultFailsClosedOnRepresentationMismatch(t *testing.T) {
	context := cluster.EncryptionContext{}
	if _, err := clusterCompletedResult(cluster.Job{Result: []byte("relay plaintext")}, true, "", context); err == nil || !strings.Contains(err.Error(), "no encrypted result") {
		t.Fatalf("E2EE plaintext downgrade was accepted: %v", err)
	}
	if _, err := clusterCompletedResult(cluster.Job{
		Result:       []byte("relay plaintext"),
		SealedResult: &cluster.SealedEnvelope{Ciphertext: "not-valid"},
	}, true, "", context); err == nil || !strings.Contains(err.Error(), "plaintext alongside") {
		t.Fatalf("ambiguous E2EE representation was accepted: %v", err)
	}
	if _, err := clusterCompletedResult(cluster.Job{SealedResult: &cluster.SealedEnvelope{Ciphertext: "not-valid"}}, false, "", context); err == nil || !strings.Contains(err.Error(), "plaintext job") {
		t.Fatalf("unexpected ciphertext was accepted for a plaintext job: %v", err)
	}
	want := "clear result"
	got, err := clusterCompletedResult(cluster.Job{Result: []byte(want)}, false, "", context)
	if err != nil || string(got) != want {
		t.Fatalf("plaintext result changed: %q, %v", got, err)
	}
}
