package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRegularFileBoundedAcceptsExactAndRejectsOneByteOver(t *testing.T) {
	dir := t.TempDir()
	exact := filepath.Join(dir, "exact.json")
	over := filepath.Join(dir, "over.json")
	if err := os.WriteFile(exact, bytes.Repeat([]byte{'a'}, 1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, bytes.Repeat([]byte{'a'}, 1025), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := readRegularFileBounded(exact, 1024); err != nil || len(value) != 1024 {
		t.Fatalf("exact file boundary rejected: len=%d err=%v", len(value), err)
	}
	if _, err := readRegularFileBounded(over, 1024); err == nil {
		t.Fatal("one byte beyond file boundary accepted")
	}
	if _, err := readRegularFileBounded(dir, 1024); err == nil {
		t.Fatal("directory accepted as a regular input file")
	}
}

func TestChatImageCallSiteAcceptsExactLimitAndRejectsOverrun(t *testing.T) {
	dir := t.TempDir()
	pngHeader := []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'}
	exact := filepath.Join(dir, "exact.png")
	over := filepath.Join(dir, "over.png")
	if err := os.WriteFile(exact, append(pngHeader, bytes.Repeat([]byte{0}, (8<<20)-len(pngHeader))...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, append(pngHeader, bytes.Repeat([]byte{0}, (8<<20)+1-len(pngHeader))...), 0600); err != nil {
		t.Fatal(err)
	}
	if raw, mediaType, err := readChatImage(exact); err != nil || len(raw) != 8<<20 || mediaType != "image/png" {
		t.Fatalf("exact chat image boundary rejected: len=%d type=%q err=%v", len(raw), mediaType, err)
	}
	if _, _, err := readChatImage(over); err == nil {
		t.Fatal("chat image one byte over the boundary was accepted")
	}
	if _, _, err := readChatImage(dir); err == nil {
		t.Fatal("chat image accepted a non-regular path")
	}
}

func TestScheduleInputAcceptsExactLimitAndRejectsOverrun(t *testing.T) {
	exact := bytes.Repeat([]byte{'x'}, 12<<20)
	if raw, err := readScheduleInput(bytes.NewReader(exact)); err != nil || len(raw) != len(exact) {
		t.Fatalf("exact schedule boundary rejected: len=%d err=%v", len(raw), err)
	}
	if _, err := readScheduleInput(bytes.NewReader(append(exact, 'x'))); err == nil {
		t.Fatal("schedule stdin one byte over the boundary was silently truncated")
	}
}

func TestReviewFileDiscoveryIsBoundedAndRejectsOversizedInputs(t *testing.T) {
	dir := t.TempDir()
	if value, err := readOptionalTextBounded(filepath.Join(dir, "missing.txt"), 8); err != nil || value != "" {
		t.Fatalf("missing optional text = %q, %v", value, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "message.txt"), []byte("123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOptionalTextBounded(filepath.Join(dir, "message.txt"), 8); err == nil {
		t.Fatal("oversized optional text accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if image, err := firstRegularImageFile(dir); err != nil || filepath.Base(image) != "image.png" {
		t.Fatalf("image discovery = %q, %v", image, err)
	}
}
