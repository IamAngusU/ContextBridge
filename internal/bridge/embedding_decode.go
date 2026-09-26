package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	maximumEmbeddingVectors       = 256
	maximumEmbeddingDimensions    = 32768
	maximumEmbeddingResponseBytes = 64 << 20
	maximumJSONNesting            = 64
)

type embeddingUsage struct {
	PromptTokens uint64
	TotalTokens  uint64
	Present      bool
	HasPrompt    bool
	HasTotal     bool
}

func (usage embeddingUsage) Complete() bool {
	return usage.Present && usage.HasPrompt && usage.HasTotal && usage.TotalTokens == usage.PromptTokens
}

type indexedEmbedding struct {
	Vector []float32
	Index  int
}

// decodeOpenAIEmbeddingResponse parses the response structurally instead of
// unmarshalling attacker-controlled arrays into unrestricted Go slices. The
// configured provider is an external trust boundary: wire bytes, vector count,
// dimensions, indexes and nesting are independent limits.
func decodeOpenAIEmbeddingResponse(reader io.Reader) ([][]float32, embeddingUsage, error) {
	decoder, limited := boundedJSONDecoder(reader, maximumEmbeddingResponseBytes)
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return nil, embeddingUsage{}, err
	}
	var items []indexedEmbedding
	var usage embeddingUsage
	for decoder.More() {
		key, err := decodeJSONKey(decoder)
		if err != nil {
			return nil, usage, err
		}
		switch key {
		case "data":
			items, err = decodeIndexedEmbeddings(decoder)
		case "usage":
			usage, err = decodeEmbeddingUsage(decoder)
			usage.Present = true
		default:
			err = skipJSONValue(decoder, 0)
		}
		if err != nil {
			return nil, usage, err
		}
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		return nil, usage, err
	}
	if err := ensureJSONEOF(decoder, limited); err != nil {
		return nil, usage, err
	}
	if len(items) == 0 {
		return nil, usage, errors.New("embedding engine returned no vectors")
	}
	embeddings := make([][]float32, len(items))
	seen := make([]bool, len(items))
	for _, item := range items {
		if item.Index < 0 || item.Index >= len(items) {
			return nil, usage, fmt.Errorf("embedding response index %d is outside 0..%d", item.Index, len(items)-1)
		}
		if seen[item.Index] {
			return nil, usage, fmt.Errorf("embedding response contains duplicate index %d", item.Index)
		}
		seen[item.Index] = true
		embeddings[item.Index] = item.Vector
	}
	return embeddings, usage, nil
}

func decodeOllamaEmbeddingResponse(reader io.Reader) ([][]float32, uint64, error) {
	decoder, limited := boundedJSONDecoder(reader, maximumEmbeddingResponseBytes)
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return nil, 0, err
	}
	var embeddings [][]float32
	var promptTokens uint64
	for decoder.More() {
		key, err := decodeJSONKey(decoder)
		if err != nil {
			return nil, 0, err
		}
		switch key {
		case "embeddings":
			embeddings, err = decodeEmbeddingVectors(decoder)
		case "prompt_eval_count":
			err = decoder.Decode(&promptTokens)
		default:
			err = skipJSONValue(decoder, 0)
		}
		if err != nil {
			return nil, 0, err
		}
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		return nil, 0, err
	}
	if err := ensureJSONEOF(decoder, limited); err != nil {
		return nil, 0, err
	}
	if len(embeddings) == 0 {
		return nil, 0, errors.New("embedding engine returned no vectors")
	}
	return embeddings, promptTokens, nil
}

func decodeIndexedEmbeddings(decoder *json.Decoder) ([]indexedEmbedding, error) {
	if err := expectJSONDelimiter(decoder, '['); err != nil {
		return nil, err
	}
	items := make([]indexedEmbedding, 0, 8)
	for decoder.More() {
		if len(items) == maximumEmbeddingVectors {
			return nil, fmt.Errorf("embedding response exceeds %d vectors", maximumEmbeddingVectors)
		}
		item, err := decodeIndexedEmbedding(decoder)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := expectJSONDelimiter(decoder, ']'); err != nil {
		return nil, err
	}
	return items, nil
}

func decodeIndexedEmbedding(decoder *json.Decoder) (indexedEmbedding, error) {
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return indexedEmbedding{}, err
	}
	item := indexedEmbedding{Index: -1}
	hasVector, hasIndex := false, false
	for decoder.More() {
		key, err := decodeJSONKey(decoder)
		if err != nil {
			return item, err
		}
		switch key {
		case "embedding":
			if hasVector {
				return item, errors.New("embedding response item contains duplicate embedding")
			}
			item.Vector, err = decodeEmbeddingVector(decoder)
			hasVector = true
		case "index":
			if hasIndex {
				return item, errors.New("embedding response item contains duplicate index")
			}
			err = decoder.Decode(&item.Index)
			hasIndex = true
			if err == nil && (item.Index < 0 || item.Index >= maximumEmbeddingVectors) {
				return item, fmt.Errorf("embedding response index %d exceeds protocol limits", item.Index)
			}
		default:
			err = skipJSONValue(decoder, 0)
		}
		if err != nil {
			return item, err
		}
	}
	if err := expectJSONDelimiter(decoder, '}'); err != nil {
		return item, err
	}
	if !hasVector || !hasIndex {
		return item, errors.New("embedding response item requires embedding and index")
	}
	return item, nil
}

func decodeEmbeddingVectors(decoder *json.Decoder) ([][]float32, error) {
	if err := expectJSONDelimiter(decoder, '['); err != nil {
		return nil, err
	}
	vectors := make([][]float32, 0, 8)
	for decoder.More() {
		if len(vectors) == maximumEmbeddingVectors {
			return nil, fmt.Errorf("embedding response exceeds %d vectors", maximumEmbeddingVectors)
		}
		vector, err := decodeEmbeddingVector(decoder)
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, vector)
	}
	if err := expectJSONDelimiter(decoder, ']'); err != nil {
		return nil, err
	}
	return vectors, nil
}

func decodeEmbeddingVector(decoder *json.Decoder) ([]float32, error) {
	if err := expectJSONDelimiter(decoder, '['); err != nil {
		return nil, err
	}
	vector := make([]float32, 0, 256)
	for decoder.More() {
		if len(vector) == maximumEmbeddingDimensions {
			return nil, fmt.Errorf("embedding vector exceeds %d dimensions", maximumEmbeddingDimensions)
		}
		var value float32
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode embedding scalar: %w", err)
		}
		vector = append(vector, value)
	}
	if err := expectJSONDelimiter(decoder, ']'); err != nil {
		return nil, err
	}
	return vector, nil
}

func decodeEmbeddingUsage(decoder *json.Decoder) (embeddingUsage, error) {
	if err := expectJSONDelimiter(decoder, '{'); err != nil {
		return embeddingUsage{}, err
	}
	var usage embeddingUsage
	for decoder.More() {
		key, err := decodeJSONKey(decoder)
		if err != nil {
			return usage, err
		}
		switch key {
		case "prompt_tokens":
			err = decoder.Decode(&usage.PromptTokens)
			usage.HasPrompt = err == nil
		case "total_tokens":
			err = decoder.Decode(&usage.TotalTokens)
			usage.HasTotal = err == nil
		default:
			err = skipJSONValue(decoder, 0)
		}
		if err != nil {
			return usage, err
		}
	}
	return usage, expectJSONDelimiter(decoder, '}')
}

func decodeJSONKey(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", errors.New("JSON object key is not a string")
	}
	return key, nil
}

func expectJSONDelimiter(decoder *json.Decoder, wanted json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != wanted {
		return fmt.Errorf("expected JSON delimiter %q", wanted)
	}
	return nil
}

func skipJSONValue(decoder *json.Decoder, depth int) error {
	if depth >= maximumJSONNesting {
		return errors.New("provider JSON exceeds nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			if _, err := decodeJSONKey(decoder); err != nil {
				return err
			}
			if err := skipJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		return expectJSONDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := skipJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		return expectJSONDelimiter(decoder, ']')
	default:
		return errors.New("unexpected closing JSON delimiter")
	}
}

func boundedJSONDecoder(reader io.Reader, maximum int64) (*json.Decoder, *io.LimitedReader) {
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	return decoder, limited
}

func ensureJSONEOF(decoder *json.Decoder, limited *io.LimitedReader) error {
	if _, err := decoder.Token(); err == io.EOF {
		if limited.N == 0 {
			return fmt.Errorf("embedding response exceeds %d MiB", maximumEmbeddingResponseBytes>>20)
		}
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("embedding response contains multiple JSON values")
}
