package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const maxDraftHistoryBytes = 1 << 20
const maxDraftTextBytes = 16 << 10

type browserDraft struct {
	CapturedAt time.Time `json:"captured_at"`
	Profile    string    `json:"profile"`
	TabID      int       `json:"tab_id"`
	TabTitle   string    `json:"tab_title,omitempty"`
	Origin     string    `json:"origin"`
	SessionID  string    `json:"session_id,omitempty"`
	Text       string    `json:"text"`
}

func (s *Server) handleBrowserDrafts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var draft browserDraft
	if err := decodeJSON(r.Body, &draft, 20<<10); err != nil || !validBrowserDraft(draft) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or oversized browser draft"})
		return
	}
	draft.CapturedAt = time.Now()
	draft.Profile = truncateUTF8(strings.TrimSpace(draft.Profile), 40)
	draft.TabTitle = truncateUTF8(strings.TrimSpace(draft.TabTitle), 200)
	draft.SessionID = truncateUTF8(strings.TrimSpace(draft.SessionID), 100)
	if err := s.saveBrowserDraft(draft); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "draft could not be saved; original editor was not cleared"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func validBrowserDraft(draft browserDraft) bool {
	if draft.TabID <= 0 || draft.Profile == "" || len(draft.Profile) > 40 || len(draft.TabTitle) > 200 || len(draft.SessionID) > 100 {
		return false
	}
	if len(draft.Text) == 0 || len(draft.Text) > maxDraftTextBytes || !utf8.ValidString(draft.Text) || strings.TrimSpace(draft.Text) == "" || strings.ContainsRune(draft.Text, 0) {
		return false
	}
	parsed, err := url.Parse(draft.Origin)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && draft.Origin == parsed.Scheme+"://"+parsed.Host && len(draft.Origin) <= 200
}

func (s *Server) saveBrowserDraft(draft browserDraft) error {
	s.draftHistoryMu.Lock()
	defer s.draftHistoryMu.Unlock()
	dir := s.draftHistoryDir
	if dir == "" {
		return errors.New("private user directory is unavailable")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "draft-history.jsonl")
	existing, err := readDraftHistoryTail(path)
	if err != nil {
		return err
	}
	line, err := json.Marshal(draft)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	for len(existing)+len(line) > maxDraftHistoryBytes {
		end := bytes.IndexByte(existing, '\n')
		if end < 0 {
			existing = nil
			break
		}
		existing = existing[end+1:]
	}
	output := append(existing, line...)
	tmp, err := os.CreateTemp(dir, ".draft-history-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(output); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readDraftHistoryTail(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - maxDraftHistoryBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxDraftHistoryBytes))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			return nil, nil
		}
		data = data[end+1:]
	}
	return data, nil
}
