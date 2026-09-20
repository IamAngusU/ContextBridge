package cluster

import (
	"bytes"
	"testing"
)

func TestSealedRoundTripAndAAD(t *testing.T) {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("job:one:node")
	envelope, producerShared, err := SealFor(publicKey, []byte("private payload"), aad)
	if err != nil {
		t.Fatal(err)
	}
	plain, workerShared, err := OpenWith(privateKey, envelope, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, []byte("private payload")) || producerShared != workerShared {
		t.Fatal("shared secret or plaintext differs")
	}
	if _, _, err := OpenWith(privateKey, envelope, []byte("job:two:node")); err == nil {
		t.Fatal("changed AAD must fail authentication")
	}
	result, err := SealResponse(workerShared, []byte("private result"), []byte("result:one:node"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenResponse(producerShared, result, []byte("result:one:node"))
	if err != nil || !bytes.Equal(opened, []byte("private result")) {
		t.Fatalf("result roundtrip failed: %v", err)
	}
}
