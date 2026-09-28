package cluster

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestPoolAuthorityBindsWorkerAndExactEncryptedJob(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	authority, err := NewPoolAuthority("customer-pool", now)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := CertifyPoolWorker(authority, publicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePoolWorkerCertificate(certificate, authority.PublicKey, authority.PoolID, publicKey, now); err != nil {
		t.Fatal(err)
	}
	_, foreignPublicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePoolWorkerCertificate(certificate, authority.PublicKey, authority.PoolID, foreignPublicKey, now); err == nil {
		t.Fatal("worker certificate accepted a relay-selected foreign encryption key")
	}

	context := EncryptionContext{JobID: "job-protected", NodeID: "node-protected", Attempt: 1, OwnerSubject: "customer-producer", TenantID: "tenant-a", Requirements: Requirements{Task: "generation", Provider: "ollama"}}
	sealed, _, err := SealFor(publicKey, []byte(`{"prompt":"private"}`), JobAAD(context))
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := SignPoolJobAuthorization(authority, context, sealed, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePoolJobAuthorization(authority.PublicKey, authority.PoolID, authorization, context, sealed, now); err != nil {
		t.Fatal(err)
	}

	changedContext := context
	changedContext.Requirements.Model = "operator-selected-model"
	if err := ValidatePoolJobAuthorization(authority.PublicKey, authority.PoolID, authorization, changedContext, sealed, now); err == nil {
		t.Fatal("authorization accepted a changed execution context")
	}
	changedSealed := *sealed
	changedSealed.Ciphertext = strings.Repeat("A", len(changedSealed.Ciphertext))
	if err := ValidatePoolJobAuthorization(authority.PublicKey, authority.PoolID, authorization, context, &changedSealed, now); err == nil {
		t.Fatal("authorization accepted changed ciphertext")
	}
	if err := ValidatePoolJobAuthorization(authority.PublicKey, authority.PoolID, authorization, context, sealed, authorization.ExpiresAt); err == nil {
		t.Fatal("authorization remained valid at its exclusive expiry")
	}

	identityPath := filepath.Join(t.TempDir(), "worker-identity.json")
	worker := Worker{cfg: WorkerConfig{IdentityFile: identityPath}, identity: WorkerIdentity{NodeID: context.NodeID, PrivateKey: privateKey, PublicKey: publicKey, PoolCertificate: certificate}}
	job := Job{ID: context.JobID, AssignedNode: context.NodeID, Attempt: context.Attempt, OwnerSubject: context.OwnerSubject, TenantID: context.TenantID, Requirements: context.Requirements, SealedPayload: sealed, PoolAuthorization: authorization}
	if err := worker.validatePoolJob(job, now); err != nil {
		t.Fatal(err)
	}
	if err := worker.claimPoolJobAuthorization(job, now); err != nil {
		t.Fatal(err)
	}
	if err := worker.claimPoolJobAuthorization(job, now); err == nil {
		t.Fatal("protected worker accepted a replayed job authorization")
	}
	job.PoolAuthorization = nil
	if err := worker.validatePoolJob(job, now); err == nil {
		t.Fatal("protected worker accepted an unsigned job")
	}
}

func TestPoolAuthorizationReplayRemainsClaimedAfterWorkerRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	authority, err := NewPoolAuthority("customer-pool", now)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := CertifyPoolWorker(authority, publicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	context := EncryptionContext{JobID: "job-restart-replay", NodeID: "node-protected", Attempt: 1, OwnerSubject: "customer-producer", Requirements: Requirements{Task: "generation", Provider: "ollama"}}
	sealed, _, err := SealFor(publicKey, []byte(`{"prompt":"private"}`), JobAAD(context))
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := SignPoolJobAuthorization(authority, context, sealed, now)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: context.JobID, AssignedNode: context.NodeID, Attempt: context.Attempt, OwnerSubject: context.OwnerSubject, Requirements: context.Requirements, SealedPayload: sealed, PoolAuthorization: authorization}
	identity := WorkerIdentity{NodeID: context.NodeID, PrivateKey: privateKey, PublicKey: publicKey, PoolCertificate: certificate}
	identityPath := filepath.Join(t.TempDir(), "worker-identity.json")

	firstProcess := Worker{cfg: WorkerConfig{IdentityFile: identityPath}, identity: identity}
	if err := firstProcess.claimPoolJobAuthorization(job, now); err != nil {
		t.Fatal(err)
	}
	secondProcess := Worker{cfg: WorkerConfig{IdentityFile: identityPath}, identity: identity}
	if err := secondProcess.claimPoolJobAuthorization(job, now.Add(time.Second)); err == nil {
		t.Fatal("restarted worker accepted an already claimed customer pool authorization")
	}
}

func TestPoolAuthorizationClaimFailsClosedForCorruptReplayState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "worker-identity.json.pool-replay.db")
	signature := "signed-authorization"
	if err := claimPoolAuthorization(path, signature, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}

	database, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimKey := sha256.Sum256([]byte(signature))
	if err := database.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(poolAuthorizationClaimsBucket).Put(claimKey[:], []byte("not-a-time"))
	}); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if err := claimPoolAuthorization(path, signature, now.Add(time.Hour), now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "invalid claim") {
		t.Fatalf("corrupt replay state did not fail closed: %v", err)
	}
}

func TestPoolAuthorizationClaimFailsClosedWithoutDurableIdentity(t *testing.T) {
	worker := Worker{identity: WorkerIdentity{PoolCertificate: &PoolWorkerCertificate{}}}
	job := Job{PoolAuthorization: &PoolJobAuthorization{Signature: "signed", ExpiresAt: time.Now().UTC().Add(time.Hour)}}
	if err := worker.claimPoolJobAuthorization(job, time.Now().UTC()); err == nil {
		t.Fatal("protected worker accepted a claim without durable replay storage")
	}
}

func TestPoolAuthorityFileIsValidatedAndNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool-authority.json")
	created, err := CreatePoolAuthorityFile(path, "customer-pool", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPoolAuthority(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PoolID != created.PoolID || loaded.PublicKey != created.PublicKey || loaded.PrivateKey != created.PrivateKey {
		t.Fatalf("loaded authority changed: %#v", loaded)
	}
	if _, err := CreatePoolAuthorityFile(path, "replacement", time.Now().UTC()); !os.IsExist(err) {
		t.Fatalf("authority creation overwrote an existing key: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tampered PoolAuthority
	if err := json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.PrivateKey = tampered.PublicKey
	tamperedRaw, _ := json.Marshal(tampered)
	if err := os.WriteFile(path, tamperedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPoolAuthority(path); err == nil {
		t.Fatal("authority loader accepted mismatched private material")
	}
}

func TestPoolWorkerCertificateSurvivesRelayPairingAndReconnect(t *testing.T) {
	now := time.Now().UTC()
	authority, err := NewPoolAuthority("customer-pool", now)
	if err != nil {
		t.Fatal(err)
	}
	_, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := CertifyPoolWorker(authority, publicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pair, err := store.CreatePairing(PairRequest{NodeName: "protected", PublicKey: publicKey, PoolCertificate: certificate}, "https://relay.example/#pair", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.DecidePairing(pair.UserCode, true)
	if err != nil {
		t.Fatal(err)
	}
	state, delivered, _, err := store.PollPairing(pair.DeviceCode)
	if err != nil || state != "approved" || delivered.NodeID != approved.NodeID {
		t.Fatalf("pairing delivery failed: %s %#v %v", state, delivered, err)
	}
	node, err := store.GetNode(approved.NodeID)
	if err != nil || !poolWorkerCertificatesEqual(node.PoolCertificate, certificate) {
		t.Fatalf("paired node lost its pool certificate: %#v %v", node, err)
	}
	if err := store.UpsertNodePinned(Node{ID: node.ID, Name: node.Name, PublicKey: publicKey, Connected: true, State: "online"}); err != nil {
		t.Fatal(err)
	}
	reconnected, err := store.GetNode(node.ID)
	if err != nil || !poolWorkerCertificatesEqual(reconnected.PoolCertificate, certificate) {
		t.Fatalf("reconnect stripped the pinned pool certificate: %#v %v", reconnected, err)
	}
}

func TestPoolAssignmentSelectionKeepsMixedFleetCompatible(t *testing.T) {
	now := time.Now().UTC()
	authority, err := NewPoolAuthority("customer-pool", now)
	if err != nil {
		t.Fatal(err)
	}
	_, protectedKey, _ := NewIdentity()
	protectedCertificate, err := CertifyPoolWorker(authority, protectedKey, now)
	if err != nil {
		t.Fatal(err)
	}
	_, ordinaryKey, _ := NewIdentity()
	foreignAuthority, _ := NewPoolAuthority("customer-pool", now)
	_, foreignKey, _ := NewIdentity()
	foreignCertificate, _ := CertifyPoolWorker(foreignAuthority, foreignKey, now)
	nodes := []Node{
		{ID: "ordinary", PublicKey: ordinaryKey},
		{ID: "protected", PublicKey: protectedKey, PoolCertificate: protectedCertificate},
		{ID: "foreign", PublicKey: foreignKey, PoolCertificate: foreignCertificate},
	}
	ordinary := nodesForPoolAssignment(nodes, "", "", now)
	if len(ordinary) != 1 || ordinary[0].ID != "ordinary" {
		t.Fatalf("ordinary assignment candidates = %#v", ordinary)
	}
	protected := nodesForPoolAssignment(nodes, authority.PoolID, authority.PublicKey, now)
	if len(protected) != 1 || protected[0].ID != "protected" {
		t.Fatalf("protected assignment candidates = %#v", protected)
	}
	request := AssignmentRequest{}
	if err := authority.BindAssignmentRequest(&request); err != nil || request.PoolID != authority.PoolID || request.PoolAuthorityKey != authority.PublicKey {
		t.Fatalf("authority did not bind assignment request: %#v %v", request, err)
	}
	if err := validatePoolAssignmentSelector(request.PoolID, ""); err == nil {
		t.Fatal("partial pool selector was accepted")
	}
}
