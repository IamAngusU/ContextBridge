package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: release-archive SOURCE-DIRECTORY TARGET.tar.gz")
		os.Exit(2)
	}
	if err := writeArchive(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeArchive(source, target string) (returnErr error) {
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", root)
	}

	temporary := target + ".partial"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			_ = os.Remove(temporary)
		}
	}()
	gzipWriter := gzip.NewWriter(file)
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

	err = filepath.Walk(root, func(path string, entry os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if entry.IsDir() {
			name += "/"
		}
		header, err := tar.FileInfoHeader(entry, "")
		if err != nil {
			return err
		}
		header.Name = name
		header.Uid = 0
		header.Gid = 0
		header.Uname = "root"
		header.Gname = "root"
		switch {
		case entry.IsDir():
			header.Mode = 0o755
		case strings.EqualFold(filepath.Base(path), "contextbridge"):
			header.Mode = 0o755
		default:
			header.Mode = 0o644
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !entry.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(path)
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
	return os.Rename(temporary, target)
}
