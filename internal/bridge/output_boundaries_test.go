package bridge

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTextOutputEmojiBoundaryIsExplicitAndUTF8Safe(t *testing.T) {
	raw := strings.Repeat("x", 255) + "😀"
	output := NormalizeOutput([]byte(raw), OutputSpec{Mode: "text", MaxBytes: 256}, "test", "model", time.Millisecond)
	if !output.Truncated || output.Text != strings.Repeat("x", 255) {
		t.Fatalf("emoji crossing the byte boundary was not safely truncated: %#v", output)
	}
	if !utf8.ValidString(output.Text) || len(output.Text) > 256 {
		t.Fatalf("bounded text is not valid UTF-8: %q", output.Text)
	}
}

func TestJSONOutputExactBoundaryAndOneByteOverFailClosed(t *testing.T) {
	const limit = 256
	exactRaw := []byte(`{"v":"` + strings.Repeat("j", limit-len(`{"v":""}`)) + `"}`)
	if len(exactRaw) != limit {
		t.Fatalf("invalid exact JSON fixture: %d bytes", len(exactRaw))
	}
	exact := NormalizeOutput(exactRaw, OutputSpec{Mode: "json", MaxBytes: limit}, "test", "model", time.Millisecond)
	if exact.Error != "" || len(exact.JSON) != limit || exact.Truncated {
		t.Fatalf("exact JSON byte boundary was not preserved: %#v", exact)
	}

	overRaw := []byte(`{"v":"` + strings.Repeat("j", limit-len(`{"v":""}`)+1) + `"}`)
	if len(overRaw) != limit+1 {
		t.Fatalf("invalid over-limit JSON fixture: %d bytes", len(overRaw))
	}
	over := NormalizeOutput(overRaw, OutputSpec{Mode: "json", MaxBytes: limit}, "test", "model", time.Millisecond)
	if over.Error != "invalid_json" {
		t.Fatalf("one-byte-over JSON did not fail closed: %#v", over)
	}
	if len(over.JSON) != 0 || over.Text != "" || over.Truncated || len(over.Artifacts) != 0 {
		t.Fatalf("invalid JSON leaked a prefix or partial result: %#v", over)
	}
}
