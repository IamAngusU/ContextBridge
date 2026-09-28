package strictjson

import (
	"bytes"
	"encoding/json"
	"testing"
)

func BenchmarkDecodeOpaque8MiB(b *testing.B) {
	payload := bytes.Repeat([]byte("0,"), 4<<20)
	payload = payload[:len(payload)-1]
	raw := make([]byte, 0, len(payload)+16)
	raw = append(raw, `{"payload":[`...)
	raw = append(raw, payload...)
	raw = append(raw, ']', '}')
	target := struct {
		Payload json.RawMessage `json:"payload"`
	}{}
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for range b.N {
		target.Payload = nil
		if err := Decode(raw, &target); err != nil {
			b.Fatal(err)
		}
	}
}
