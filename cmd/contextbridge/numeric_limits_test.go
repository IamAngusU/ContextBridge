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
