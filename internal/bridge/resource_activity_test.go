package bridge

import (
	"testing"
	"time"
)

func TestNormalizeResourceActivityOptInAndNoModelNarration(t *testing.T) {
	for _, mode := range []string{"text", "json", "decision"} {
		raw := []byte(`{"mode":"` + mode + `","text":"done","json":{"ok":true},"activity":{"schema":"contextbridge.resource-activity.v1","items":[{"id":"a1","kind":"tool","action":"executed","label":"inspect"}]}}`)
		for _, provider := range []string{"adapter", "ollama"} {
			for _, optIn := range []bool{false, true} {
				output := NormalizeOutput(raw, OutputSpec{Mode: mode, Activity: optIn}, provider, "model", time.Millisecond)
				if (len(output.Activity) > 0) != (optIn && provider == "adapter") {
					t.Fatalf("%s %s %v: %+v", mode, provider, optIn, output)
				}
			}
		}
	}
	output := NormalizeOutput([]byte(`{"mode":"text","text":"write completed","activity":{"schema":"wrong"}}`),
		OutputSpec{Mode: "text", Activity: true}, "adapter", "model", 0)
	if output.Error != "" || output.Text != "write completed" || output.ActivityStatus != "invalid" || len(output.Activity) != 0 {
		t.Fatalf("bad optional telemetry changed action outcome: %+v", output)
	}
	output = NormalizeOutput([]byte(`{"mode":"text","text":"I read file.md and saw photo.png"}`),
		OutputSpec{Mode: "text", Activity: true}, "adapter", "model", 0)
	if output.ActivityStatus != "not_reported" || len(output.Activity) != 0 {
		t.Fatal("narration promoted into tool evidence")
	}
}
