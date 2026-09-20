package cluster

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPrepareLocalPayloadCarriesProviderAndSession(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{"prompt":"hello","provider":"ollama","model":"not-approved"}`), Requirements{Provider: "adapter", Model: "remote-model-pro", SessionID: "conversation-7"}, "local-job")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["provider"] != "adapter" || payload["model"] != "remote-model-pro" || payload["session_id"] != "conversation-7" || payload["id"] != "local-job" {
		t.Fatalf("routing metadata missing from local payload: %#v", payload)
	}
}

func TestPrepareLocalPayloadMakesCostBudgetAuthoritative(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{"prompt":"hello","max_cost_usd":999}`), Requirements{Provider: "deepseek", MaxCostUSD: 0.25}, "local-job")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["max_cost_usd"] != 0.25 {
		t.Fatalf("producer budget overrode authenticated requirements: %#v", payload)
	}
	raw, err = prepareLocalPayload([]byte(`{"prompt":"hello","max_cost_usd":999}`), Requirements{Provider: "ollama"}, "local-job")
	if err != nil {
		t.Fatal(err)
	}
	payload = nil
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["max_cost_usd"]; exists {
		t.Fatalf("unauthenticated budget survived: %#v", payload)
	}
}

func TestAdapterSessionBindingIsScopedToAuthenticatedProducer(t *testing.T) {
	forged := []byte(`{"prompt":"hello","contextbridge_session_key":"forged"}`)
	var first, second, followup map[string]interface{}
	for _, entry := range []struct {
		owner string
		out   *map[string]interface{}
	}{
		{"producer-a", &first}, {"producer-b", &second}, {"producer-a", &followup},
	} {
		raw, err := prepareLocalPayload(forged, Requirements{Provider: "adapter", SessionID: "shared-name"}, "local-job", entry.owner)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, entry.out); err != nil {
			t.Fatal(err)
		}
	}
	if first["contextbridge_session_key"] == "forged" || first["contextbridge_session_key"] == second["contextbridge_session_key"] {
		t.Fatal("producer supplied or cross-producer adapter session key was accepted")
	}
	if first["contextbridge_session_key"] != followup["contextbridge_session_key"] {
		t.Fatal("follow-up turn did not retain its producer-scoped adapter session key")
	}
}

func TestPrepareLocalPayloadMakesRequirementsSessionAuthoritative(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{"prompt":"hello","session_id":"forged","metadata":{"contextbridge_new_session":false}}`), Requirements{
		Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", AdapterFreshSession: true,
	}, "local-job", "producer-a")
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		SessionID  string                 `json:"session_id"`
		SessionKey string                 `json:"contextbridge_session_key"`
		Metadata   map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	expected := adapterSessionRoutingKey("producer-a", Requirements{Provider: "adapter", AdapterProfile: "profile-one"})
	if job.SessionID != "default" || job.SessionKey != expected || job.Metadata["contextbridge_new_session"] != true {
		t.Fatalf("worker trusted payload session or fresh-session metadata: %#v", job)
	}
}

func TestPrepareLocalPayloadRemovesUnauthenticatedAdapterRoutingHints(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{
		"prompt":"hello",
		"contextbridge_adapter_endpoint_id":999,
		"contextbridge_session_key":"cb:forged",
		"metadata":{"contextbridge_new_session":true,"contextbridge_new_session_per_job":true,"contextbridge_resume_only":true,"contextbridge_baseline_text":"forged","contextbridge_baseline_response_count":0,"contextbridge_baseline_response_identity":"forged","contextbridge_baseline_text_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","contextbridge_model_fallbacks":["Adapter Model A"],"contextbridge_reasoning_fallbacks":["Sehr hoch"],"keep":"value"}
	}`), Requirements{Task: "generation", Provider: "adapter", AdapterProfile: "profile-one"}, "local-job", "producer-a")
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]json.RawMessage
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if _, ok := job["contextbridge_adapter_endpoint_id"]; ok {
		t.Fatal("producer supplied adapter endpoint id survived without an authenticated requirement")
	}
	var sessionKey string
	if err := json.Unmarshal(job["contextbridge_session_key"], &sessionKey); err != nil || sessionKey == "cb:forged" {
		t.Fatalf("producer supplied session key survived: %q %v", sessionKey, err)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(job["metadata"], &metadata); err != nil {
		t.Fatal(err)
	}
	if _, ok := metadata["contextbridge_new_session"]; ok {
		t.Fatal("producer supplied fresh-session flag survived without an authenticated requirement")
	}
	if _, ok := metadata["contextbridge_new_session_per_job"]; ok {
		t.Fatal("producer supplied per-job flag survived without an authenticated requirement")
	}
	for _, field := range []string{
		"contextbridge_resume_only", "contextbridge_baseline_text", "contextbridge_baseline_response_count",
		"contextbridge_baseline_response_identity", "contextbridge_baseline_text_digest",
		"contextbridge_model_fallbacks", "contextbridge_reasoning_fallbacks",
	} {
		if _, ok := metadata[field]; ok {
			t.Fatalf("producer supplied recovery field %q survived", field)
		}
	}
	if string(metadata["keep"]) != `"value"` {
		t.Fatalf("unrelated metadata was not preserved: %#v", metadata)
	}
}

func TestPrepareLocalPayloadSecuresProviderlessAutomaticRoute(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{
		"route":"default","provider":"adapter","model":"disallowed-model","adapter_profile":"profile-two",
		"reasoning":"forged","session_id":"forged-session",
		"metadata":{"contextbridge_resume_only":true,"contextbridge_baseline_text":"old answer"}
	}`), Requirements{Task: "generation", SessionID: "outer-session"}, "local-job", "producer-a")
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]json.RawMessage
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"provider", "model", "adapter_profile", "reasoning"} {
		if _, ok := job[field]; ok {
			t.Fatalf("provider-less cluster route retained unauthenticated %s", field)
		}
	}
	var session, sessionKey string
	if err := json.Unmarshal(job["session_id"], &session); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(job["contextbridge_session_key"], &sessionKey); err != nil {
		t.Fatal(err)
	}
	expected := adapterSessionRoutingKey("producer-a", Requirements{Provider: "adapter", AdapterProfile: "any", SessionID: "outer-session"})
	if session != "outer-session" || sessionKey != expected {
		t.Fatalf("automatic route lost producer-scoped session authority: session=%q key=%q", session, sessionKey)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(job["metadata"], &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 0 {
		t.Fatalf("automatic route retained internal adapter controls: %#v", metadata)
	}
}

func TestAdapterProfileRequirementOverridesPayloadClaim(t *testing.T) {
	raw, err := prepareLocalPayload([]byte(`{"adapter_profile":"profile-two","prompt":"safe"}`), Requirements{
		Task: "generation", Provider: "adapter", AdapterProfile: "profile-one", Reasoning: "Sehr hoch", AdapterEndpointID: 42,
	}, "local-job", "producer")
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]interface{}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if job["adapter_profile"] != "profile-one" {
		t.Fatalf("payload profile escaped hard routing requirement: %#v", job)
	}
	if job["reasoning"] != "Sehr hoch" || job["contextbridge_adapter_endpoint_id"] != float64(42) {
		t.Fatalf("relay-selected adapter controls were not carried to the local lease boundary: %#v", job)
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
		AllowedTasks: []string{"generation"}, AllowedProviders: []string{"adapter"}, AllowedModels: []string{"remote-model-pro"},
	}}
	requirements, err := worker.applyPolicy(Requirements{})
	if err != nil || requirements.Task != "generation" || requirements.Provider != "adapter" || requirements.Model != "remote-model-pro" {
		t.Fatalf("default worker policy was not enforced: %#v, %v", requirements, err)
	}
	for _, denied := range []Requirements{
		{Task: "embedding", Provider: "adapter", Model: "remote-model-pro"},
		{Task: "generation", Provider: "ollama", Model: "remote-model-pro"},
		{Task: "generation", Provider: "adapter", Model: "other"},
	} {
		if _, err := worker.applyPolicy(denied); err == nil {
			t.Fatalf("worker accepted forbidden relay requirements: %#v", denied)
		}
	}
}

func TestAdapterModelPolicyAcceptsOrdinarySpacesForNBSPLabel(t *testing.T) {
	worker := &Worker{cfg: WorkerConfig{AllowedModels: []string{"remote-model pro"}}}
	if _, err := worker.applyPolicy(Requirements{Task: "generation", Provider: "adapter", Model: "remote-model\u00a0pro"}); err != nil {
		t.Fatalf("adapter model policy rejected equivalent whitespace: %v", err)
	}
	if _, err := worker.applyPolicy(Requirements{Task: "generation", Provider: "ollama", Model: "remote-model\u00a0pro"}); err == nil {
		t.Fatal("local model policy accepted a different exact identifier")
	}
}

func TestWorkerConsoleLabelsDoNotExposePromptOrAssumeSelectedModel(t *testing.T) {
	job := Job{Requirements: Requirements{Provider: "adapter"}, Payload: json.RawMessage(`{"prompt":"private prompt","adapter_profile":"profile-two","model":"remote-model-pro","reasoning":"high"}`)}
	provider, profile, model, reasoning := jobRequestLabels(job)
	if provider != "adapter" || profile != "profile-two" || model != "remote-model-pro" || reasoning != "high" {
		t.Fatalf("incorrect requested labels: %q %q %q %q", provider, profile, model, reasoning)
	}
	if _, got, _ := localResultSelection(json.RawMessage(`{"output":{"provider":"adapter","model":"adapter:remote-model-pro"}}`)); got != "" {
		t.Fatalf("requested model was misreported as selected: %q", got)
	}
	if _, got, level := localResultSelection(json.RawMessage(`{"output":{"provider":"adapter","selected_model":"remote-model-selected","selected_reasoning":"hoch"}}`)); got != "remote-model-selected" || level != "hoch" {
		t.Fatalf("endpoint selection was not read back: %q %q", got, level)
	}
	if provider, got, _ := localResultSelection(json.RawMessage(`{"output":{"provider":"ollama","model":"qwen"}}`)); provider != "ollama" || got != "qwen" {
		t.Fatalf("local model was not read back: %q %q", provider, got)
	}
}

func TestCompactLocalSubmissionDoesNotEchoLargeInput(t *testing.T) {
	raw := []byte(`{"job":{"id":"job-1","prompt":"private prompt","text":"private text","image_base64":"very-large-input","model":"qwen","contextbridge_session_key":"cb:private-routing-key","contextbridge_adapter_endpoint_id":42},"contextbridge_adapter_endpoint_id":42,"contextbridge_ephemeral_adapter_endpoint":true,"output":{"mode":"text","text":"answer","contextbridge_adapter_endpoint_id":42,"contextbridge_ephemeral_adapter_endpoint":true},"status":"completed"}`)
	compact, err := compactLocalSubmission(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) >= len(raw) || strings.Contains(string(compact), "private prompt") || strings.Contains(string(compact), "very-large-input") || strings.Contains(string(compact), "private-routing-key") {
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
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(compact, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["contextbridge_adapter_endpoint_id"]; ok {
		t.Fatal("top-level adapter execution metadata leaked to the producer result")
	}
	var compactJob map[string]json.RawMessage
	if err := json.Unmarshal(decoded["job"], &compactJob); err != nil {
		t.Fatal(err)
	}
	if _, ok := compactJob["contextbridge_session_key"]; ok {
		t.Fatal("opaque adapter session key leaked to the producer result")
	}
	if _, ok := compactJob["contextbridge_adapter_endpoint_id"]; ok {
		t.Fatal("internal adapter endpoint id leaked inside the producer result")
	}
	var compactOutput map[string]json.RawMessage
	if err := json.Unmarshal(decoded["output"], &compactOutput); err != nil {
		t.Fatal(err)
	}
	if _, ok := compactOutput["contextbridge_adapter_endpoint_id"]; ok {
		t.Fatal("internal adapter endpoint id leaked inside the normalized output")
	}
	if _, ok := compactOutput["contextbridge_ephemeral_adapter_endpoint"]; ok {
		t.Fatal("internal ephemeral-endpoint marker leaked inside the normalized output")
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

func TestCompactLocalSubmissionFailsClosedWhenJobEnvelopeIsMissing(t *testing.T) {
	raw := []byte(`{"prompt":"secret","contextbridge_adapter_endpoint_id":42,"output":{"mode":"text","text":"answer"}}`)
	if compact, err := compactLocalSubmission(raw); err == nil || compact != nil {
		t.Fatalf("job-less local response did not fail closed: %q, %v", compact, err)
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
