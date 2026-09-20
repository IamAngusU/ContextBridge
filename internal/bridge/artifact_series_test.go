package bridge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestImageSeriesUsesOneAggregateBudgetAndCompletesAtomically(t *testing.T) {
	const aggregateLimit = 1024
	spec := OutputSpec{
		Mode:             "text",
		Artifacts:        true,
		MaxArtifactBytes: aggregateLimit,
		MinImages:        3,
	}

	exactSizes := []int{341, 341, 342}
	exact := NormalizeOutput(imageSeriesEnvelope(t, exactSizes), spec, "adapter", "test-endpoint", time.Millisecond)
	if exact.Error != "" || exact.Truncated || len(exact.Artifacts) != len(exactSizes) {
		t.Fatalf("exact aggregate image series was not returned intact: %#v", exact)
	}
	total := 0
	for index, artifact := range exact.Artifacts {
		if artifact.DataBase64 == "" || artifact.Size != exactSizes[index] {
			t.Fatalf("image %d was incomplete at the exact aggregate boundary: %#v", index+1, artifact)
		}
		total += artifact.Size
	}
	if total != aggregateLimit {
		t.Fatalf("decoded image series total %d bytes, want %d", total, aggregateLimit)
	}

	// The third image makes the series one byte too large. Because all three
	// were explicitly required, the result must fail as a whole: no answer
	// prefix and no partial artifact list may escape as a successful result.
	over := NormalizeOutput(imageSeriesEnvelope(t, []int{341, 341, 343}), spec, "adapter", "test-endpoint", time.Millisecond)
	if !strings.HasPrefix(over.Error, "images_missing: expected 3 image(s), received 2") {
		t.Fatalf("one-byte aggregate overrun did not fail the required series: %#v", over)
	}
	if over.Text != "" || over.Truncated || len(over.Artifacts) != 0 {
		t.Fatalf("failed required series leaked a partial result: %#v", over)
	}
}

func TestImageSeriesCountCapIsDeterministic(t *testing.T) {
	spec := OutputSpec{Mode: "text", Artifacts: true, MaxArtifactBytes: 4096, MinImages: 12}
	raw := imageSeriesEnvelope(t, make([]int, 13))
	output := NormalizeOutput(raw, spec, "adapter", "test-endpoint", time.Millisecond)
	if output.Error != "" || len(output.Artifacts) != 12 {
		t.Fatalf("thirteen candidates should deterministically retain the first twelve: %#v", output)
	}
	for index, artifact := range output.Artifacts {
		want := fmt.Sprintf("image-%02d.png", index+1)
		if artifact.Name != want {
			t.Fatalf("artifact %d = %q, want %q", index, artifact.Name, want)
		}
	}

	job := Job{Prompt: "series", Output: OutputSpec{Mode: "text", Artifacts: true, MinImages: 12}}
	if err := validateJob(job); err != nil {
		t.Fatalf("twelve required images were rejected: %v", err)
	}
	job.Output.MinImages = 13
	if err := validateJob(job); err == nil {
		t.Fatal("thirteen required images exceeded the protocol count cap")
	}
}

func TestTypedArtifactMinimumsRejectImpossibleCombinedCount(t *testing.T) {
	job := Job{Prompt: "mixed series", Output: OutputSpec{
		Mode: "text", Artifacts: true, MinArtifacts: 12, MinImages: 6, MinMedia: 6,
	}}
	if err := validateJob(job); err != nil {
		t.Fatalf("six images plus six media files should fit the shared count cap: %v", err)
	}

	// MinArtifacts overlaps the typed requirements, but images and media do
	// not overlap each other. Seven of each would require fourteen slots.
	job.Output.MinImages = 7
	job.Output.MinMedia = 7
	if err := validateJob(job); err == nil || !strings.Contains(err.Error(), "combined output.min_images") {
		t.Fatalf("impossible mixed artifact requirements were not rejected: %v", err)
	}
}

func imageSeriesEnvelope(t *testing.T, sizes []int) []byte {
	t.Helper()
	type artifactEnvelope struct {
		Name       string `json:"name"`
		MediaType  string `json:"media_type"`
		DataBase64 string `json:"data_base64"`
	}
	envelope := struct {
		Mode      string             `json:"mode"`
		Text      string             `json:"text"`
		Artifacts []artifactEnvelope `json:"artifacts"`
	}{Mode: "text", Text: "series complete", Artifacts: make([]artifactEnvelope, 0, len(sizes))}
	for index, size := range sizes {
		if size == 0 {
			size = 16
		}
		data := makePNGFixture(size, byte(index+1))
		envelope.Artifacts = append(envelope.Artifacts, artifactEnvelope{
			Name:       fmt.Sprintf("image-%02d.png", index+1),
			MediaType:  "image/png",
			DataBase64: base64.StdEncoding.EncodeToString(data),
		})
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func makePNGFixture(size int, fill byte) []byte {
	if size < 8 {
		size = 8
	}
	data := make([]byte, size)
	copy(data, []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'})
	for index := 8; index < len(data); index++ {
		data[index] = fill
	}
	return data
}
