package bridge

import (
	"errors"
	"strings"
	"testing"
)

func TestQuotedLogValueEscapesRecordBoundaries(t *testing.T) {
	got := quotedLogValue("normal\nforged\r\tentry")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("quoted log value contains a physical record boundary: %q", got)
	}
	if got != `"normal\\nforged\\r\tentry"` {
		t.Fatalf("unexpected quoted log value: %q", got)
	}
	if got := quotedLogError(errors.New("failure\nforged")); strings.ContainsAny(got, "\r\n") {
		t.Fatalf("quoted log error contains a physical record boundary: %q", got)
	}
	oversized := quotedLogValue(strings.Repeat("x", maximumLoggedValueBytes+100))
	if len(oversized) > maximumLoggedValueBytes+32 || !strings.Contains(oversized, "[truncated]") {
		t.Fatalf("oversized log value was not bounded: length=%d", len(oversized))
	}
}
