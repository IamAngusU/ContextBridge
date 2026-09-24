package bridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobResultReadStaysInsideManagedJobsRoot(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const id = "linked-job"
	secret := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(secret, []byte(`{"secret":"must-not-leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "jobs", jobStorageStem(id)+".result.json")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symbolic-link result check is unavailable on this host: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+id, nil)
	(&Server{store: store}).handleJobResult(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("escaped result returned status %d, want 404", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "must-not-leak") {
		t.Fatal("result endpoint followed a symlink outside the managed jobs root")
	}
}
