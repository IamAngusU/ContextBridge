package bridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletedSessionAccountingRetainsBoundedIDsNotOutputs(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maximumRecentCompletions+100; index++ {
		store.recordCompletionLocked(fmt.Sprintf("job-%d", index))
	}
	queued, completed := store.Stats()
	if queued != 0 || completed != maximumRecentCompletions+100 {
		t.Fatalf("stats = queued %d completed %d", queued, completed)
	}
	if len(store.completed) != maximumRecentCompletions || len(store.completedOrder) != maximumRecentCompletions {
		t.Fatalf("recent completion identity cache is unbounded: %d/%d", len(store.completed), len(store.completedOrder))
	}
}

func TestLocalHistoryPrunesCompletePairsAndKeepsIncompleteJobs(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	jobs := filepath.Join(directory, "jobs")
	write := func(name string, modified time.Time) {
		path := filepath.Join(jobs, name)
		if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	oldJob, oldResult := jobStorageStem("old")+".job.json", jobStorageStem("old")+".result.json"
	newJob, newResult := jobStorageStem("new")+".job.json", jobStorageStem("new")+".result.json"
	activeJob := jobStorageStem("active") + ".job.json"
	write(oldJob, now.Add(-48*time.Hour))
	write(oldResult, now.Add(-48*time.Hour))
	write(newJob, now.Add(-time.Hour))
	write(newResult, now.Add(-time.Hour))
	write(activeJob, now.Add(-72*time.Hour))
	if err := store.ConfigureHistoryRetention(24*time.Hour, 1, 64<<20); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{oldJob, oldResult} {
		if _, err := os.Stat(filepath.Join(jobs, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expired history file %s remains: %v", name, err)
		}
	}
	for _, name := range []string{newJob, newResult, activeJob} {
		if _, err := os.Stat(filepath.Join(jobs, name)); err != nil {
			t.Fatalf("retained history file %s missing: %v", name, err)
		}
	}
}

func TestJobStorageNamesCannotCollideWithResultSuffixIDs(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"foo", "foo.result"} {
		if err := store.SaveJob(Job{ID: id}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveOutput(id, Output{Mode: "text", Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	jobs := filepath.Join(directory, "jobs")
	paths := map[string]bool{}
	for _, id := range []string{"foo", "foo.result"} {
		for _, suffix := range []string{".job.json", ".result.json"} {
			path := filepath.Join(jobs, jobStorageStem(id)+suffix)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("missing collision-free path %s: %v", path, err)
			}
			if paths[path] {
				t.Fatalf("job/result path collision at %s", path)
			}
			paths[path] = true
		}
	}
}
