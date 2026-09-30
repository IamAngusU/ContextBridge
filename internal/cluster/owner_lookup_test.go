package cluster

import (
	"crypto/sha256"
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

	job, err := store.CreateJob(SubmitRequest{OwnerSubject: "producer-a", TenantID: "tenant-a", Requirements: Requirements{Task: "generation"}, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	run := PipelineRun{ID: "run-owner-lookup", Pipeline: "test", OwnerSubject: "producer-a", TenantID: "tenant-a", Status: "running", Input: json.RawMessage(`{}`), CreatedAt: time.Now().UTC()}
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
	if _, err := store.GetJobForProducer(job.ID, "producer-a", []string{"tenant-b"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign-tenant job lookup decoded authoritative body or exposed existence: %v", err)
	}
	if _, err := store.GetPipelineRunForProducer(run.ID, "producer-a", []string{"tenant-b"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign-tenant pipeline lookup decoded authoritative body or exposed existence: %v", err)
	}
	if jobs, _, _, scanned, err := store.ListJobSummaryPage(10, nil, "", "producer-a", "", nil, []string{"tenant-b"}); err != nil || len(jobs) != 0 || scanned != 1 {
		t.Fatalf("foreign-tenant history decoded authoritative body: jobs=%#v scanned=%d err=%v", jobs, scanned, err)
	}
	if _, err := store.GetJobForOwner(job.ID, "producer-a"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner lookup did not reach its authoritative malformed body: %v", err)
	}
	if _, err := store.GetPipelineRunForOwner(run.ID, "producer-a"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pipeline owner lookup did not reach its authoritative malformed body: %v", err)
	}
	if _, err := store.GetJobForProducer(job.ID, "producer-a", []string{"tenant-a"}); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("allowed-tenant job lookup did not reach its authoritative malformed body: %v", err)
	}
	if _, err := store.GetPipelineRunForProducer(run.ID, "producer-a", []string{"tenant-a"}); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("allowed-tenant pipeline lookup did not reach its authoritative malformed body: %v", err)
	}
	if _, _, _, _, err := store.ListJobSummaryPage(10, nil, "", "producer-a", "", nil, []string{"tenant-a"}); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("allowed-tenant history did not reach its authoritative malformed body: %v", err)
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
		meta, err := tx.CreateBucket(bucketStoreMeta)
		if err != nil {
			return err
		}
		if err := meta.Put(keyOwnerLookupVersion, []byte("1")); err != nil {
			return err
		}
		jobs, err := tx.CreateBucket(bucketJobs)
		if err != nil {
			return err
		}
		job := Job{ID: "legacy-owner-job", OwnerSubject: "producer-a", TenantID: "tenant-a", Status: JobCompleted, CreatedAt: now, UpdatedAt: now, FinishedAt: now}
		if err := putJSON(jobs, job.ID, job); err != nil {
			return err
		}
		jobOwners, err := tx.CreateBucket(bucketJobOwnerLookup)
		if err != nil {
			return err
		}
		legacyDigest := sha256.Sum256([]byte(job.OwnerSubject))
		if err := jobOwners.Put([]byte(job.ID), legacyDigest[:]); err != nil {
			return err
		}
		runs, err := tx.CreateBucket(bucketPipelineRuns)
		if err != nil {
			return err
		}
		run := PipelineRun{ID: "legacy-owner-run", Pipeline: "test", OwnerSubject: "producer-a", TenantID: "tenant-a", Status: "completed", CreatedAt: now, FinishedAt: now}
		if err := putJSON(runs, run.ID, run); err != nil {
			return err
		}
		pipelineOwners, err := tx.CreateBucket(bucketPipelineOwnerLookup)
		if err != nil {
			return err
		}
		return pipelineOwners.Put([]byte(run.ID), legacyDigest[:])
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
	if err := store.db.View(func(tx *bolt.Tx) error {
		if string(tx.Bucket(bucketStoreMeta).Get(keyOwnerLookupVersion)) != string(ownerLookupVersion) {
			return errors.New("execution scope lookup version was not upgraded")
		}
		if len(tx.Bucket(bucketJobOwnerLookup).Get([]byte("legacy-owner-job"))) != 2*sha256.Size ||
			len(tx.Bucket(bucketPipelineOwnerLookup).Get([]byte("legacy-owner-run"))) != 2*sha256.Size {
			return errors.New("legacy owner-only lookup was not rebuilt with tenant scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetJobForOwner("legacy-owner-job", "producer-a"); err != nil {
		t.Fatalf("migrated job owner lookup failed: %v", err)
	}
	if _, err := store.GetPipelineRunForOwner("legacy-owner-run", "producer-a"); err != nil {
		t.Fatalf("migrated pipeline owner lookup failed: %v", err)
	}
	if _, err := store.GetJobForOwner("legacy-owner-job", "producer-b"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated foreign job became visible: %v", err)
	}
	if _, err := store.GetJobForProducer("legacy-owner-job", "producer-a", []string{"tenant-b"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated foreign-tenant job became visible: %v", err)
	}
	if _, err := store.GetJobForProducer("legacy-owner-job", "producer-a", []string{"tenant-a"}); err != nil {
		t.Fatalf("migrated allowed-tenant job lookup failed: %v", err)
	}
	if _, err := store.GetPipelineRunForProducer("legacy-owner-run", "producer-a", []string{"tenant-b"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated foreign-tenant pipeline became visible: %v", err)
	}
}
