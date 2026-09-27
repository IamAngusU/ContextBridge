package llamaruntime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
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

func TestRuntimeExtractorsDoNotFollowTargetSymlinks(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		write   func(*testing.T, string)
		extract func(string, string) error
	}{
		{name: "zip", archive: "runtime.zip", write: writeRuntimeZipFixture, extract: extractZip},
		{name: "tar-gzip", archive: "runtime.tar.gz", write: writeRuntimeTarGzFixture, extract: extractTarGz},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), test.archive)
			test.write(t, archive)
			target := t.TempDir()
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(target, "bin")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if err := test.extract(archive, target); err == nil {
				t.Fatal("runtime extraction followed a target-directory symlink")
			}
			if _, err := os.Stat(filepath.Join(outside, "llama-server")); !os.IsNotExist(err) {
				t.Fatalf("runtime extraction escaped its target: %v", err)
			}
		})
	}
}

func TestRuntimeExtractorsPreserveSafeNestedFiles(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		write   func(*testing.T, string)
		extract func(string, string) error
	}{
		{name: "zip", archive: "runtime.zip", write: writeRuntimeZipFixture, extract: extractZip},
		{name: "tar-gzip", archive: "runtime.tar.gz", write: writeRuntimeTarGzFixture, extract: extractTarGz},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), test.archive)
			test.write(t, archive)
			target := t.TempDir()
			if err := test.extract(archive, target); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(target, "bin", "llama-server"))
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "runtime" {
				t.Fatalf("extracted runtime = %q", content)
			}
		})
	}
}

func TestRuntimeExtractorsRejectSymlinkTargets(t *testing.T) {
	tests := []struct {
		name    string
		archive string
		write   func(*testing.T, string)
		extract func(string, string) error
	}{
		{name: "zip", archive: "runtime.zip", write: writeRuntimeZipFixture, extract: extractZip},
		{name: "tar-gzip", archive: "runtime.tar.gz", write: writeRuntimeTarGzFixture, extract: extractTarGz},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), test.archive)
			test.write(t, archive)
			parent := t.TempDir()
			outside := t.TempDir()
			target := filepath.Join(parent, "runtime.partial")
			if err := os.Symlink(outside, target); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if err := test.extract(archive, target); err == nil {
				t.Fatal("runtime extraction accepted a symlink target")
			}
			if _, err := os.Stat(filepath.Join(outside, "bin", "llama-server")); !os.IsNotExist(err) {
				t.Fatalf("runtime extraction escaped through its target: %v", err)
			}
		})
	}
}

func writeRuntimeZipFixture(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	item, err := writer.Create("bin/llama-server")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := item.Write([]byte("runtime")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeRuntimeTarGzFixture(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	writer := tar.NewWriter(gzipWriter)
	content := []byte("runtime")
	if err := writer.WriteHeader(&tar.Header{Name: "bin/llama-server", Mode: 0700, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
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
	wantExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if current := Current(directory); current != wantExecutable {
		t.Fatalf("current runtime = %q, want canonical path %q", current, wantExecutable)
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

func TestWriteCurrentPointerReplacesRegularFileDurably(t *testing.T) {
	directory := t.TempDir()
	versionDirectory := filepath.Join(directory, "b2")
	if err := os.MkdirAll(versionDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(versionDirectory, "llama-server")
	if err := os.WriteFile(executable, []byte("runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "current.txt"), []byte("stale\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeCurrentPointer(directory, executable); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "current.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, []byte(executable+"\n")) {
		t.Fatalf("current pointer = %q", raw)
	}
	want, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got := Current(directory); got != want {
		t.Fatalf("activated runtime = %q, want %q", got, want)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".contextbridge-current-") {
			t.Fatalf("activation left temporary file %q", entry.Name())
		}
	}
}

func TestWriteCurrentPointerRejectsNonRegularDestination(t *testing.T) {
	directory := t.TempDir()
	pointer := filepath.Join(directory, "current.txt")
	if err := os.Mkdir(pointer, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeCurrentPointer(directory, filepath.Join(directory, "runtime")); err == nil {
		t.Fatal("runtime activation replaced a non-regular current pointer")
	}
	if info, err := os.Stat(pointer); err != nil || !info.IsDir() {
		t.Fatalf("non-regular pointer was changed: info=%v err=%v", info, err)
	}
}

func TestRuntimePointerSymlinkCannotBeReadOrOverwritten(t *testing.T) {
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("do-not-change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "current.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got := Current(directory); got != "" {
		t.Fatalf("runtime pointer symlink resolved to %q", got)
	}
	if err := writeCurrentPointer(directory, filepath.Join(directory, "runtime")); err == nil {
		t.Fatal("runtime activation accepted a pointer symlink")
	}
	raw, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "do-not-change" {
		t.Fatalf("runtime activation overwrote symlink target: %q", raw)
	}
}
