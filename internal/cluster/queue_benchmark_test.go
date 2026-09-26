package cluster

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func BenchmarkQueuedJobsFairWindow(b *testing.B) {
	for _, queued := range []int{100, 1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("queued_%d", queued), func(b *testing.B) {
			store, err := OpenStore(filepath.Join(b.TempDir(), "cluster.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			base := time.Unix(1_700_000_000, 0).UTC()
			if err := store.db.Update(func(tx *bolt.Tx) error {
				queue := tx.Bucket(bucketQueue)
				for index := 0; index < queued; index++ {
					job := Job{ID: fmt.Sprintf("bench-%06d", index), OwnerSubject: fmt.Sprintf("owner-%04d", index%1000), Priority: index % 4, CreatedAt: base.Add(time.Duration(index))}
					value, marshalErr := json.Marshal(queueEntry{JobID: job.ID, OwnerSubject: job.OwnerSubject, Priority: job.Priority})
					if marshalErr != nil {
						return marshalErr
					}
					if err := queue.Put(queueKey(job), value); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			var after []byte
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				jobs, next, err := store.QueuedJobsFairWindow(200, after, nil)
				if err != nil || len(jobs) == 0 {
					b.Fatalf("fair window = %d jobs, %v", len(jobs), err)
				}
				after = next
			}
		})
	}
}
