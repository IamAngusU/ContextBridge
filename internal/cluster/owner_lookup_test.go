package cluster

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestProducerOwnerLookupRejectsForeignRecordsBeforeBodyDecode(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{ID: "run-owner-lookup", Pipeline: "test", OwnerSubject: "producer-a", Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
	if err := store.CreatePipelineRunAdmitted(run, 10, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketJobs).Put([]byte(job.ID), []byte(`{"payload":`)); err != nil {
			return err
		}
		return tx.Bucket(bucketPipelineRuns).Put([]byte(run.ID), []byte(`{"input":`))
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetJobForOwner(job.ID, "producer-b"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign job lookup decoded authoritative body or exposed existence: %v", err)
	}
	if _, err := store.GetPipelineRunForOwner(run.ID, "producer-b"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign pipeline lookup decoded authoritative body or exposed existence: %v", err)
	}
	if _, err := store.GetJobForOwner(job.ID, "producer-a"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner lookup did not reach its authoritative malformed body: %v", err)
	}
	if _, err := store.GetPipelineRunForOwner(run.ID, "producer-a"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pipeline owner lookup did not reach its authoritative malformed body: %v", err)
	}
}

func TestExecutionOwnerLookupsMigrateExistingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.db")
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = db.Update(func(tx *bolt.Tx) error {
		jobs, err := tx.CreateBucket(bucketJobs)
		if err != nil {
			return err
		}
		job := Job{ID: "legacy-owner-job", OwnerSubject: "producer-a", Status: JobCompleted, CreatedAt: now, UpdatedAt: now, FinishedAt: now}
		if err := putJSON(jobs, job.ID, job); err != nil {
			return err
		}
		runs, err := tx.CreateBucket(bucketPipelineRuns)
		if err != nil {
			return err
		}
		run := PipelineRun{ID: "legacy-owner-run", Pipeline: "test", OwnerSubject: "producer-a", Status: "completed", CreatedAt: now, FinishedAt: now}
		return putJSON(runs, run.ID, run)
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetJobForOwner("legacy-owner-job", "producer-a"); err != nil {
		t.Fatalf("migrated job owner lookup failed: %v", err)
	}
	if _, err := store.GetPipelineRunForOwner("legacy-owner-run", "producer-a"); err != nil {
		t.Fatalf("migrated pipeline owner lookup failed: %v", err)
	}
	if _, err := store.GetJobForOwner("legacy-owner-job", "producer-b"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated foreign job became visible: %v", err)
	}
}
