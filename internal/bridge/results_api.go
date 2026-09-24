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
	path := filepath.Join(s.store.dir, "jobs", jobStorageStem(id)+".result.json")
	// #nosec G703 -- the route validates the ID and storageID constrains the final filename.
	file, err := os.Open(path)
	if err != nil {
		// Read-only compatibility for records created before collision-free
		// job/result stems were introduced. New writes never use this layout.
		legacy := filepath.Join(s.store.dir, "jobs", storageID(id)+".result.json")
		file, err = os.Open(legacy)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job result not found"})
			return
		}
		path = legacy
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job result could not be read"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}
