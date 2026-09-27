package strictjson

import (
	"encoding/json"
	"strings"
	"testing"
)

type nestedDocument struct {
	Priority int               `json:"priority"`
	Nested   nestedField       `json:"nested"`
	Payload  json.RawMessage   `json:"payload"`
	Labels   map[string]string `json:"labels"`
}

type nestedField struct {
	Enabled bool `json:"enabled"`
}

func TestDecodeRejectsAmbiguousStructProperties(t *testing.T) {
	for _, raw := range []string{
		`{"priority":1,"priority":2}`,
		`{"priority":1,"Priority":2}`,
		`{"nested":{"enabled":true,"Enabled":false}}`,
	} {
		var document nestedDocument
		if err := Decode([]byte(raw), &document); err == nil {
			t.Fatalf("ambiguous JSON was accepted: %s", raw)
		}
	}
}

func TestDecodePreservesOpaqueCaseSensitiveKeys(t *testing.T) {
	raw := []byte(`{"payload":{"A":1,"a":2},"labels":{"A":"one","a":"two"}}`)
	var document nestedDocument
	if err := Decode(raw, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(document.Payload), `"A":1`) || len(document.Labels) != 2 {
		t.Fatalf("opaque keys were not preserved: payload=%s labels=%v", document.Payload, document.Labels)
	}
}

func TestDecodeRejectsInvalidUTF8AndTrailingValues(t *testing.T) {
	for _, raw := range [][]byte{
		append([]byte(`{"priority":"`), 0xff, '"', '}'),
		[]byte(`{"priority":1} {"priority":2}`),
	} {
		var document nestedDocument
		if err := Decode(raw, &document); err == nil {
			t.Fatalf("invalid JSON was accepted: %q", raw)
		}
	}
}

func FuzzDecodeClosedDocument(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"priority":1,"nested":{"enabled":true}}`),
		[]byte(`{"priority":1,"Priority":2}`),
		[]byte(`{"payload":{"A":1,"a":2}}`),
		append([]byte(`{"priority":"`), 0xff, '"', '}'),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var document nestedDocument
		_ = Decode(raw, &document)
	})
}
