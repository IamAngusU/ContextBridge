package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestBrowserDraftHistoryIsBoundedAndOldestFirst(t *testing.T) {
	dir := t.TempDir()
	server := &Server{cfg: config.Config{Storage: config.Storage{Directory: dir}}, draftHistoryDir: dir}
	for index := 0; index < 80; index++ {
		draft := browserDraft{Profile: "gemini", TabID: 42, TabTitle: "Test chat", Origin: "https://gemini.google.com", SessionID: "test-session", Text: strings.Repeat("x", 15000) + string(rune('A'+index%26))}
		if err := server.saveBrowserDraft(draft); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "draft-history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxDraftHistoryBytes {
		t.Fatalf("history exceeded 1 MiB: %d", len(data))
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) >= 80 || len(lines) == 0 {
		t.Fatalf("oldest records were not rotated: %d lines", len(lines))
	}
	for _, line := range lines {
		var record browserDraft
		if err := json.Unmarshal(line, &record); err != nil || record.Profile != "gemini" || record.Text == "" {
			t.Fatalf("invalid retained history line: %v", err)
		}
	}
}

func TestBrowserDraftMustBeValidBeforeItIsSaved(t *testing.T) {
	dir := t.TempDir()
	server := &Server{cfg: config.Config{Storage: config.Storage{Directory: dir}}, draftHistoryDir: dir}
	valid := browserDraft{Profile: "chatgpt", TabID: 11, Origin: "https://chatgpt.com", Text: "my unsent note"}
	for _, draft := range []browserDraft{
		{Profile: "chatgpt", TabID: 11, Origin: "https://chatgpt.com", Text: strings.Repeat("x", maxDraftTextBytes+1)},
		{Profile: "chatgpt", TabID: 11, Origin: "https://chatgpt.com/c/private", Text: "draft"},
		{Profile: "chatgpt", TabID: 11, Origin: "https://chatgpt.com", Text: "   "},
	} {
		if validBrowserDraft(draft) {
			t.Fatal("unsafe draft passed validation")
		}
	}
	body, _ := json.Marshal(valid)
	req := httptest.NewRequest(http.MethodPost, "/v1/browser/drafts", bytes.NewReader(body))
	response := httptest.NewRecorder()
	server.handleBrowserDrafts(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "draft-history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var saved browserDraft
	if err := json.Unmarshal(bytes.TrimSpace(data), &saved); err != nil || saved.CapturedAt.IsZero() || saved.Text != valid.Text {
		t.Fatalf("draft was not saved with a server timestamp: %v", err)
	}
}
