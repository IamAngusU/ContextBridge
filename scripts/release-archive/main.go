package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: release-archive SOURCE-DIRECTORY TARGET.(zip|tar.gz)")
		os.Exit(2)
	}
	if err := writeArchive(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeArchive(source, target string) error {
	timestamp, err := archiveTimestamp()
	if err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(strings.ToLower(target), ".tar.gz"):
		return writeTarGzip(source, target, timestamp)
	case strings.HasSuffix(strings.ToLower(target), ".zip"):
		return writeZip(source, target, timestamp)
	default:
		return errors.New("release archive target must end in .zip or .tar.gz")
	}
}

func archiveTimestamp() (time.Time, error) {
	raw := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH"))
	if raw == "" {
		// A stable fallback keeps local source exports reproducible even when Git
		// metadata is unavailable. Official builds set the commit timestamp.
		return time.Unix(946684800, 0).UTC(), nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, errors.New("SOURCE_DATE_EPOCH must be a non-negative Unix timestamp")
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func canonicalArchiveRoot(source string) (string, *os.Root, error) {
	root, err := filepath.Abs(source)
	if err != nil {
		return "", nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, err
	}
	// #nosec G703 -- root is an explicit release-builder input, canonicalized above and opened with os.OpenRoot below.
	info, err := os.Stat(root)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("source is not a directory: %s", root)
	}
	rootDirectory, err := os.OpenRoot(root)
	if err != nil {
		return "", nil, err
	}
	return root, rootDirectory, nil
}

func normalizedMode(path string, entry os.FileInfo) os.FileMode {
	switch {
	case entry.IsDir():
		return 0o755 | os.ModeDir
	case strings.EqualFold(filepath.Base(path), "contextbridge") || strings.EqualFold(filepath.Ext(path), ".exe"):
		return 0o755
	default:
		return 0o644
	}
}

func walkArchive(root string, visit func(path, relative string, entry os.FileInfo) error) error {
	// filepath.Walk traverses each directory in lexical order, which is part of
	// the byte-for-byte archive contract.
	// #nosec G703 -- root is a validated release staging directory and symlinks are rejected below.
	return filepath.Walk(root, func(path string, entry os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("release source contains a symlink: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return visit(path, relative, entry)
	})
}

func writeTarGzip(source, target string, timestamp time.Time) (returnErr error) {
	root, rootDirectory, err := canonicalArchiveRoot(source)
	if err != nil {
		return err
	}
	defer rootDirectory.Close()
	file, temporary, err := createPartial(target)
	if err != nil {
		return err
	}
	defer cleanupPartial(temporary, &returnErr)

	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = timestamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	closeWriters := func() error {
		if err := tarWriter.Close(); err != nil {
			return err
		}
		if err := gzipWriter.Close(); err != nil {
			return err
		}
		return file.Close()
	}

	err = walkArchive(root, func(path, relative string, entry os.FileInfo) error {
		name := filepath.ToSlash(relative)
		if entry.IsDir() {
			name += "/"
		}
		header, err := tar.FileInfoHeader(entry, "")
		if err != nil {
			return err
		}
		header.Name = name
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "root", "root"
		header.Mode = int64(normalizedMode(path, entry).Perm())
		header.ModTime = timestamp
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Format = tar.FormatPAX
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !entry.Mode().IsRegular() {
			return nil
		}
		input, err := rootDirectory.Open(relative)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = file.Close()
		return err
	}
	if err := closeWriters(); err != nil {
		return err
	}
	return publishPartial(temporary, target)
}

func writeZip(source, target string, timestamp time.Time) (returnErr error) {
	root, rootDirectory, err := canonicalArchiveRoot(source)
	if err != nil {
		return err
	}
	defer rootDirectory.Close()
	file, temporary, err := createPartial(target)
	if err != nil {
		return err
	}
	defer cleanupPartial(temporary, &returnErr)
	zipWriter := zip.NewWriter(file)

	err = walkArchive(root, func(path, relative string, entry os.FileInfo) error {
		name := filepath.ToSlash(relative)
		if entry.IsDir() {
			name += "/"
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if entry.IsDir() {
			header.Method = zip.Store
		}
		// ZIP timestamps cannot represent dates before 1980.
		modified := timestamp
		minimum := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		if modified.Before(minimum) {
			modified = minimum
		}
		header.Modified = modified
		header.SetMode(normalizedMode(path, entry))
		output, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		if !entry.Mode().IsRegular() {
			return nil
		}
		input, err := rootDirectory.Open(relative)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = zipWriter.Close()
		_ = file.Close()
		return err
	}
	if err := zipWriter.Close(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return publishPartial(temporary, target)
}

func createPartial(target string) (*os.File, string, error) {
	temporary := target + ".partial"
	// #nosec G703 -- target is an explicit release-builder output; O_EXCL prevents replacement of an existing partial archive.
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	return file, temporary, err
}

func cleanupPartial(path string, returnErr *error) {
	if *returnErr != nil {
		// #nosec G703 -- cleanup is restricted to the exact partial output created by createPartial.
		_ = os.Remove(path)
	}
}

func publishPartial(temporary, target string) error {
	// #nosec G703 -- both paths are the exact operator-selected output and its O_EXCL-created partial file.
	return os.Rename(temporary, target)
}
