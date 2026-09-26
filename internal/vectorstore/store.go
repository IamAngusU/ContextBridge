package vectorstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	EmbeddingSpaceV1        = "contextbridge.embedding-space.v1"
	MaximumVectorDimensions = 32768
)

var (
	ErrEmbeddingSpaceMismatch        = errors.New("embedding_space_mismatch: reindex the complete tenant before changing embedding space")
	ErrEmbeddingSpaceReindexRequired = errors.New("embedding_space_reindex_required: legacy vectors have no embedding-space identity; reingest the complete tenant")
)

// EmbeddingSpace is the minimum persisted identity required to decide whether
// two vectors may be compared. Equal dimensions alone are not compatibility
// evidence: model content, pooling and query/passage preprocessing also define
// the space. Fingerprint is derived from every other field and is never an
// operator-supplied trust shortcut.
type EmbeddingSpace struct {
	Schema                     string `json:"schema"`
	Fingerprint                string `json:"fingerprint"`
	Provider                   string `json:"provider"`
	Runtime                    string `json:"runtime,omitempty"`
	Model                      string `json:"model"`
	Revision                   string `json:"revision,omitempty"`
	OperatorRevision           string `json:"operator_revision,omitempty"`
	ModelSHA256                string `json:"model_sha256,omitempty"`
	Dimensions                 int    `json:"dimensions"`
	Normalization              string `json:"normalization"`
	Similarity                 string `json:"similarity"`
	QueryPassageStrategySHA256 string `json:"query_passage_strategy_sha256"`
	Evidence                   string `json:"evidence"`
}

type embeddingSpaceIdentity struct {
	Schema                     string `json:"schema"`
	Provider                   string `json:"provider"`
	Runtime                    string `json:"runtime,omitempty"`
	Model                      string `json:"model"`
	Revision                   string `json:"revision,omitempty"`
	OperatorRevision           string `json:"operator_revision,omitempty"`
	ModelSHA256                string `json:"model_sha256,omitempty"`
	Dimensions                 int    `json:"dimensions"`
	Normalization              string `json:"normalization"`
	Similarity                 string `json:"similarity"`
	QueryPassageStrategySHA256 string `json:"query_passage_strategy_sha256"`
	Evidence                   string `json:"evidence"`
}

// NormalizeEmbeddingSpace validates, canonicalizes and fingerprints one
// descriptor. Mutable aliases remain explicitly labelled as such; this
// function does not promote an alias to immutable evidence.
func NormalizeEmbeddingSpace(space EmbeddingSpace) (EmbeddingSpace, error) {
	space.Schema = EmbeddingSpaceV1
	space.Provider = strings.ToLower(strings.TrimSpace(space.Provider))
	space.Runtime = strings.ToLower(strings.TrimSpace(space.Runtime))
	space.Model = strings.TrimSpace(space.Model)
	space.Revision = strings.TrimSpace(space.Revision)
	space.OperatorRevision = strings.TrimSpace(space.OperatorRevision)
	space.ModelSHA256 = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(space.ModelSHA256), "sha256:"))
	space.Normalization = strings.ToLower(strings.TrimSpace(space.Normalization))
	space.Similarity = strings.ToLower(strings.TrimSpace(space.Similarity))
	space.QueryPassageStrategySHA256 = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(space.QueryPassageStrategySHA256), "sha256:"))
	space.Evidence = strings.ToLower(strings.TrimSpace(space.Evidence))
	if !validEmbeddingIdentity(space.Provider, 120, false) || !validEmbeddingIdentity(space.Model, 200, false) || !validEmbeddingIdentity(space.Runtime, 80, true) || !validEmbeddingIdentity(space.Revision, 200, true) || !validEmbeddingIdentity(space.OperatorRevision, 200, true) {
		return EmbeddingSpace{}, errors.New("embedding space requires bounded provider and model identity")
	}
	if space.Dimensions < 1 || space.Dimensions > MaximumVectorDimensions {
		return EmbeddingSpace{}, fmt.Errorf("embedding space dimensions must be between 1 and %d", MaximumVectorDimensions)
	}
	if space.Normalization == "" || len(space.Normalization) > 80 || space.Similarity != "cosine" {
		return EmbeddingSpace{}, errors.New("embedding space requires bounded normalization and cosine similarity")
	}
	if !validSHA256(space.QueryPassageStrategySHA256) || (space.ModelSHA256 != "" && !validSHA256(space.ModelSHA256)) {
		return EmbeddingSpace{}, errors.New("embedding space digests must contain 64 hexadecimal characters")
	}
	if space.Evidence != "immutable_revision" && space.Evidence != "operator_revision" && space.Evidence != "mutable_alias" {
		return EmbeddingSpace{}, errors.New("embedding space evidence must be immutable_revision, operator_revision, or mutable_alias")
	}
	if space.Evidence == "operator_revision" && space.OperatorRevision == "" {
		return EmbeddingSpace{}, errors.New("operator_revision embedding evidence requires an explicit revision")
	}
	if space.Evidence == "immutable_revision" && space.Revision == "" && space.ModelSHA256 == "" {
		return EmbeddingSpace{}, errors.New("immutable_revision embedding evidence requires a revision or model digest")
	}
	identity := embeddingSpaceIdentity{
		Schema: space.Schema, Provider: space.Provider, Runtime: space.Runtime, Model: space.Model,
		Revision: space.Revision, OperatorRevision: space.OperatorRevision, ModelSHA256: space.ModelSHA256, Dimensions: space.Dimensions,
		Normalization: space.Normalization, Similarity: space.Similarity,
		QueryPassageStrategySHA256: space.QueryPassageStrategySHA256, Evidence: space.Evidence,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return EmbeddingSpace{}, err
	}
	digest := sha256.Sum256(encoded)
	space.Fingerprint = hex.EncodeToString(digest[:])
	return space, nil
}

func (space EmbeddingSpace) Valid() bool {
	if space.Schema != EmbeddingSpaceV1 || space.Fingerprint == "" {
		return false
	}
	normalized, err := NormalizeEmbeddingSpace(space)
	return err == nil && strings.EqualFold(normalized.Fingerprint, strings.TrimSpace(space.Fingerprint))
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validEmbeddingIdentity(value string, maximum int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
	}) < 0
}

type Document struct {
	ID       string                 `json:"id"`
	Text     string                 `json:"text"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type Match struct {
	ID       string                 `json:"id"`
	Text     string                 `json:"text"`
	Score    float32                `json:"score"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type Store interface {
	Upsert(ctx context.Context, tenant string, space EmbeddingSpace, documents []Document, vectors [][]float32) error
	Search(ctx context.Context, tenant string, space EmbeddingSpace, vector []float32, limit int) ([]Match, error)
	Count() int
}
