package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureDoRegression(t *testing.T, run func() error) (string, error) {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer func() { os.Stdout = previous; writer.Close() }()
	os.Stdout = writer
	callErr := run()
	writer.Close()
	os.Stdout = previous
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), callErr
}

func TestPlainDoKeepsCalculatorAheadOfConfiguredAgent(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"id": "0123456789abcdef0123456789abcdef", "state": "completed", "result": map[string]any{"status": "review", "answer": map[string]any{"answer": "6.3", "method": "offline-model"}}})
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(file, []byte("server:\n  token: local-regression-test-not-a-secret\nroutes:\n  default:\n    provider: ollama\nagent_api:\n  url: "+server.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXTBRIDGE_CONFIG", file)
	text, err := captureDoRegression(t, func() error { return doCommand([]string{"Was macht (4,2 * 10.1) mal 3 / 30 + 5?"}) })
	if err != nil || calls != 0 || !strings.Contains(text, "= 9.242") || !strings.Contains(text, "→ tool: calculator · local · no model or pool job") || !strings.Contains(text, "verified computation (not an LLM answer)") {
		t.Fatalf("plain do regressed: calls=%d error=%v output=%s", calls, err, text)
	}
	for _, prompt := range []string{"Calculate 1/0", "What is 1,234 + 0.5?", "Calculate 2^999"} {
		if _, err := captureDoRegression(t, func() error { return doCommand([]string{prompt}) }); err == nil {
			t.Fatalf("invalid arithmetic accepted: %s", prompt)
		}
	}
	if calls != 0 {
		t.Fatal("rejected arithmetic reached agent/model")
	}
	for _, args := range [][]string{
		{"--background", "2+2"}, {"--json", "2+2"},
		{"--task", "code", "--background", "2+2"},
		{"Was macht 2+2? Erkläre außerdem RGB."},
	} {
		before := calls
		if _, err := captureDoRegression(t, func() error { return doCommand(args) }); err != nil {
			t.Fatal(err)
		}
		if calls != before+1 {
			t.Fatal("explicit job/JSON/code/compound task bypassed service")
		}
	}
}

func TestAgentAnswerShowsActualMethodSourcesAndReview(t *testing.T) {
	var out strings.Builder
	renderLocalAgentAnswer(&out, map[string]any{
		"requires_review": true, "duration_ms": 1250.0,
		"answer": map[string]any{"answer": "RGB = Red, Green, Blue", "method": "offline-model", "confidence": 1.0,
			"sources": []any{map[string]any{"title": "RGB color model (local.zim)", "url": "", "snippet": "private-snippet-not-for-terminal"}}},
	})
	text := out.String()
	for _, expected := range []string{"ai  › RGB", "offline-model", "prüfen", "local.zim", "1.25 s"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in %s", expected, text)
		}
	}
	if strings.Contains(text, "verified computation") || strings.Contains(text, "private-snippet") {
		t.Fatal("model promoted to proof or snippet exposed")
	}
}
