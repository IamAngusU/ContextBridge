package vectorstore

import (
	"context"
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
