package llamaruntime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

type Progress func(message string, received, total int64)

func Install(ctx context.Context, directory string, progress Progress) (string, error) {
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	release, err := latest(ctx)
	if err != nil {
		return "", err
	}
	asset, err := selectAsset(release.Assets)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(asset.Digest, "sha256:") {
		return "", fmt.Errorf("official release asset %s has no SHA256 digest", asset.Name)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	archive := filepath.Join(directory, asset.Name+".partial")
	if err := fetch(ctx, asset, archive, progress); err != nil {
		return "", err
	}
	versionDir := filepath.Join(directory, release.Tag)
	temporary := versionDir + ".partial"
	_ = os.RemoveAll(temporary)
	if err := os.MkdirAll(temporary, 0700); err != nil {
		return "", err
	}
	if strings.HasSuffix(asset.Name, ".zip") {
		err = extractZip(archive, temporary)
	} else {
		err = extractTarGz(archive, temporary)
	}
	if err != nil {
		return "", err
	}
	executable, err := findServer(temporary)
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(executable, 0700)
	}
	_ = os.RemoveAll(versionDir)
	if err := os.Rename(temporary, versionDir); err != nil {
		return "", err
	}
	_ = os.Remove(archive)
	final, err := findServer(versionDir)
	if err != nil {
		return "", err
	}
	pointer := filepath.Join(directory, "current.txt")
	_ = os.WriteFile(pointer, []byte(final+"\n"), 0600)
	progress("Installed llama.cpp "+release.Tag, asset.Size, asset.Size)
	return final, nil
}

func Current(directory string) string {
	raw, err := os.ReadFile(filepath.Join(directory, "current.txt"))
	if err == nil {
		path := strings.TrimSpace(string(raw))
		if stat, statErr := os.Stat(path); statErr == nil && !stat.IsDir() {
			return path
		}
	}
	return ""
}

func latest(ctx context.Context) (Release, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/ggml-org/llama.cpp/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ContextBridge")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub release API returned %s", resp.Status)
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&release); err != nil {
		return Release{}, err
	}
	return release, nil
}

func selectAsset(assets []Asset) (Asset, error) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		return Asset{}, fmt.Errorf("llama.cpp automatic install does not support %s", runtime.GOARCH)
	}
	needle := ""
	switch runtime.GOOS {
	case "windows":
		if arch == "x64" && hasCommand("nvidia-smi") {
			needle = "-bin-win-vulkan-x64.zip"
		} else if arch == "x64" && hasCommand("rocm-smi") {
			needle = "-bin-win-hip-radeon-x64.zip"
		} else {
			needle = "-bin-win-cpu-" + arch + ".zip"
		}
	case "linux":
		if arch == "x64" && hasCommand("rocm-smi") {
			needle = "-bin-ubuntu-rocm-"
		} else if _, err := os.Stat("/dev/dri"); err == nil {
			needle = "-bin-ubuntu-vulkan-" + arch + ".tar.gz"
		} else {
			needle = "-bin-ubuntu-" + arch + ".tar.gz"
		}
	case "darwin":
		needle = "-bin-macos-" + arch + ".tar.gz"
	default:
		return Asset{}, fmt.Errorf("llama.cpp automatic install does not support %s", runtime.GOOS)
	}
	for _, asset := range assets {
		if strings.Contains(asset.Name, needle) {
			return asset, nil
		}
	}
	return Asset{}, fmt.Errorf("official llama.cpp release has no asset matching %s", needle)
}

func fetch(ctx context.Context, asset Asset, target string, progress Progress) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("runtime download returned %s", resp.Status)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	buffer := make([]byte, 256<<10)
	var received int64
	last := time.Now()
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, err := writer.Write(buffer[:n]); err != nil {
				file.Close()
				return err
			}
			received += int64(n)
			if time.Since(last) > 500*time.Millisecond {
				progress("Downloading "+asset.Name, received, asset.Size)
				last = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
			return readErr
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	expected := strings.TrimPrefix(asset.Digest, "sha256:")
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("SHA256 mismatch for %s", asset.Name)
	}
	progress("Verified "+asset.Name, received, asset.Size)
	return nil
}

func extractZip(path, target string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, item := range reader.File {
		path, ok := safeArchivePath(target, item.Name)
		if !ok {
			return fmt.Errorf("unsafe path in runtime archive: %s", item.Name)
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		source, err := item.Open()
		if err != nil {
			return err
		}
		destination, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(destination, source)
		source.Close()
		destination.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func extractTarGz(path, target string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		path, ok := safeArchivePath(target, header.Name)
		if !ok {
			return fmt.Errorf("unsafe path in runtime archive: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			destination, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(destination, reader)
			destination.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
	return nil
}

func safeArchivePath(root, name string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || strings.HasPrefix(clean, string(os.PathSeparator)) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", false
	}
	path := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, path)
	return path, err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func findServer(root string) (string, error) {
	wanted := "llama-server"
	if runtime.GOOS == "windows" {
		wanted += ".exe"
	}
	var result string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.EqualFold(info.Name(), wanted) {
			result = path
			return filepath.SkipAll
		}
		return nil
	})
	if result == "" {
		return "", fmt.Errorf("%s was not present in the runtime archive", wanted)
	}
	return result, nil
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
