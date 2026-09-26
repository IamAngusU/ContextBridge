package vectorstore

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkLocalSearchTopOne(b *testing.B) {
	for _, documents := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("documents_%d", documents), func(b *testing.B) {
			store := &Local{max: documents, data: map[string]map[string]record{"tenant": {}}}
			for index := 0; index < documents; index++ {
				id := fmt.Sprintf("doc-%06d", index)
				store.data["tenant"][id] = record{Document: Document{ID: id}, Vector: []float32{float32(index + 1), 1}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if _, err := store.Search(context.Background(), "tenant", []float32{1, 0}, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
