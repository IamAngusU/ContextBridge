package bridge

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJobRejectsDuplicatePropertiesRecursively(t *testing.T) {
	for _, raw := range []string{
		`{"prompt":"first","prompt":"second"}`,
		`{"prompt":"safe","output":{"mode":"text","mode":"json"}}`,
		`{"prompt":"safe","metadata":{"scope":"a","scope":"b"}}`,
	} {
		var job Job
		if err := decodeJSON(strings.NewReader(raw), &job, 1<<20); err == nil {
			t.Fatalf("ambiguous job JSON was accepted: %s", raw)
		}
	}
}

// FuzzJobContractJSON exercises the complete local JSON boundary rather than
// only individual label helpers. Any accepted input must remain valid after a
// canonical JSON round trip and must satisfy the structural invariants relied
// on by the executors.
func FuzzJobContractJSON(f *testing.F) {
	for _, seed := range []string{
		`{"prompt":"hello","output":{"mode":"text","max_bytes":256}}`,
		`{"prompt":"hello","unknown":true}`,
		`{"prompt":"first","prompt":"second"}`,
		`{"task":"embedding","texts":["one","two"],"output":{"mode":"embedding"}}`,
		`{"task":"rag_query","tenant_id":"tenant-a","query":"needle","output":{"mode":"rag"}}`,
		`{"prompt":"x","output":{"artifacts":true,"min_images":12,"min_media":1}}`,
		`null`, `[]`, `{"prompt":null}`, `{"prompt":"x","max_cost_usd":1e1000}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		var job Job
		if err := decodeJSON(bytes.NewReader(raw), &job, 1<<20); err != nil {
			return
		}
		if err := validateJob(job); err != nil {
			return
		}

		canonical, err := json.Marshal(job)
		if err != nil {
			t.Fatalf("accepted job did not marshal: %v", err)
		}
		var roundTrip Job
		if err := decodeJSON(bytes.NewReader(canonical), &roundTrip, 1<<20); err != nil {
			t.Fatalf("accepted job did not decode after canonical round trip: %v", err)
		}
		if err := validateJob(roundTrip); err != nil {
			t.Fatalf("accepted job became invalid after canonical round trip: %v", err)
		}
		mode := outputMode(roundTrip.Output)
		if mode != "decision" && mode != "json" && mode != "text" && mode != "embedding" && mode != "rag" {
			t.Fatalf("accepted job retained unsupported output mode %q", mode)
		}
		if roundTrip.Output.MinImages+roundTrip.Output.MinMedia > 12 {
			t.Fatalf("accepted job retained impossible typed artifact minimums: %+v", roundTrip.Output)
		}
	})
}
