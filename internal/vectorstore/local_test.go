package vectorstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStoreSeparatesTenantsAndPersists(t *testing.T) {
	directory := t.TempDir()
	store, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), "tenant-a", []Document{{ID: "a", Text: "alpha"}}, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), "tenant-b", []Document{{ID: "b", Text: "beta"}}, [][]float32{{0, 1}}); err != nil {
		t.Fatal(err)
	}
	matches, err := store.Search(context.Background(), "tenant-a", []float32{1, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != "a" || matches[0].Score < 0.99 {
		t.Fatalf("unexpected matches: %#v", matches)
	}
	reloaded, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Count() != 2 {
		t.Fatalf("expected two persisted documents, got %d", reloaded.Count())
	}
}

func TestLocalStoreRejectsOversizedPersistentState(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "vectors.json"), make([]byte, maximumLocalVectorStoreBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocal(directory, 10); err == nil {
		t.Fatal("oversized vector store was accepted")
	}
}

func TestLocalUpsertRejectsWholeInvalidBatchWithoutMutation(t *testing.T) {
	directory := t.TempDir()
	store, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), "tenant", []Document{{ID: "existing", Text: "before"}}, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	err = store.Upsert(context.Background(), "tenant", []Document{{ID: "new", Text: "must not appear"}, {ID: "", Text: "invalid"}}, [][]float32{{0, 1}, {1, 1}})
	if err == nil {
		t.Fatal("invalid batch was accepted")
	}
	if store.Count() != 1 {
		t.Fatalf("invalid batch changed in-memory count to %d", store.Count())
	}
	reloaded, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Count() != 1 {
		t.Fatalf("invalid batch changed durable count to %d", reloaded.Count())
	}
}

func TestLocalUpsertCapacityCountsUniqueFinalIDs(t *testing.T) {
	store, err := NewLocal(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), "tenant", []Document{{ID: "a", Text: "one"}}, [][]float32{{1}}); err != nil {
		t.Fatal(err)
	}
	// Repeated IDs are last-write-wins and consume one final slot.
	if err := store.Upsert(context.Background(), "tenant", []Document{{ID: "b", Text: "first"}, {ID: "b", Text: "last"}}, [][]float32{{2}, {3}}); err != nil {
		t.Fatal(err)
	}
	if store.Count() != 2 {
		t.Fatalf("duplicate batch IDs consumed extra capacity: %d", store.Count())
	}
	if err := store.Upsert(context.Background(), "tenant", []Document{{ID: "a", Text: "updated"}, {ID: "c", Text: "too many"}}, [][]float32{{4}, {5}}); err == nil {
		t.Fatal("capacity overflow was accepted")
	}
	if store.Count() != 2 {
		t.Fatalf("rejected capacity batch changed count to %d", store.Count())
	}
}

func TestLocalUpsertCancellationAndPersistFailureAreAtomic(t *testing.T) {
	directory := t.TempDir()
	store, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), "tenant", []Document{{ID: "a", Text: "before"}}, [][]float32{{1}}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Upsert(cancelled, "tenant", []Document{{ID: "b"}}, [][]float32{{2}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upsert error = %v", err)
	}
	originalPath := store.path
	store.path = directory // rename onto a directory must fail on every supported OS.
	if err := store.Upsert(context.Background(), "tenant", []Document{{ID: "b"}}, [][]float32{{2}}); err == nil {
		t.Fatal("forced persistence failure was accepted")
	}
	store.path = originalPath
	if store.Count() != 1 {
		t.Fatalf("failed persistence changed in-memory count to %d", store.Count())
	}
	reloaded, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Count() != 1 {
		t.Fatalf("failed persistence changed durable count to %d", reloaded.Count())
	}
}

func TestLocalSearchKeepsOnlyBoundedTopK(t *testing.T) {
	store := &Local{max: 10000, data: map[string]map[string]record{"tenant": {}}}
	for index := 0; index < 5000; index++ {
		id := fmt.Sprintf("doc-%04d", index)
		store.data["tenant"][id] = record{Document: Document{ID: id}, Vector: []float32{float32(index), 1}}
	}
	matches, err := store.Search(context.Background(), "tenant", []float32{1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("top-k result count = %d", len(matches))
	}
	for index := 1; index < len(matches); index++ {
		if matchBetter(matches[index], matches[index-1]) {
			t.Fatalf("matches are not in best-first order: %#v", matches)
		}
	}
}

func TestBoundedStoreWriterStopsBeforeOversizedSerialization(t *testing.T) {
	var destination bytes.Buffer
	writer := &boundedStoreWriter{writer: &destination, remaining: 8}
	written, err := writer.Write([]byte("123456789"))
	if !errors.Is(err, errLocalVectorStoreTooLarge) || written != 8 || destination.String() != "12345678" {
		t.Fatalf("bounded write = %d, %q, %v", written, destination.String(), err)
	}
	if written, err = writer.Write([]byte("x")); !errors.Is(err, errLocalVectorStoreTooLarge) || written != 0 {
		t.Fatalf("write after limit = %d, %v", written, err)
	}
}

func TestDecodeLocalVectorStoreRejectsMultipleValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.json")
	if err := os.WriteFile(path, []byte(`{} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	var target map[string]map[string]record
	if err := decodeLocalVectorStore(path, &target); err == nil {
		t.Fatal("multiple JSON values accepted")
	}
}
