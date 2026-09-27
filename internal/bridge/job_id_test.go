package bridge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type failingJobIDReader struct{}

func (failingJobIDReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestPrepareJobFailsClosedWhenRandomnessIsUnavailable(t *testing.T) {
	job := Job{}
	err := prepareJobWithReader(&job, failingJobIDReader{})
	if err == nil || !strings.Contains(err.Error(), "generate job identifier") {
		t.Fatalf("prepare error = %v", err)
	}
	if job.ID != "" || job.Source != "" || job.Route != "" || !job.CreatedAt.IsZero() {
		t.Fatalf("failed identifier generation partially prepared job: %#v", job)
	}
}

func TestPrepareJobRejectsPartialIdentifierRandomness(t *testing.T) {
	job := Job{}
	err := prepareJobWithReader(&job, strings.NewReader("too short"))
	if err == nil || !strings.Contains(err.Error(), "generate job identifier") {
		t.Fatalf("prepare error = %v", err)
	}
	if job.ID != "" || job.Source != "" || job.Route != "" || !job.CreatedAt.IsZero() {
		t.Fatalf("partial identifier randomness mutated job: %#v", job)
	}
}

func TestPrepareJobUsesCompleteCryptographicIdentifierBytes(t *testing.T) {
	job := Job{}
	if err := prepareJobWithReader(&job, strings.NewReader("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if job.ID != "30313233343536373839616263646566" {
		t.Fatalf("job ID = %q", job.ID)
	}
	if job.Source != "api" || job.Route != "default" || job.CreatedAt.IsZero() || time.Since(job.CreatedAt) > time.Minute {
		t.Fatalf("job defaults were not prepared: %#v", job)
	}
}

func TestPrepareJobPreservesCallerIdentifierWithoutRandomness(t *testing.T) {
	job := Job{ID: "caller-provided"}
	if err := prepareJobWithReader(&job, failingJobIDReader{}); err != nil {
		t.Fatal(err)
	}
	if job.ID != "caller-provided" {
		t.Fatalf("caller ID changed to %q", job.ID)
	}
}
