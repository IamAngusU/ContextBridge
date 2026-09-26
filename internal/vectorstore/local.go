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
	if err := decodeLocalVectorStore(store.path, &store.data); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return store, nil
}

func decodeLocalVectorStore(path string, target interface{}) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumLocalVectorStoreBytes {
		return fmt.Errorf("local vector store must be a regular file no larger than %d MiB", maximumLocalVectorStoreBytes>>20)
	}
	if info.Size() == 0 {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumLocalVectorStoreBytes+1))
	if err := decoder.Decode(target); err != nil {
		return errors.New("local vector store is corrupted")
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("local vector store is corrupted")
	}
	return nil
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
	matches := make([]Match, 0, limit)
	for _, item := range s.data[tenant] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(item.Vector) != len(vector) {
			continue
		}
		candidate := Match{ID: item.Document.ID, Text: item.Document.Text, Metadata: item.Document.Metadata, Score: cosine(item.Vector, vector)}
		if len(matches) < limit {
			matches = append(matches, candidate)
			matchHeapUp(matches, len(matches)-1)
		} else if matchBetter(candidate, matches[0]) {
			matches[0] = candidate
			matchHeapDown(matches, 0)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matchBetter(matches[i], matches[j]) })
	return matches, nil
}

func matchBetter(left, right Match) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	return left.ID < right.ID
}

// The heap root is the worst retained match. This keeps search working memory
// bounded by top_k instead of by the complete tenant store.
func matchWorse(left, right Match) bool { return matchBetter(right, left) }

func matchHeapUp(items []Match, index int) {
	for index > 0 {
		parent := (index - 1) / 2
		if !matchWorse(items[index], items[parent]) {
			return
		}
		items[index], items[parent] = items[parent], items[index]
		index = parent
	}
}

func matchHeapDown(items []Match, index int) {
	for {
		left := index*2 + 1
		if left >= len(items) {
			return
		}
		worst := left
		right := left + 1
		if right < len(items) && matchWorse(items[right], items[left]) {
			worst = right
		}
		if !matchWorse(items[worst], items[index]) {
			return
		}
		items[index], items[worst] = items[worst], items[index]
		index = worst
	}
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
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".vectors-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	bounded := &boundedStoreWriter{writer: temporary, remaining: maximumLocalVectorStoreBytes}
	if err := json.NewEncoder(bounded).Encode(data); err != nil {
		if errors.Is(err, errLocalVectorStoreTooLarge) {
			return fmt.Errorf("local vector store exceeds %d MiB", maximumLocalVectorStoreBytes>>20)
		}
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return err
	}
	keep = true
	return nil
}

var errLocalVectorStoreTooLarge = errors.New("local vector store byte limit exceeded")

type boundedStoreWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *boundedStoreWriter) Write(content []byte) (int, error) {
	if int64(len(content)) > writer.remaining {
		if writer.remaining <= 0 {
			return 0, errLocalVectorStoreTooLarge
		}
		allowed := int(writer.remaining)
		written, err := writer.writer.Write(content[:allowed])
		writer.remaining -= int64(written)
		if err != nil {
			return written, err
		}
		return written, errLocalVectorStoreTooLarge
	}
	written, err := writer.writer.Write(content)
	writer.remaining -= int64(written)
	return written, err
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
