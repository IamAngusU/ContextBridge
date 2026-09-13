package cluster

import (
	"encoding/json"
	"testing"
)

func TestPrepareLocalPayloadCarriesProviderAndSession(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{"prompt":"hello"}`), Requirements{Provider: "browser", SessionID: "conversation-7"}, "local-job")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["provider"] != "browser" || payload["session_id"] != "conversation-7" || payload["id"] != "local-job" {
		t.Fatalf("routing metadata missing from local payload: %#v", payload)
	}
}
