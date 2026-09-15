package cluster

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestBrowserProfileRequirementOverridesPayloadClaim(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{"browser_profile":"gemini","prompt":"safe"}`), Requirements{
		Task: "generation", Provider: "browser", BrowserProfile: "chatgpt",
	}, "local-job", "producer")
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]interface{}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if job["browser_profile"] != "chatgpt" {
		t.Fatalf("payload profile escaped hard routing requirement: %#v", job)
	}
}

func TestPrepareLocalPayloadNamespacesRAGTenantByProducer(t *testing.T) {
	prepare := func(owner, authoritativeTenant, forgedTenant string) string {
		raw, err := prepareLocalPayload(
			[]byte(`{"tenant_id":"`+forgedTenant+`","documents":[{"id":"one","text":"hello"}]}`),
			Requirements{Task: "rag_ingest", Provider: "ollama"},
			"local-rag-job",
			owner,
			authoritativeTenant,
		)
		if err != nil {
			t.Fatal(err)
		}
		var job map[string]json.RawMessage
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatal(err)
		}
		var tenant string
		if err := json.Unmarshal(job["tenant_id"], &tenant); err != nil {
			t.Fatal(err)
		}
		return tenant
	}
	a := prepare("producer-a", "shared", "forged")
	if a == "" || a == "shared" || a == "forged" {
		t.Fatalf("tenant was not replaced with an opaque producer namespace: %q", a)
	}
	if again := prepare("producer-a", "shared", "other-forgery"); again != a {
		t.Fatalf("same producer and tenant were not stable: %q != %q", again, a)
	}
	if b := prepare("producer-b", "shared", "forged"); b == a {
		t.Fatal("different producers received the same RAG tenant partition")
	}
	if _, err := prepareLocalPayload([]byte(`{"query":"hello"}`), Requirements{Task: "rag_query", Provider: "ollama"}, "local-rag-job", "producer-a"); err == nil {
		t.Fatal("cluster RAG job without a tenant was accepted")
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
	compact, err := compactLocalSubmission(raw)
	if err != nil {
		t.Fatal(err)
	}
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

func TestCompactLocalSubmissionNeverFallsBackToSensitiveRawInput(t *testing.T) {
	// Encoding <>& expands the output via json.Marshal's HTML escaping. Before
	// this regression test, that made the compact form larger and caused the
	// original secret prompt to be returned.
	raw := []byte(`{"job":{"prompt":"s"},"output":{"text":"<>&"}}`)
	compact, err := compactLocalSubmission(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) < len(raw) {
		t.Fatalf("regression fixture no longer exercises the larger re-marshaled result: raw=%d compact=%d", len(raw), len(compact))
	}
	if strings.Contains(string(compact), `"prompt"`) {
		t.Fatalf("sensitive input leaked after compaction: %s", compact)
	}
	var decoded struct {
		Job    map[string]json.RawMessage `json:"job"`
		Output struct {
			Text string `json:"text"`
		} `json:"output"`
	}
	if err := json.Unmarshal(compact, &decoded); err != nil || decoded.Output.Text != "<>&" {
		t.Fatalf("result was not preserved: %#v, %v", decoded, err)
	}
	if _, exists := decoded.Job["prompt"]; exists {
		t.Fatalf("sensitive prompt field survived compaction: %s", compact)
	}
}

func TestCompactLocalSubmissionFailsClosedOnMalformedEnvelope(t *testing.T) {
	if compact, err := compactLocalSubmission([]byte(`{"job":`)); err == nil || compact != nil {
		t.Fatalf("malformed local response did not fail closed: %q, %v", compact, err)
	}
}

func TestReadLocalSubmissionResponseAcceptsExactLimitAndRejectsOneByteMore(t *testing.T) {
	exact := bytes.Repeat([]byte{'x'}, int(maximumLocalSubmissionBytes))
	read, err := readLocalSubmissionResponse(bytes.NewReader(exact))
	if err != nil || len(read) != len(exact) {
		t.Fatalf("exact local response boundary was rejected: len=%d err=%v", len(read), err)
	}
	if _, err := readLocalSubmissionResponse(bytes.NewReader(append(exact, 'x'))); err == nil {
		t.Fatal("local response one byte over the boundary was accepted")
	}
}

func TestWorkerTruncatePreservesUTF8AtByteBoundary(t *testing.T) {
	value := "ab😀cd"
	for _, limit := range []int{3, 4, 5} {
		got := truncate(value, limit)
		if !utf8.ValidString(got) || len(got) > limit || got != "ab" {
			t.Fatalf("truncate(%q, %d) = %q; want valid byte-bounded UTF-8", value, limit, got)
		}
	}
	if got := truncate(value, 6); got != "ab😀" {
		t.Fatalf("truncate at full rune boundary = %q", got)
	}
}
