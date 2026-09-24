package vectorstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const maximumLocalVectorStoreBytes int64 = 256 << 20

type Local struct {
	path string
	max  int
	mu   sync.RWMutex
	data map[string]map[string]record
}

type record struct {
	Document Document  `json:"document"`
	Vector   []float32 `json:"vector"`
}

func NewLocal(directory string, max int) (*Local, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	store := &Local{path: filepath.Join(directory, "vectors.json"), max: max, data: map[string]map[string]record{}}
	raw, readErr := readLocalVectorStore(store.path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &store.data); err != nil {
			return nil, errors.New("local vector store is corrupted")
		}
	}
	return store, nil
}

func readLocalVectorStore(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumLocalVectorStoreBytes {
		return nil, fmt.Errorf("local vector store must be a regular file no larger than %d MiB", maximumLocalVectorStoreBytes>>20)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumLocalVectorStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximumLocalVectorStoreBytes {
		return nil, fmt.Errorf("local vector store exceeds %d MiB", maximumLocalVectorStoreBytes>>20)
	}
	return raw, nil
}

func (s *Local) Upsert(ctx context.Context, tenant string, documents []Document, vectors [][]float32) error {
	if len(documents) != len(vectors) {
		return errors.New("document and vector counts differ")
	}
	if tenant == "" {
		tenant = "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Validate the complete request before constructing a candidate. A rejected
	// later document must never leave earlier documents visible in memory.
	for index, document := range documents {
		if err := ctx.Err(); err != nil {
			return err
		}
		if document.ID == "" || len(vectors[index]) == 0 {
			return errors.New("documents require IDs and vectors")
		}
	}
	candidate := make(map[string]map[string]record, len(s.data)+1)
	for name, records := range s.data {
		candidate[name] = records
	}
	tenantRecords := make(map[string]record, len(s.data[tenant])+len(documents))
	for id, item := range s.data[tenant] {
		tenantRecords[id] = item
	}
	candidate[tenant] = tenantRecords
	count := s.countLocked()
	for index, document := range documents {
		if _, exists := tenantRecords[document.ID]; !exists {
			count++
		}
		if count > s.max {
			return errors.New("local vector store reached max_documents")
		}
		tenantRecords[document.ID] = record{Document: document, Vector: append([]float32(nil), vectors[index]...)}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.persistDataLocked(candidate); err != nil {
		return err
	}
	s.data = candidate
	return nil
}

func (s *Local) Search(ctx context.Context, tenant string, vector []float32, limit int) ([]Match, error) {
	if tenant == "" {
		tenant = "default"
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	matches := make([]Match, 0, len(s.data[tenant]))
	for _, item := range s.data[tenant] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(item.Vector) != len(vector) {
			continue
		}
		matches = append(matches, Match{ID: item.Document.ID, Text: item.Document.Text, Metadata: item.Document.Metadata, Score: cosine(item.Vector, vector)})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func (s *Local) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.countLocked()
}

func (s *Local) countLocked() int {
	total := 0
	for _, tenant := range s.data {
		total += len(tenant)
	}
	return total
}

func (s *Local) persistLocked() error {
	return s.persistDataLocked(s.data)
}

func (s *Local) persistDataLocked(data map[string]map[string]record) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if int64(len(raw)) > maximumLocalVectorStoreBytes {
		return fmt.Errorf("local vector store exceeds %d MiB", maximumLocalVectorStoreBytes>>20)
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, s.path)
}

func cosine(left, right []float32) float32 {
	var dot, leftNorm, rightNorm float64
	for index := range left {
		a, b := float64(left[index]), float64(right[index])
		dot += a * b
		leftNorm += a * a
		rightNorm += b * b
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm)))
}
