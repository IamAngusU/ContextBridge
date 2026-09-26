package vectorstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func testEmbeddingSpace(t testing.TB, dimensions int, model string) EmbeddingSpace {
	t.Helper()
	space, err := NormalizeEmbeddingSpace(EmbeddingSpace{
		Provider: "test", Runtime: "test", Model: model, Dimensions: dimensions,
		Normalization: "provider_unspecified", Similarity: "cosine",
		QueryPassageStrategySHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Evidence:                   "mutable_alias",
	})
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func TestLocalStoreSeparatesTenantsAndPersists(t *testing.T) {
	directory := t.TempDir()
	store, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	space := testEmbeddingSpace(t, 2, "embed-a")
	if err := store.Upsert(context.Background(), "tenant-a", space, []Document{{ID: "a", Text: "alpha"}}, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), "tenant-b", space, []Document{{ID: "b", Text: "beta"}}, [][]float32{{0, 1}}); err != nil {
		t.Fatal(err)
	}
	matches, err := store.Search(context.Background(), "tenant-a", space, []float32{1, 0}, 5)
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
	space := testEmbeddingSpace(t, 2, "embed-a")
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "existing", Text: "before"}}, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	err = store.Upsert(context.Background(), "tenant", space, []Document{{ID: "new", Text: "must not appear"}, {ID: "", Text: "invalid"}}, [][]float32{{0, 1}, {1, 1}})
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
	space := testEmbeddingSpace(t, 1, "embed-a")
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "a", Text: "one"}}, [][]float32{{1}}); err != nil {
		t.Fatal(err)
	}
	// Repeated IDs are last-write-wins and consume one final slot.
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "b", Text: "first"}, {ID: "b", Text: "last"}}, [][]float32{{2}, {3}}); err != nil {
		t.Fatal(err)
	}
	if store.Count() != 2 {
		t.Fatalf("duplicate batch IDs consumed extra capacity: %d", store.Count())
	}
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "a", Text: "updated"}, {ID: "c", Text: "too many"}}, [][]float32{{4}, {5}}); err == nil {
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
	space := testEmbeddingSpace(t, 1, "embed-a")
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "a", Text: "before"}}, [][]float32{{1}}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Upsert(cancelled, "tenant", space, []Document{{ID: "b"}}, [][]float32{{2}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upsert error = %v", err)
	}
	originalPath := store.path
	store.path = directory // rename onto a directory must fail on every supported OS.
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "b"}}, [][]float32{{2}}); err == nil {
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
	space := testEmbeddingSpace(t, 2, "embed-a")
	for index := 0; index < 5000; index++ {
		id := fmt.Sprintf("doc-%04d", index)
		store.data["tenant"][id] = record{Document: Document{ID: id}, Vector: []float32{float32(index), 1}, EmbeddingSpace: space}
	}
	matches, err := store.Search(context.Background(), "tenant", space, []float32{1, 0}, 3)
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

func TestLocalStoreRejectsCrossSpaceSearchAndPartialMigration(t *testing.T) {
	store, err := NewLocal(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	spaceA := testEmbeddingSpace(t, 2, "embed-a")
	spaceB := testEmbeddingSpace(t, 2, "embed-b")
	if err := store.Upsert(context.Background(), "tenant", spaceA, []Document{{ID: "a"}, {ID: "b"}}, [][]float32{{1, 0}, {0, 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Search(context.Background(), "tenant", spaceB, []float32{1, 0}, 1); !errors.Is(err, ErrEmbeddingSpaceMismatch) {
		t.Fatalf("cross-space search error = %v", err)
	}
	if err := store.Upsert(context.Background(), "tenant", spaceB, []Document{{ID: "a"}}, [][]float32{{1, 0}}); !errors.Is(err, ErrEmbeddingSpaceMismatch) {
		t.Fatalf("partial cross-space migration error = %v", err)
	}
	if err := store.Upsert(context.Background(), "tenant", spaceB, []Document{{ID: "a"}, {ID: "b"}}, [][]float32{{1, 0}, {0, 1}}); err != nil {
		t.Fatalf("complete reindex failed: %v", err)
	}
	if _, err := store.Search(context.Background(), "tenant", spaceB, []float32{1, 0}, 1); err != nil {
		t.Fatalf("reindexed search failed: %v", err)
	}
}

func TestLocalStoreRejectsInvalidVectorValuesWithoutMutation(t *testing.T) {
	store, err := NewLocal(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	space := testEmbeddingSpace(t, 2, "embed-a")
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "valid"}}, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name   string
		vector []float32
	}{
		{name: "short", vector: []float32{1}},
		{name: "long", vector: []float32{1, 0, 0}},
		{name: "nan", vector: []float32{1, float32(math.NaN())}},
		{name: "positive-infinity", vector: []float32{1, float32(math.Inf(1))}},
		{name: "negative-infinity", vector: []float32{1, float32(math.Inf(-1))}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "invalid"}}, [][]float32{test.vector}); err == nil {
				t.Fatal("invalid vector was accepted")
			}
			if _, err := store.Search(context.Background(), "tenant", space, test.vector, 1); err == nil {
				t.Fatal("invalid query vector was accepted")
			}
			if store.Count() != 1 {
				t.Fatalf("invalid vector changed store count to %d", store.Count())
			}
		})
	}
}

func TestLocalStoreRequiresCompleteReindexForLegacyVectors(t *testing.T) {
	directory := t.TempDir()
	legacy := `{"tenant":{"a":{"document":{"id":"a","text":"legacy"},"vector":[1,0]},"b":{"document":{"id":"b","text":"legacy"},"vector":[0,1]}}}`
	if err := os.WriteFile(filepath.Join(directory, "vectors.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocal(directory, 10)
	if err != nil {
		t.Fatal(err)
	}
	space := testEmbeddingSpace(t, 2, "embed-a")
	if _, err := store.Search(context.Background(), "tenant", space, []float32{1, 0}, 1); !errors.Is(err, ErrEmbeddingSpaceReindexRequired) {
		t.Fatalf("legacy search error = %v", err)
	}
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "a"}}, [][]float32{{1, 0}}); !errors.Is(err, ErrEmbeddingSpaceReindexRequired) {
		t.Fatalf("partial legacy migration error = %v", err)
	}
	if err := store.Upsert(context.Background(), "tenant", space, []Document{{ID: "a"}, {ID: "b"}}, [][]float32{{1, 0}, {0, 1}}); err != nil {
		t.Fatalf("complete legacy reindex failed: %v", err)
	}
}

func TestEmbeddingSpaceFingerprintRejectsTampering(t *testing.T) {
	space := testEmbeddingSpace(t, 2, "embed-a")
	space.Model = "embed-b"
	if space.Valid() {
		t.Fatal("tampered embedding-space descriptor remained valid")
	}
	store := &Local{max: 10, data: map[string]map[string]record{"tenant": {"a": {Document: Document{ID: "a"}, Vector: []float32{1, 0}, EmbeddingSpace: space}}}}
	querySpace := testEmbeddingSpace(t, 2, "embed-a")
	if _, err := store.Search(context.Background(), "tenant", querySpace, []float32{1, 0}, 1); !errors.Is(err, ErrEmbeddingSpaceReindexRequired) {
		t.Fatalf("tampered descriptor error = %v", err)
	}
	querySpace.Schema = "attacker-controlled"
	if querySpace.Valid() {
		t.Fatal("tampered embedding-space schema remained valid")
	}
}

func TestEmbeddingSpaceEvidenceCannotClaimAnAbsentRevision(t *testing.T) {
	base := EmbeddingSpace{
		Provider: "test", Runtime: "test", Model: "embed", Dimensions: 2,
		Normalization: "provider_unspecified", Similarity: "cosine",
		QueryPassageStrategySHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	for _, evidence := range []string{"operator_revision", "immutable_revision"} {
		base.Evidence = evidence
		if _, err := NormalizeEmbeddingSpace(base); err == nil {
			t.Fatalf("%s accepted without revision evidence", evidence)
		}
	}
}

func TestEmbeddingSpaceSeparatesOperatorEvidenceFromProviderRevision(t *testing.T) {
	base := EmbeddingSpace{
		Provider: "test", Runtime: "test", Model: "embed", OperatorRevision: "operator-reviewed-r7", Dimensions: 2,
		Normalization: "provider_unspecified", Similarity: "cosine", Evidence: "operator_revision",
		QueryPassageStrategySHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	space, err := NormalizeEmbeddingSpace(base)
	if err != nil {
		t.Fatal(err)
	}
	if space.Revision != "" || space.OperatorRevision != "operator-reviewed-r7" || !space.Valid() {
		t.Fatalf("operator evidence was promoted to provider revision: %#v", space)
	}
	changed := base
	changed.OperatorRevision = "operator-reviewed-r8"
	changedSpace, err := NormalizeEmbeddingSpace(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedSpace.Fingerprint == space.Fingerprint {
		t.Fatal("operator revision change did not change embedding-space identity")
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
