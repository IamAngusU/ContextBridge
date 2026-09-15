package bridge

import (
	"encoding/base64"
	"testing"
)

// BenchmarkContextBridgeNormalize12MiBArtifact measures the local validation
// cost for an exact-maximum transferred artifact: base64 decode, media policy,
// size accounting, and SHA-256 recomputation. It excludes provider download
// time and network transport. Use a fixed -benchtime count for publication.
func BenchmarkContextBridgeNormalize12MiBArtifact(b *testing.B) {
	data := make([]byte, 12<<20)
	encoded := base64.StdEncoding.EncodeToString(data)
	input := []Artifact{{
		Name:       "maximum.bin",
		MediaType:  "application/octet-stream",
		DataBase64: encoded,
	}}
	spec := OutputSpec{Mode: "text", Artifacts: true, MaxArtifactBytes: 12 << 20}

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		output := NormalizeArtifacts(input, spec)
		if len(output) != 1 || output[0].Size != len(data) || output[0].SHA256 == "" {
			b.Fatalf("maximum artifact normalization failed: %#v", output)
		}
	}
}
