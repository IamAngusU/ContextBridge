package vectorstore

import (
	"context"
	"errors"
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
