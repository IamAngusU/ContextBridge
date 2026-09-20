package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReleaseArchivesAreByteReproducible(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"contextbridge":      "binary bytes",
		"README.md":          "read me\n",
		"docs/operations.md": "operations\n",
	}
	for name, content := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, suffix := range []string{".tar.gz", ".zip"} {
		first := filepath.Join(t.TempDir(), "first"+suffix)
		second := filepath.Join(t.TempDir(), "second"+suffix)
		if err := writeArchive(source, first); err != nil {
			t.Fatal(err)
		}
		for name := range files {
			path := filepath.Join(source, filepath.FromSlash(name))
			future := time.Now().Add(12 * time.Hour)
			if err := os.Chtimes(path, future, future); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeArchive(source, second); err != nil {
			t.Fatal(err)
		}
		firstBytes, err := os.ReadFile(first)
		if err != nil {
			t.Fatal(err)
		}
		secondBytes, err := os.ReadFile(second)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(firstBytes, secondBytes) {
			t.Fatalf("%s archives differ despite identical contents", suffix)
		}
	}
}

func TestReleaseArchivesNormalizeMetadataAndContents(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	wantTime := time.Unix(1700000000, 0).UTC()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "contextbridge"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := writeArchive(source, tarPath); err != nil {
		t.Fatal(err)
	}
	tarFile, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tarFile.Close()
	gzipReader, err := gzip.NewReader(tarFile)
	if err != nil {
		t.Fatal(err)
	}
	if !gzipReader.ModTime.Equal(wantTime) {
		t.Fatalf("gzip timestamp = %s, want %s", gzipReader.ModTime, wantTime)
	}
	tarReader := tar.NewReader(gzipReader)
	header, err := tarReader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "contextbridge" || header.Mode != 0o755 || !header.ModTime.Equal(wantTime) {
		t.Fatalf("unexpected tar header: %#v", header)
	}
	payload, err := io.ReadAll(tarReader)
	if err != nil || string(payload) != "payload" {
		t.Fatalf("tar payload = %q, %v", payload, err)
	}

	zipPath := filepath.Join(t.TempDir(), "release.zip")
	if err := writeArchive(source, zipPath); err != nil {
		t.Fatal(err)
	}
	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zipReader.Close()
	if len(zipReader.File) != 1 {
		t.Fatalf("zip entries = %d, want 1", len(zipReader.File))
	}
	entry := zipReader.File[0]
	if entry.Name != "contextbridge" || entry.Mode().Perm() != 0o755 || !entry.Modified.Equal(wantTime) {
		t.Fatalf("unexpected zip header: %#v", entry.FileHeader)
	}
}

func TestArchiveTimestampRejectsInvalidValue(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "-1")
	if _, err := archiveTimestamp(); err == nil {
		t.Fatal("negative SOURCE_DATE_EPOCH was accepted")
	}
}
