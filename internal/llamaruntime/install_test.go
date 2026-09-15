package llamaruntime

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeArchivePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "folder/../../escape", "/absolute/path"} {
		if _, ok := safeArchivePath(root, name); ok {
			t.Fatalf("accepted unsafe archive path %q", name)
		}
	}
	if path, ok := safeArchivePath(root, "bin/llama-server"); !ok || filepath.Dir(path) != filepath.Join(root, "bin") {
		t.Fatalf("rejected safe archive path %q", path)
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "runtime.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	item, err := writer.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := item.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(archive, t.TempDir()); err == nil {
		t.Fatal("expected archive traversal to be rejected")
	}
}

func TestExtractZipRejectsDuplicatePaths(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "runtime.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, name := range []string{"bin/llama-server", "bin/llama-server"} {
		item, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := item.Write([]byte("data")); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(archive, t.TempDir()); err == nil {
		t.Fatal("expected duplicate archive path to be rejected")
	}
}

func TestSecureRuntimeDownloadURL(t *testing.T) {
	for _, value := range []string{"http://example.test/runtime.zip", "file:///tmp/runtime.zip", "https://user@example.test/runtime.zip", "not-a-url"} {
		if secureDownloadURL(value) {
			t.Fatalf("accepted unsafe runtime URL %q", value)
		}
	}
	if !secureDownloadURL("https://github.com/ggml-org/llama.cpp/releases/download/b1/runtime.zip") {
		t.Fatal("rejected HTTPS runtime URL")
	}
}
