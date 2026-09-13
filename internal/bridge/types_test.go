package bridge

import (
	"encoding/base64"
	"strings"
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

func TestNormalizeBrowserArtifactsRecomputesIntegrityAndBounds(t *testing.T) {
	data := []byte("generated file")
	raw := []byte(`{"mode":"text","text":"done","artifacts":[{"name":"../answer.txt","media_type":"text/plain","size":999,"sha256":"forged","data_base64":"` + base64.StdEncoding.EncodeToString(data) + `"},{"name":"ignored.exe","media_type":"application/x-msdownload","data_base64":"AA=="}]}`)
	output := NormalizeOutput(raw, OutputSpec{Mode: "text", Artifacts: true}, "browser", "tab", time.Millisecond)
	if len(output.Artifacts) != 1 {
		t.Fatalf("expected one safe artifact, got %#v", output.Artifacts)
	}
	artifact := output.Artifacts[0]
	if artifact.Name != "answer.txt" || artifact.Size != len(data) || len(artifact.SHA256) != 64 {
		t.Fatalf("artifact was not normalized: %#v", artifact)
	}

	withoutOptIn := NormalizeOutput(raw, OutputSpec{Mode: "text"}, "browser", "tab", time.Millisecond)
	if len(withoutOptIn.Artifacts) != 0 {
		t.Fatal("artifacts must require explicit output.artifacts opt-in")
	}
}

func TestRequiredArtifactsCountOnlyTransferredFiles(t *testing.T) {
	spec := OutputSpec{Mode: "text", Artifacts: true, MinArtifacts: 1}
	reference := []byte(`{"mode":"text","text":"I made the image","artifacts":[{"name":"picture.png","media_type":"image/png","url":"https://chatgpt.com/picture.png"}]}`)
	missing := NormalizeOutput(reference, spec, "browser", "tab", time.Millisecond)
	if !strings.HasPrefix(missing.Error, "artifacts_missing:") {
		t.Fatalf("a link or a claimed image must not satisfy the file requirement: %#v", missing)
	}
	data := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lu8AAAAASUVORK5CYII="
	transferred := []byte(`{"mode":"text","text":"done","artifacts":[{"name":"picture.png","media_type":"image/png","data_base64":"` + data + `"}]}`)
	complete := NormalizeOutput(transferred, spec, "browser", "tab", time.Millisecond)
	if complete.Error != "" || len(complete.Artifacts) != 1 {
		t.Fatalf("verified bytes should satisfy the file requirement: %#v", complete)
	}
}

func TestRequiredImagesRejectsTextAndReferences(t *testing.T) {
	spec := OutputSpec{Mode: "text", Artifacts: true, MinImages: 1}
	text := base64.StdEncoding.EncodeToString([]byte("I created a picture"))
	for _, artifact := range []string{
		`{"name":"picture.png","media_type":"image/png","url":"https://chatgpt.com/picture.png"}`,
		`{"name":"picture.png","media_type":"image/png","data_base64":"` + text + `"}`,
		`{"name":"answer.txt","media_type":"text/plain","data_base64":"` + text + `"}`,
	} {
		output := NormalizeOutput([]byte(`{"mode":"text","text":"done","artifacts":[`+artifact+`]}`), spec, "browser", "tab", time.Millisecond)
		if !strings.HasPrefix(output.Error, "images_missing:") {
			t.Fatalf("non-image artifact passed image requirement: %#v", output)
		}
	}
	image := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lu8AAAAASUVORK5CYII="
	output := NormalizeOutput([]byte(`{"mode":"text","text":"done","artifacts":[{"name":"picture.png","media_type":"image/png","data_base64":"`+image+`"}]}`), spec, "browser", "tab", time.Millisecond)
	if output.Error != "" || len(output.Artifacts) != 1 {
		t.Fatalf("verified PNG was rejected: %#v", output)
	}
}

func TestValidateJobRequiresArtifactOptInForMinimum(t *testing.T) {
	job := Job{Prompt: "Create an image", Output: OutputSpec{Mode: "text", MinArtifacts: 1}}
	if err := validateJob(job); err == nil {
		t.Fatal("minimum artifacts without opt-in should be rejected")
	}
	job.Output.Artifacts = true
	if err := validateJob(job); err != nil {
		t.Fatalf("valid file requirement was rejected: %v", err)
	}
	job.Output = OutputSpec{Mode: "text", MinImages: 1}
	if err := validateJob(job); err == nil {
		t.Fatal("minimum images without artifact opt-in should be rejected")
	}
	job.Output.Artifacts = true
	if err := validateJob(job); err != nil {
		t.Fatalf("valid image requirement was rejected: %v", err)
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
