package bridge

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/vectorstore"
)

func TestNormalizeDecisionNeverBlocks(t *testing.T) {
	decision := NormalizeDecision([]byte(`{"verdict":"block","flags":["hate"],"confidence":0.9}`), "test", "model", time.Millisecond)
	if decision.Verdict != "review" {
		t.Fatalf("expected review, got %s", decision.Verdict)
	}
}

func TestNormalizeAdapterArtifactsRecomputesIntegrityAndBounds(t *testing.T) {
	data := []byte("generated file")
	raw := []byte(`{"mode":"text","text":"done","artifacts":[{"name":"../answer.txt","media_type":"text/plain","size":999,"sha256":"forged","data_base64":"` + base64.StdEncoding.EncodeToString(data) + `"},{"name":"ignored.exe","media_type":"application/x-msdownload","data_base64":"AA=="}]}`)
	output := NormalizeOutput(raw, OutputSpec{Mode: "text", Artifacts: true}, "adapter", "endpoint", time.Millisecond)
	if len(output.Artifacts) != 1 {
		t.Fatalf("expected one safe artifact, got %#v", output.Artifacts)
	}
	artifact := output.Artifacts[0]
	if artifact.Name != "answer.txt" || artifact.Size != len(data) || len(artifact.SHA256) != 64 {
		t.Fatalf("artifact was not normalized: %#v", artifact)
	}

	withoutOptIn := NormalizeOutput(raw, OutputSpec{Mode: "text"}, "adapter", "endpoint", time.Millisecond)
	if len(withoutOptIn.Artifacts) != 0 {
		t.Fatal("artifacts must require explicit output.artifacts opt-in")
	}
}

func TestRequiredArtifactsCountOnlyTransferredFiles(t *testing.T) {
	spec := OutputSpec{Mode: "text", Artifacts: true, MinArtifacts: 1}
	reference := []byte(`{"mode":"text","text":"I made the image","artifacts":[{"name":"picture.png","media_type":"image/png","url":"https://profile-one.com/picture.png"}]}`)
	missing := NormalizeOutput(reference, spec, "adapter", "endpoint", time.Millisecond)
	if !strings.HasPrefix(missing.Error, "artifacts_missing:") {
		t.Fatalf("a link or a claimed image must not satisfy the file requirement: %#v", missing)
	}
	data := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lu8AAAAASUVORK5CYII="
	transferred := []byte(`{"mode":"text","text":"done","artifacts":[{"name":"picture.png","media_type":"image/png","data_base64":"` + data + `"}]}`)
	complete := NormalizeOutput(transferred, spec, "adapter", "endpoint", time.Millisecond)
	if complete.Error != "" || len(complete.Artifacts) != 1 {
		t.Fatalf("verified bytes should satisfy the file requirement: %#v", complete)
	}
}

func TestRequiredImagesRejectsTextAndReferences(t *testing.T) {
	spec := OutputSpec{Mode: "text", Artifacts: true, MinImages: 1}
	text := base64.StdEncoding.EncodeToString([]byte("I created a picture"))
	for _, artifact := range []string{
		`{"name":"picture.png","media_type":"image/png","url":"https://profile-one.com/picture.png"}`,
		`{"name":"picture.png","media_type":"image/png","data_base64":"` + text + `"}`,
		`{"name":"answer.txt","media_type":"text/plain","data_base64":"` + text + `"}`,
	} {
		output := NormalizeOutput([]byte(`{"mode":"text","text":"done","artifacts":[`+artifact+`]}`), spec, "adapter", "endpoint", time.Millisecond)
		if !strings.HasPrefix(output.Error, "images_missing:") {
			t.Fatalf("non-image artifact passed image requirement: %#v", output)
		}
	}
	image := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lu8AAAAASUVORK5CYII="
	output := NormalizeOutput([]byte(`{"mode":"text","text":"done","artifacts":[{"name":"picture.png","media_type":"image/png","data_base64":"`+image+`"}]}`), spec, "adapter", "endpoint", time.Millisecond)
	if output.Error != "" || len(output.Artifacts) != 1 {
		t.Fatalf("verified PNG was rejected: %#v", output)
	}
}

func TestRequiredMediaRejectsClaimsAndVerifiesMP4Bytes(t *testing.T) {
	spec := OutputSpec{Mode: "text", Artifacts: true, MinMedia: 1}
	for _, artifact := range []string{
		`{"name":"song.mp4","media_type":"video/mp4","url":"https://profile-two.google.com/song.mp4"}`,
		`{"name":"song.mp4","media_type":"video/mp4","data_base64":"` + base64.StdEncoding.EncodeToString([]byte("not an mp4")) + `"}`,
		`{"name":"answer.txt","media_type":"text/plain","data_base64":"` + base64.StdEncoding.EncodeToString([]byte("music ready")) + `"}`,
	} {
		output := NormalizeOutput([]byte(`{"mode":"text","text":"done","artifacts":[`+artifact+`]}`), spec, "adapter", "endpoint", time.Millisecond)
		if !strings.HasPrefix(output.Error, "media_missing:") {
			t.Fatalf("non-media artifact passed music requirement: %#v", output)
		}
	}
	media := base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 0, 0, 0, 0})
	output := NormalizeOutput([]byte(`{"mode":"text","text":"done","artifacts":[{"name":"song.mp4","media_type":"video/mp4","data_base64":"`+media+`"}]}`), spec, "adapter", "endpoint", time.Millisecond)
	if output.Error != "" || len(output.Artifacts) != 1 {
		t.Fatalf("verified MP4 was rejected: %#v", output)
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
	job.Output = OutputSpec{Mode: "text", MinMedia: 1}
	if err := validateJob(job); err == nil {
		t.Fatal("minimum media without artifact opt-in should be rejected")
	}
	job.Output.Artifacts = true
	if err := validateJob(job); err != nil {
		t.Fatalf("valid media requirement was rejected: %v", err)
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

func TestNormalizeAdapterOutputEnvelopeKeepsTrustedModel(t *testing.T) {
	raw := []byte(`{"mode":"json","json":{"language":"de"},"model":"adapter-model"}`)
	output := NormalizeOutput(raw, OutputSpec{Mode: "json", RequiredKeys: []string{"language"}}, "adapter", "fallback", time.Millisecond)
	if output.Error != "" || output.Model != "fallback" || string(output.JSON) != `{"language":"de"}` {
		t.Fatalf("unexpected adapter output: %#v", output)
	}
}

func TestNormalizeAdapterOutputSeparatesEndpointSelectionFromRequestedModel(t *testing.T) {
	raw := []byte(`{"mode":"text","text":"ok","model":"forged","selected_model":"remote-model-selected","selected_reasoning":"hoch"}`)
	output := NormalizeOutput(raw, OutputSpec{Mode: "text"}, "adapter", "adapter:remote-model-pro", time.Millisecond)
	if output.Model != "adapter:remote-model-pro" || output.SelectedModel != "remote-model-selected" || output.SelectedReasoning != "hoch" {
		t.Fatalf("requested and endpoint-reported selections were mixed: %#v", output)
	}
	local := NormalizeOutput(raw, OutputSpec{Mode: "text"}, "ollama", "qwen", time.Millisecond)
	if local.SelectedModel != "" || local.SelectedReasoning != "" {
		t.Fatalf("non-adapter output must not claim a endpoint selection: %#v", local)
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

func TestNormalizeTextOutputReportsExactAndUTF8SafeBoundaries(t *testing.T) {
	exact := NormalizeOutput([]byte(strings.Repeat("a", 256)), OutputSpec{Mode: "text", MaxBytes: 256}, "test", "model", time.Millisecond)
	if exact.Truncated || len(exact.Text) != 256 {
		t.Fatalf("exact text limit was not preserved: %#v", exact)
	}

	over := NormalizeOutput([]byte(strings.Repeat("ä", 129)), OutputSpec{Mode: "text", MaxBytes: 256}, "test", "model", time.Millisecond)
	if !over.Truncated || len(over.Text) > 256 || !utf8.ValidString(over.Text) {
		t.Fatalf("over-limit UTF-8 text was not marked and safely bounded: %#v", over)
	}

	defaultExact := NormalizeOutput([]byte(strings.Repeat("d", 64<<10)), OutputSpec{Mode: "text"}, "test", "model", time.Millisecond)
	if defaultExact.Truncated || len(defaultExact.Text) != 64<<10 {
		t.Fatalf("exact default text limit was not preserved: %d bytes, truncated=%t", len(defaultExact.Text), defaultExact.Truncated)
	}
	defaultOver := NormalizeOutput([]byte(strings.Repeat("d", (64<<10)+1)), OutputSpec{Mode: "text"}, "test", "model", time.Millisecond)
	if !defaultOver.Truncated || len(defaultOver.Text) != 64<<10 {
		t.Fatalf("default text overrun was not reported: %d bytes, truncated=%t", len(defaultOver.Text), defaultOver.Truncated)
	}

	maximumExact := NormalizeOutput([]byte(strings.Repeat("m", 1<<20)), OutputSpec{Mode: "text", MaxBytes: 1 << 20}, "test", "model", time.Millisecond)
	if maximumExact.Truncated || len(maximumExact.Text) != 1<<20 {
		t.Fatalf("exact maximum text limit was not preserved: %d bytes, truncated=%t", len(maximumExact.Text), maximumExact.Truncated)
	}
	maximumOver := NormalizeOutput([]byte(strings.Repeat("m", (1<<20)+1)), OutputSpec{Mode: "text", MaxBytes: 1 << 20}, "test", "model", time.Millisecond)
	if !maximumOver.Truncated || len(maximumOver.Text) != 1<<20 {
		t.Fatalf("maximum text overrun was not reported: %d bytes, truncated=%t", len(maximumOver.Text), maximumOver.Truncated)
	}
}

func TestValidateJobExactProtocolBoundaries(t *testing.T) {
	image := make([]byte, 8<<20)
	copy(image, []byte("\x89PNG\r\n\x1a\n"))
	job := Job{
		Prompt:         strings.Repeat("p", 20000),
		Text:           strings.Repeat("t", 200000),
		ImageBase64:    base64.StdEncoding.EncodeToString(image),
		ImageMediaType: "image/png",
		Output:         OutputSpec{Mode: "text", MaxBytes: 1 << 20, Artifacts: true, MaxArtifactBytes: 12 << 20},
	}
	if err := validateJob(job); err != nil {
		t.Fatalf("exact protocol limits were rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Job){
		"prompt": func(value *Job) { value.Prompt += "x" },
		"text":   func(value *Job) { value.Text += "x" },
		"image": func(value *Job) {
			value.ImageBase64 = base64.StdEncoding.EncodeToString(make([]byte, (8<<20)+1))
		},
		"output":    func(value *Job) { value.Output.MaxBytes++ },
		"artifacts": func(value *Job) { value.Output.MaxArtifactBytes++ },
	} {
		candidate := job
		mutate(&candidate)
		if err := validateJob(candidate); err == nil {
			t.Fatalf("%s limit accepted one byte too many", name)
		}
	}
}

func TestValidateJobRejectsDisguisedOrUnsupportedImageBytes(t *testing.T) {
	for name, job := range map[string]Job{
		"text disguised as png": {
			Prompt: "inspect", ImageBase64: base64.StdEncoding.EncodeToString([]byte("not a png")), ImageMediaType: "image/png", Output: OutputSpec{Mode: "text"},
		},
		"png declared jpeg": {
			Prompt: "inspect", ImageBase64: base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n")), ImageMediaType: "image/jpeg", Output: OutputSpec{Mode: "text"},
		},
		"svg input": {
			Prompt: "inspect", ImageBase64: base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)), ImageMediaType: "image/svg+xml", Output: OutputSpec{Mode: "text"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateJob(job); err == nil {
				t.Fatal("untrusted bytes crossed the vision decoder boundary")
			}
		})
	}
}

func TestResponseJobDoesNotEchoVisualInput(t *testing.T) {
	job := Job{ID: "job-1", Prompt: "describe", ImageBase64: "large-input", ImageMediaType: "image/png", Model: "vision-model"}
	response := responseJob(job)
	if response.ImageBase64 != "" || response.ImageMediaType != "image/png" || response.ID != "job-1" || response.Model != "vision-model" {
		t.Fatalf("visual response envelope was not compacted safely: %#v", response)
	}
	if job.ImageBase64 != "large-input" {
		t.Fatal("response compaction mutated the queued job")
	}
}

func TestCompactResponseJobDoesNotEchoAnyLargeOrSensitiveInput(t *testing.T) {
	job := Job{
		ID:                             "job-compact",
		Prompt:                         "private prompt",
		Text:                           "private text",
		Texts:                          []string{"private batch"},
		Documents:                      []vectorstore.Document{{ID: "private-doc", Text: "private document"}},
		Query:                          "private query",
		ImageBase64:                    "private-image",
		ImageMediaType:                 "image/png",
		Model:                          "vision-model",
		ContextBridgeSessionKey:        "cb:" + strings.Repeat("a", 64),
		ContextBridgeAdapterEndpointID: 42,
	}
	response := compactResponseJob(job)
	if response.Prompt != "" || response.Text != "" || len(response.Texts) != 0 || len(response.Documents) != 0 || response.Query != "" || response.ImageBase64 != "" {
		t.Fatalf("compact response retained submitted content: %#v", response)
	}
	if response.ID != job.ID || response.Model != job.Model || response.ImageMediaType != job.ImageMediaType {
		t.Fatalf("compact response lost routing metadata: %#v", response)
	}
	if response.ContextBridgeSessionKey != "" || response.ContextBridgeAdapterEndpointID != 0 {
		t.Fatalf("compact response leaked internal adapter routing metadata: %#v", response)
	}
	if job.Prompt == "" || job.ImageBase64 == "" || len(job.Documents) == 0 {
		t.Fatal("compact response mutated the original job")
	}
}

func TestNormalizeArtifactsAcceptsExactBudgetAndRejectsOneByteMore(t *testing.T) {
	spec := OutputSpec{Mode: "text", Artifacts: true, MaxArtifactBytes: 12 << 20}
	exactData := make([]byte, 12<<20)
	exact := NormalizeArtifacts([]Artifact{{Name: "exact.bin", MediaType: "application/octet-stream", DataBase64: base64.StdEncoding.EncodeToString(exactData)}}, spec)
	if len(exact) != 1 || exact[0].Size != 12<<20 || exact[0].DataBase64 == "" {
		t.Fatalf("exact artifact budget was rejected: %#v", exact)
	}
	overData := make([]byte, (12<<20)+1)
	over := NormalizeArtifacts([]Artifact{{Name: "over.bin", MediaType: "application/octet-stream", DataBase64: base64.StdEncoding.EncodeToString(overData)}}, spec)
	if len(over) != 0 {
		t.Fatalf("artifact over budget was retained: %#v", over)
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
