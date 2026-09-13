package cluster

import (
	"encoding/json"
	"testing"
)

func TestPrepareLocalPayloadCarriesProviderAndSession(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{"prompt":"hello","provider":"ollama","model":"not-approved"}`), Requirements{Provider: "browser", Model: "3.1 Pro", SessionID: "conversation-7"}, "local-job")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["provider"] != "browser" || payload["model"] != "3.1 Pro" || payload["session_id"] != "conversation-7" || payload["id"] != "local-job" {
		t.Fatalf("routing metadata missing from local payload: %#v", payload)
	}
}

func TestWorkerPolicyRestrictsRelayProvidersAndModels(t *testing.T) {
	worker := &Worker{cfg: WorkerConfig{
		AllowedTasks: []string{"generation"}, AllowedProviders: []string{"browser"}, AllowedModels: []string{"3.1 Pro"},
	}}
	requirements, err := worker.applyPolicy(Requirements{})
	if err != nil || requirements.Task != "generation" || requirements.Provider != "browser" || requirements.Model != "3.1 Pro" {
		t.Fatalf("default worker policy was not enforced: %#v, %v", requirements, err)
	}
	for _, denied := range []Requirements{
		{Task: "embedding", Provider: "browser", Model: "3.1 Pro"},
		{Task: "generation", Provider: "ollama", Model: "3.1 Pro"},
		{Task: "generation", Provider: "browser", Model: "other"},
	} {
		if _, err := worker.applyPolicy(denied); err == nil {
			t.Fatalf("worker accepted forbidden relay requirements: %#v", denied)
		}
	}
}
