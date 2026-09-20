package vectorstore

import "context"

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
	Upsert(ctx context.Context, tenant string, documents []Document, vectors [][]float32) error
	Search(ctx context.Context, tenant string, vector []float32, limit int) ([]Match, error)
	Count() int
}
