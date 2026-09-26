package bridge

import (
	"fmt"
	"strings"
	"testing"
)

func TestDecodeOpenAIEmbeddingResponse(t *testing.T) {
	embeddings, usage, err := decodeOpenAIEmbeddingResponse(strings.NewReader(`{"data":[{"embedding":[1,2],"index":1},{"embedding":[3,4],"index":0}],"usage":{"prompt_tokens":5,"total_tokens":6}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) != 2 || embeddings[0][0] != 3 || embeddings[1][0] != 1 || usage.PromptTokens != 5 || usage.TotalTokens != 6 {
		t.Fatalf("decoded response = %#v, %#v", embeddings, usage)
	}
}

func TestDecodeEmbeddingResponseRejectsStructuralOverflow(t *testing.T) {
	tests := map[string]string{
		"257-vectors":      `{"embeddings":[` + strings.Repeat(`[1],`, maximumEmbeddingVectors) + `[1]]}`,
		"32769-dimensions": `{"embeddings":[[` + strings.Repeat(`1,`, maximumEmbeddingDimensions) + `1]]}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeOllamaEmbeddingResponse(strings.NewReader(payload)); err == nil {
				t.Fatal("structural overflow accepted")
			}
		})
	}
}

func TestDecodeOpenAIEmbeddingResponseRejectsInvalidIndexes(t *testing.T) {
	tests := map[string]string{
		"duplicate":      `{"data":[{"embedding":[1],"index":0},{"embedding":[2],"index":0}]}`,
		"out-of-range":   `{"data":[{"embedding":[1],"index":1}]}`,
		"protocol-range": fmt.Sprintf(`{"data":[{"embedding":[1],"index":%d}]}`, maximumEmbeddingVectors),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeOpenAIEmbeddingResponse(strings.NewReader(payload)); err == nil {
				t.Fatal("invalid indexes accepted")
			}
		})
	}
}

func TestDecodeEmbeddingResponseSkipsUnknownShapeWithoutMaterializingIt(t *testing.T) {
	payload := `{"ignored":[` + strings.Repeat(`[],`, 10000) + `[]],"embeddings":[[1,2,3]],"prompt_eval_count":7}`
	embeddings, tokens, err := decodeOllamaEmbeddingResponse(strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != 3 || tokens != 7 {
		t.Fatalf("decoded response = %#v, tokens %d", embeddings, tokens)
	}
}

func TestBoundedJSONDecoderRejectsWireOverflow(t *testing.T) {
	decoder, limited := boundedJSONDecoder(strings.NewReader(`{} `), 2)
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		t.Fatal(err)
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		t.Fatal(err)
	}
	if err := ensureJSONEOF(decoder, limited); err == nil {
		t.Fatal("response beyond byte limit accepted")
	}
	decoder, limited = boundedJSONDecoder(strings.NewReader(`{}`), 2)
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		t.Fatal(err)
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		t.Fatal(err)
	}
	if err := ensureJSONEOF(decoder, limited); err != nil {
		t.Fatalf("exact byte limit rejected: %v", err)
	}
}

func FuzzBoundedEmbeddingDecoders(f *testing.F) {
	for _, seed := range []string{
		`{"embeddings":[[1,2]],"prompt_eval_count":2}`,
		`{"data":[{"embedding":[1,2],"index":0}],"usage":{"total_tokens":2}}`,
		`{"data":[{"embedding":[],"index":-1}]}`,
		`[]`,
		`{"unknown":{"nested":[true,null,"x"]}}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		if embeddings, _, err := decodeOllamaEmbeddingResponse(strings.NewReader(raw)); err == nil {
			assertBoundedEmbeddings(t, embeddings)
		}
		if embeddings, _, err := decodeOpenAIEmbeddingResponse(strings.NewReader(raw)); err == nil {
			assertBoundedEmbeddings(t, embeddings)
		}
	})
}

func assertBoundedEmbeddings(t *testing.T, embeddings [][]float32) {
	t.Helper()
	if len(embeddings) == 0 || len(embeddings) > maximumEmbeddingVectors {
		t.Fatalf("accepted embedding count %d", len(embeddings))
	}
	for _, vector := range embeddings {
		if len(vector) > maximumEmbeddingDimensions {
			t.Fatalf("accepted dimensions %d", len(vector))
		}
	}
}
