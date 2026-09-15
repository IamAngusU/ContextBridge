package cluster

import (
	"encoding/json"
	"strings"
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

func TestBrowserSessionBindingIsScopedToAuthenticatedProducer(t *testing.T) {
	forged := []byte(`{"prompt":"hello","contextbridge_session_key":"forged"}`)
	var first, second, followup map[string]interface{}
	for _, entry := range []struct {
		owner string
		out   *map[string]interface{}
	}{
		{"producer-a", &first}, {"producer-b", &second}, {"producer-a", &followup},
	} {
		raw, err := prepareLocalPayload(forged, Requirements{Provider: "browser", SessionID: "shared-name"}, "local-job", entry.owner)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, entry.out); err != nil {
			t.Fatal(err)
		}
	}
	if first["contextbridge_session_key"] == "forged" || first["contextbridge_session_key"] == second["contextbridge_session_key"] {
		t.Fatal("producer supplied or cross-producer browser session key was accepted")
	}
	if first["contextbridge_session_key"] != followup["contextbridge_session_key"] {
		t.Fatal("follow-up turn did not retain its producer-scoped browser session key")
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

func TestBrowserModelPolicyAcceptsOrdinarySpacesForNBSPLabel(t *testing.T) {
	worker := &Worker{cfg: WorkerConfig{AllowedModels: []string{"3.1 Pro"}}}
	if _, err := worker.applyPolicy(Requirements{Task: "generation", Provider: "browser", Model: "3.1\u00a0Pro"}); err != nil {
		t.Fatalf("browser model policy rejected equivalent whitespace: %v", err)
	}
	if _, err := worker.applyPolicy(Requirements{Task: "generation", Provider: "ollama", Model: "3.1\u00a0Pro"}); err == nil {
		t.Fatal("local model policy accepted a different exact identifier")
	}
}

func TestWorkerConsoleLabelsDoNotExposePromptOrAssumeSelectedModel(t *testing.T) {
	job := Job{Requirements: Requirements{Provider: "browser"}, Payload: json.RawMessage(`{"prompt":"private prompt","browser_profile":"gemini","model":"3.1 Pro","reasoning":"high"}`)}
	provider, profile, model, reasoning := jobRequestLabels(job)
	if provider != "browser" || profile != "gemini" || model != "3.1 Pro" || reasoning != "high" {
		t.Fatalf("incorrect requested labels: %q %q %q %q", provider, profile, model, reasoning)
	}
	if _, got, _ := localResultSelection(json.RawMessage(`{"output":{"provider":"browser","model":"browser:3.1 Pro"}}`)); got != "" {
		t.Fatalf("requested model was misreported as selected: %q", got)
	}
	if _, got, level := localResultSelection(json.RawMessage(`{"output":{"provider":"browser","selected_model":"Pro Erweitert","selected_reasoning":"hoch"}}`)); got != "Pro Erweitert" || level != "hoch" {
		t.Fatalf("tab selection was not read back: %q %q", got, level)
	}
	if provider, got, _ := localResultSelection(json.RawMessage(`{"output":{"provider":"ollama","model":"qwen"}}`)); provider != "ollama" || got != "qwen" {
		t.Fatalf("local model was not read back: %q %q", provider, got)
	}
}

func TestCompactLocalSubmissionDoesNotEchoLargeInput(t *testing.T) {
	raw := []byte(`{"job":{"id":"job-1","prompt":"private prompt","text":"private text","image_base64":"very-large-input","model":"qwen"},"output":{"mode":"text","text":"answer"},"status":"completed"}`)
	compact := compactLocalSubmission(raw)
	if len(compact) >= len(raw) || strings.Contains(string(compact), "private prompt") || strings.Contains(string(compact), "very-large-input") {
		t.Fatalf("large input was echoed in the cluster result: %s", compact)
	}
	var submission struct {
		Job struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"job"`
		Output struct {
			Text string `json:"text"`
		} `json:"output"`
	}
	if err := json.Unmarshal(compact, &submission); err != nil || submission.Job.ID != "job-1" || submission.Job.Model != "qwen" || submission.Output.Text != "answer" {
		t.Fatalf("useful result metadata was lost: %#v, %v", submission, err)
	}
}
