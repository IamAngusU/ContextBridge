package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecksumFailureDoesNotPoisonNextModelPull(t *testing.T) {
	good := []byte("verified-model")
	hash := sha256.Sum256(good)
	expected := hex.EncodeToString(hash[:])
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if request.Header.Get("Range") != "" {
			t.Fatalf("checksum-failed partial was reused: %q", request.Header.Get("Range"))
		}
		if calls == 1 {
			_, _ = w.Write([]byte("corrupt-model"))
			return
		}
		_, _ = w.Write(good)
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "model.gguf")
	if err := download(context.Background(), server.URL, target, expected, func(string, int64, int64) {}); err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("corrupt first download returned %v", err)
	}
	if _, err := os.Stat(target + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("checksum-failed partial survived: %v", err)
	}
	if err := download(context.Background(), server.URL, target, expected, func(string, int64, int64) {}); err != nil {
		t.Fatalf("clean retry failed: %v", err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != string(good) || calls != 2 {
		t.Fatalf("retry result=%q calls=%d err=%v", raw, calls, err)
	}
}

func TestCompleteValidPartialFinalizesAfterRangeNotSatisfiable(t *testing.T) {
	content := []byte("complete-partial")
	hash := sha256.Sum256(content)
	expected := hex.EncodeToString(hash[:])
	target := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(target+".partial", content, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") != "bytes=16-" {
			t.Fatalf("unexpected range: %q", request.Header.Get("Range"))
		}
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()
	if err := writePartialDownloadMetadata(target+".partial.json", partialDownloadMetadata{URL: server.URL, Expected: expected}); err != nil {
		t.Fatal(err)
	}
	if err := download(context.Background(), server.URL, target, expected, func(string, int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != string(content) {
		t.Fatalf("valid complete partial was not finalized: %q %v", raw, err)
	}
}

func TestBadContentRangeCannotAppendToPartial(t *testing.T) {
	target := filepath.Join(t.TempDir(), "model.gguf")
	partial := target + ".partial"
	if err := os.WriteFile(partial, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-2/6")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("def"))
	}))
	defer server.Close()
	if err := writePartialDownloadMetadata(partial+".json", partialDownloadMetadata{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := download(context.Background(), server.URL, target, "", func(string, int64, int64) {}); err == nil || !strings.Contains(err.Error(), "requested offset") {
		t.Fatalf("bad Content-Range returned %v", err)
	}
	if raw, err := os.ReadFile(partial); err != nil || string(raw) != "abc" {
		t.Fatalf("bad range modified partial: %q %v", raw, err)
	}
}

func TestPartialFromDifferentModelRevisionIsDiscarded(t *testing.T) {
	content := []byte("new-model-revision")
	hash := sha256.Sum256(content)
	expected := hex.EncodeToString(hash[:])
	target := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(target+".partial", []byte("old-revision"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePartialDownloadMetadata(target+".partial.json", partialDownloadMetadata{
		URL: "https://models.invalid/old-revision.gguf", Expected: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if value := request.Header.Get("Range"); value != "" {
			t.Fatalf("different model revision reused old partial range %q", value)
		}
		_, _ = w.Write(content)
	}))
	defer server.Close()
	if err := download(context.Background(), server.URL, target, expected, func(string, int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != string(content) {
		t.Fatalf("different model revision was not downloaded cleanly: %q %v", raw, err)
	}
}
