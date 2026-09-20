package llamaruntime

import (
	"archive/zip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeAssetUsesGitHubDownloadField(t *testing.T) {
	var release Release
	if err := json.Unmarshal([]byte(`{"tag_name":"v1.0.0","assets":[{"name":"runtime.zip","browser_download_url":"https://example.test/runtime.zip","digest":"sha256:test","size":1}]}`), &release); err != nil {
		t.Fatal(err)
	}
	if len(release.Assets) != 1 || release.Assets[0].URL != "https://example.test/runtime.zip" {
		t.Fatalf("GitHub runtime download URL was not decoded: %#v", release)
	}
}

func TestValidatedZipEntrySizeRejectsUnsignedOverflow(t *testing.T) {
	for _, size := range []uint64{uint64(maximumRuntimeEntryBytes) + 1, math.MaxUint64} {
		if _, err := validatedZipEntrySize(size, 0); err == nil {
			t.Fatalf("accepted oversized ZIP entry %d", size)
		}
	}
	if _, err := validatedZipEntrySize(1, maximumRuntimeExtractedBytes); err == nil {
		t.Fatal("accepted entry beyond the aggregate extraction limit")
	}
	if size, err := validatedZipEntrySize(4096, 0); err != nil || size != 4096 {
		t.Fatalf("rejected safe ZIP entry: size=%d err=%v", size, err)
	}
}

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

func TestCurrentRuntimeStaysInsideManagedDirectory(t *testing.T) {
	directory := t.TempDir()
	versionDirectory := filepath.Join(directory, "b1")
	if err := os.MkdirAll(versionDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(versionDirectory, "llama-server")
	if err := os.WriteFile(executable, []byte("runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "current.txt"), []byte(executable+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if current := Current(directory); current != executable {
		t.Fatalf("current runtime = %q, want %q", current, executable)
	}

	outside := filepath.Join(t.TempDir(), "outside-runtime")
	if err := os.WriteFile(outside, []byte("unmanaged"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "current.txt"), []byte(outside+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if current := Current(directory); current != "" {
		t.Fatalf("accepted unmanaged runtime %q", current)
	}
}
