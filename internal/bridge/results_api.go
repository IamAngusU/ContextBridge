package bridge

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) handleJobResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	if id == "" || strings.Contains(id, "/") || !jobIDPattern.MatchString(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job result not found"})
		return
	}
	jobsDir := filepath.Join(s.store.dir, "jobs")
	root, err := os.OpenRoot(jobsDir)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job result not found"})
		return
	}
	defer root.Close()
	name := jobStorageStem(id) + ".result.json"
	file, err := root.Open(name)
	if err != nil {
		// Read-only compatibility for records created before collision-free
		// job/result stems were introduced. New writes never use this layout.
		// os.Root also refuses a legacy symlink that escapes the managed jobs
		// directory, even if the local filesystem was modified after the write.
		name = storageID(id) + ".result.json"
		file, err = root.Open(name)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job result not found"})
			return
		}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job result could not be read"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(name), info.ModTime(), file)
}
