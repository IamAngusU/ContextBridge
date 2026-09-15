package cluster

import (
	"bytes"
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	DefaultRetentionDays                = 30
	DefaultMaxTerminalJobs              = 500
	DefaultMaxEvents                    = 5000
	DefaultMaxTerminalPipelineRuns      = 200
	DefaultRetentionSweepSeconds        = 300
	MaximumRetentionDays                = 3650
	MaximumRetainedTerminalJobs         = 100000
	MaximumRetainedEvents               = 1000000
	MaximumRetainedTerminalPipelineRuns = 100000
	MinimumRetentionSweepSeconds        = 10
	MaximumRetentionSweepSeconds        = 86400
)

var (
	bucketHistoricalTotals = []byte("historical_totals")
	keyPrunedJobTotals     = []byte("pruned_job_totals")
)

// RetentionPolicy bounds detailed relay history. MaxAge and every count must
// be positive so retention cannot accidentally be disabled by a zero value.
type RetentionPolicy struct {
	MaxAge                  time.Duration
	MaxTerminalJobs         int
	MaxEvents               int
	MaxTerminalPipelineRuns int
}

type RetentionResult struct {
	Jobs         int
	Events       int
	PipelineRuns int
}

type historicalJobTotals struct {
	JobsByState map[string]uint64 `json:"jobs_by_state"`
	Usage       Usage             `json:"usage"`
}

type retentionCandidate struct {
	ID string
	At time.Time
}

type retentionHeap []retentionCandidate

func (h retentionHeap) Len() int { return len(h) }
func (h retentionHeap) Less(i, j int) bool {
	if h[i].At.Equal(h[j].At) {
		return h[i].ID < h[j].ID
	}
	return h[i].At.Before(h[j].At)
}
func (h retentionHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *retentionHeap) Push(value interface{}) {
	*h = append(*h, value.(retentionCandidate))
}
func (h *retentionHeap) Pop() interface{} {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func (p RetentionPolicy) Validate() error {
	if p.MaxAge < 24*time.Hour || p.MaxAge > time.Duration(MaximumRetentionDays)*24*time.Hour {
		return fmt.Errorf("retention age must be between 1 and %d days", MaximumRetentionDays)
	}
	if p.MaxTerminalJobs <= 0 || p.MaxTerminalJobs > MaximumRetainedTerminalJobs {
		return fmt.Errorf("retained terminal jobs must be between 1 and %d", MaximumRetainedTerminalJobs)
	}
	if p.MaxEvents <= 0 || p.MaxEvents > MaximumRetainedEvents {
		return fmt.Errorf("retained events must be between 1 and %d", MaximumRetainedEvents)
	}
	if p.MaxTerminalPipelineRuns <= 0 || p.MaxTerminalPipelineRuns > MaximumRetainedTerminalPipelineRuns {
		return fmt.Errorf("retained terminal pipeline runs must be between 1 and %d", MaximumRetainedTerminalPipelineRuns)
	}
	return nil
}

// PruneRetention deletes detailed terminal history older than MaxAge or beyond
// its newest-record count. It never removes active or unknown lifecycle states.
// Job records, their indexes, and lifetime aggregate updates share one Bolt
// transaction, so a failed sweep cannot leave partially pruned history.
func (s *Store) PruneRetention(now time.Time, policy RetentionPolicy) (RetentionResult, error) {
	var result RetentionResult
	if err := policy.Validate(); err != nil {
		return result, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	cutoff := now.Add(-policy.MaxAge)
	err := s.db.Update(func(tx *bolt.Tx) error {
		jobKeep, err := newestTerminalJobs(tx.Bucket(bucketJobs), cutoff, policy.MaxTerminalJobs)
		if err != nil {
			return err
		}
		totals, err := readHistoricalJobTotals(tx.Bucket(bucketHistoricalTotals))
		if err != nil {
			return err
		}
		jobs := tx.Bucket(bucketJobs)
		for cursor, key, value := jobs.Cursor(), []byte(nil), []byte(nil); ; {
			if key == nil {
				key, value = cursor.First()
			} else {
				key, value = cursor.Next()
			}
			if key == nil {
				break
			}
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return err
			}
			if job.ID != string(key) {
				return errors.New("job record key does not match its id")
			}
			if !terminalJobStatus(job.Status) {
				continue
			}
			finished := jobRetentionTime(job)
			if !finished.Before(cutoff) {
				if _, keep := jobKeep[job.ID]; keep {
					continue
				}
			}
			addHistoricalJob(&totals, job)
			if err := deleteJobIndexes(tx, job); err != nil {
				return err
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			result.Jobs++
		}
		if err := putJSON(tx.Bucket(bucketHistoricalTotals), string(keyPrunedJobTotals), totals); err != nil {
			return err
		}
		removedEvents, err := pruneEvents(tx.Bucket(bucketEvents), cutoff, policy.MaxEvents)
		if err != nil {
			return err
		}
		result.Events = removedEvents
		removedRuns, err := prunePipelineRuns(tx.Bucket(bucketPipelineRuns), cutoff, policy.MaxTerminalPipelineRuns)
		if err != nil {
			return err
		}
		result.PipelineRuns = removedRuns
		return nil
	})
	return result, err
}

func newestTerminalJobs(bucket *bolt.Bucket, cutoff time.Time, limit int) (map[string]struct{}, error) {
	selected := &retentionHeap{}
	heap.Init(selected)
	err := bucket.ForEach(func(key, value []byte) error {
		var job Job
		if err := json.Unmarshal(value, &job); err != nil {
			return err
		}
		if job.ID != string(key) {
			return errors.New("job record key does not match its id")
		}
		at := jobRetentionTime(job)
		if !terminalJobStatus(job.Status) || at.Before(cutoff) {
			return nil
		}
		pushNewest(selected, retentionCandidate{ID: job.ID, At: at}, limit)
		return nil
	})
	if err != nil {
		return nil, err
	}
	keep := make(map[string]struct{}, selected.Len())
	for selected.Len() > 0 {
		candidate := heap.Pop(selected).(retentionCandidate)
		keep[candidate.ID] = struct{}{}
	}
	return keep, nil
}

func pruneEvents(bucket *bolt.Bucket, cutoff time.Time, limit int) (int, error) {
	kept := 0
	removed := 0
	cursor := bucket.Cursor()
	for key, value := cursor.Last(); key != nil; key, value = cursor.Prev() {
		var event Event
		if err := json.Unmarshal(value, &event); err != nil {
			return removed, err
		}
		kept++
		if kept <= limit && !event.Time.Before(cutoff) {
			continue
		}
		if err := cursor.Delete(); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func prunePipelineRuns(bucket *bolt.Bucket, cutoff time.Time, limit int) (int, error) {
	selected := &retentionHeap{}
	heap.Init(selected)
	if err := bucket.ForEach(func(key, value []byte) error {
		var run PipelineRun
		if err := json.Unmarshal(value, &run); err != nil {
			return err
		}
		if run.ID != string(key) {
			return errors.New("pipeline run record key does not match its id")
		}
		at := pipelineRetentionTime(run)
		if !terminalPipelineStatus(run.Status) || at.Before(cutoff) {
			return nil
		}
		pushNewest(selected, retentionCandidate{ID: run.ID, At: at}, limit)
		return nil
	}); err != nil {
		return 0, err
	}
	keep := make(map[string]struct{}, selected.Len())
	for selected.Len() > 0 {
		candidate := heap.Pop(selected).(retentionCandidate)
		keep[candidate.ID] = struct{}{}
	}
	removed := 0
	cursor := bucket.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var run PipelineRun
		if err := json.Unmarshal(value, &run); err != nil {
			return removed, err
		}
		if run.ID != string(key) {
			return removed, errors.New("pipeline run record key does not match its id")
		}
		if !terminalPipelineStatus(run.Status) {
			continue
		}
		at := pipelineRetentionTime(run)
		if !at.Before(cutoff) {
			if _, retained := keep[run.ID]; retained {
				continue
			}
		}
		if err := cursor.Delete(); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func pushNewest(selected *retentionHeap, candidate retentionCandidate, limit int) {
	if selected.Len() < limit {
		heap.Push(selected, candidate)
		return
	}
	oldest := (*selected)[0]
	if candidate.At.After(oldest.At) || candidate.At.Equal(oldest.At) && candidate.ID > oldest.ID {
		heap.Pop(selected)
		heap.Push(selected, candidate)
	}
}

func terminalJobStatus(status string) bool {
	return status == JobCompleted || status == JobFailed || status == JobCancelled
}

func terminalPipelineStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func jobRetentionTime(job Job) time.Time {
	if !job.FinishedAt.IsZero() {
		return job.FinishedAt
	}
	if !job.UpdatedAt.IsZero() {
		return job.UpdatedAt
	}
	return job.CreatedAt
}

func pipelineRetentionTime(run PipelineRun) time.Time {
	if !run.FinishedAt.IsZero() {
		return run.FinishedAt
	}
	return run.CreatedAt
}

func deleteJobIndexes(tx *bolt.Tx, job Job) error {
	index := tx.Bucket(bucketJobIndex)
	key := jobIndexKey(job)
	if value := index.Get(key); value != nil && bytes.Equal(value, []byte(job.ID)) {
		if err := index.Delete(key); err != nil {
			return err
		}
	}
	return deleteQueueEntry(tx.Bucket(bucketQueue), job.ID)
}

func readHistoricalJobTotals(bucket *bolt.Bucket) (historicalJobTotals, error) {
	totals := historicalJobTotals{JobsByState: map[string]uint64{}}
	if bucket == nil {
		return totals, nil
	}
	raw := bucket.Get(keyPrunedJobTotals)
	if raw == nil {
		return totals, nil
	}
	if err := json.Unmarshal(raw, &totals); err != nil {
		return totals, err
	}
	if totals.JobsByState == nil {
		totals.JobsByState = map[string]uint64{}
	}
	return totals, nil
}

func addHistoricalJob(totals *historicalJobTotals, job Job) {
	if totals.JobsByState == nil {
		totals.JobsByState = map[string]uint64{}
	}
	totals.JobsByState[job.Status] = saturatingUint64Add(totals.JobsByState[job.Status], 1)
	totals.Usage.InputTokens = saturatingUint64Add(totals.Usage.InputTokens, job.Usage.InputTokens)
	totals.Usage.OutputTokens = saturatingUint64Add(totals.Usage.OutputTokens, job.Usage.OutputTokens)
	totals.Usage.TotalTokens = saturatingUint64Add(totals.Usage.TotalTokens, job.Usage.TotalTokens)
	totals.Usage.EquivalentCostUSD += job.Usage.EquivalentCostUSD
	totals.Usage.SavedCostUSD += job.Usage.SavedCostUSD
}

func mergeHistoricalJobTotals(overview *Overview, totals historicalJobTotals) {
	for status, count := range totals.JobsByState {
		overview.JobsByState[status] = saturatingUint64Add(overview.JobsByState[status], count)
	}
	overview.Usage.InputTokens = saturatingUint64Add(overview.Usage.InputTokens, totals.Usage.InputTokens)
	overview.Usage.OutputTokens = saturatingUint64Add(overview.Usage.OutputTokens, totals.Usage.OutputTokens)
	overview.Usage.TotalTokens = saturatingUint64Add(overview.Usage.TotalTokens, totals.Usage.TotalTokens)
	overview.Usage.EquivalentCostUSD += totals.Usage.EquivalentCostUSD
	overview.Usage.SavedCostUSD += totals.Usage.SavedCostUSD
}

func saturatingUint64Add(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}
