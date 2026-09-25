package main

import (
	"math"
	"testing"
	"time"
)

func TestDisplayConversionsRejectOrSaturateExtremeValues(t *testing.T) {
	if got := formatInt64Bytes(-1); got != "unknown" {
		t.Fatalf("negative bytes rendered as %q", got)
	}
	if got := formatFloat64Bytes(math.NaN()); got != "unknown" {
		t.Fatalf("NaN bytes rendered as %q", got)
	}
	if got := durationFromUint64Milliseconds(math.MaxUint64); got != time.Duration(math.MaxInt64) {
		t.Fatalf("unsigned duration wrapped: %v", got)
	}
	if got := durationFromInt64Milliseconds(-1); got != 0 {
		t.Fatalf("negative duration rendered as %v", got)
	}
	if got := durationFromInt64Milliseconds(math.MaxInt64); got != time.Duration(math.MaxInt64) {
		t.Fatalf("signed duration wrapped: %v", got)
	}
}

func TestPlacementMinimumSamplesConversionIsBounded(t *testing.T) {
	if got := boundedPlacementMinimumSamples(-1); got != 1 {
		t.Fatalf("negative sample count wrapped: %d", got)
	}
	if got := boundedPlacementMinimumSamples(3); got != 3 {
		t.Fatalf("valid sample count changed: %d", got)
	}
	if got := boundedPlacementMinimumSamples(int(^uint(0) >> 1)); got != 1000 {
		t.Fatalf("large sample count was not capped: %d", got)
	}
}
