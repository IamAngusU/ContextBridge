package bridge

import (
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/vectorstore"
)

func TestNormalizeDecisionNeverBlocks(t *testing.T) {
	decision := NormalizeDecision([]byte(`{"verdict":"block","flags":["hate"],"confidence":0.9}`), "test", "model", time.Millisecond)
	if decision.Verdict != "review" {
		t.Fatalf("expected review, got %s", decision.Verdict)
	}
}

func TestNormalizeDecisionExtractsJSON(t *testing.T) {
	decision := NormalizeDecision([]byte("result: {\"verdict\":\"allow\",\"flags\":[],\"confidence\":0.8}"), "test", "model", time.Millisecond)
	if decision.Verdict != "allow" {
		t.Fatalf("expected allow, got %s", decision.Verdict)
	}
}

func TestNormalizeJSONOutputRequiresKeys(t *testing.T) {
	spec := OutputSpec{Mode: "json", RequiredKeys: []string{"topic", "summary"}}
	output := NormalizeOutput([]byte("answer: {\"topic\":\"setup\",\"summary\":\"Ready\"}"), spec, "test", "model", time.Millisecond)
	if output.Error != "" || string(output.JSON) != `{"topic":"setup","summary":"Ready"}` {
		t.Fatalf("unexpected JSON output: %#v", output)
	}

	missing := NormalizeOutput([]byte(`{"topic":"setup"}`), spec, "test", "model", time.Millisecond)
	if missing.Error != "missing_required_key:summary" {
		t.Fatalf("expected missing key error, got %#v", missing)
	}
}

func TestNormalizeBrowserOutputEnvelopeKeepsTrustedModel(t *testing.T) {
	raw := []byte(`{"mode":"json","json":{"language":"de"},"model":"browser-model"}`)
	output := NormalizeOutput(raw, OutputSpec{Mode: "json", RequiredKeys: []string{"language"}}, "browser", "fallback", time.Millisecond)
	if output.Error != "" || output.Model != "fallback" || string(output.JSON) != `{"language":"de"}` {
		t.Fatalf("unexpected browser output: %#v", output)
	}
}

func TestNormalizeTextOutputIsBounded(t *testing.T) {
	output := NormalizeOutput([]byte("abcdef"), OutputSpec{Mode: "text", MaxBytes: 4}, "test", "model", time.Millisecond)
	if output.Text != "abcdef" {
		t.Fatalf("limits below the minimum must use the default, got %q", output.Text)
	}
	output = NormalizeOutput([]byte("abcdefghijklmnopqrstuvwxyz"), OutputSpec{Mode: "text", MaxBytes: 256}, "test", "model", time.Millisecond)
	if output.Text != "abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("unexpected text output: %q", output.Text)
	}
}

func TestValidateJobRejectsUnsafeIDAndUnknownOutput(t *testing.T) {
	job := Job{ID: "../../outside", Prompt: "Review", Output: OutputSpec{Mode: "decision"}}
	if err := validateJob(job); err == nil {
		t.Fatal("expected an unsafe job ID to be rejected")
	}
	job = Job{Prompt: "Review", Output: OutputSpec{Mode: "command"}}
	if err := validateJob(job); err == nil {
		t.Fatal("expected an executable output mode to be rejected")
	}
}

func TestRouteTaskOverridesSubmittedTask(t *testing.T) {
	job := Job{Task: "rag_query", Kind: "generation"}
	if got := jobTask(job, "moderation"); got != "moderation" {
		t.Fatalf("route task was not authoritative: %s", got)
	}
	if got := jobTask(job, ""); got != "rag_query" {
		t.Fatalf("generic route did not accept submitted task: %s", got)
	}
}

func TestRouteTaskEnforcesOutputContract(t *testing.T) {
	job := Job{Task: "generation", Output: OutputSpec{Mode: "text", RequiredKeys: []string{"summary"}}}
	applyTaskOutput(&job, "extraction")
	if job.Output.Mode != "json" || len(job.Output.RequiredKeys) != 1 {
		t.Fatalf("extraction contract was not enforced: %#v", job.Output)
	}
	applyTaskOutput(&job, "embedding")
	if job.Output.Mode != "embedding" {
		t.Fatalf("embedding contract was not enforced: %#v", job.Output)
	}
}

func TestValidateJobRequiresRAGTenantAndPayload(t *testing.T) {
	job := Job{Task: "rag_ingest", Documents: []vectorstore.Document{{ID: "doc", Text: "content"}}, Output: OutputSpec{Mode: "rag"}}
	if err := validateJob(job); err == nil {
		t.Fatal("expected RAG tenant to be required")
	}
	job.TenantID = "tenant-a"
	if err := validateJob(job); err != nil {
		t.Fatalf("valid RAG ingest was rejected: %v", err)
	}
}
