package modelregistry

import "testing"

func TestModelDownloadWindowBoundaries(t *testing.T) {
	if err := validateModelDownloadWindow(maximumModelDownloadBytes, 0); err != nil {
		t.Fatalf("exact model download limit rejected: %v", err)
	}
	if err := validateModelDownloadWindow(maximumModelDownloadBytes+1, 0); err == nil {
		t.Fatal("model resume offset one byte over the limit was accepted")
	}
	if err := validateModelDownloadWindow(maximumModelDownloadBytes-1, 1); err != nil {
		t.Fatalf("exact resumed download limit rejected: %v", err)
	}
	if err := validateModelDownloadWindow(maximumModelDownloadBytes-1, 2); err == nil {
		t.Fatal("resumed download one byte over the limit was accepted")
	}
}
