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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

const (
	maximumRuntimeArchiveBytes   int64 = 4 << 30
	maximumRuntimeExtractedBytes int64 = 8 << 30
	maximumRuntimeEntryBytes     int64 = 4 << 30
	maximumRuntimeArchiveEntries       = 20000
)

var safeReleaseTagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

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
	if !safeReleaseTagPattern.MatchString(release.Tag) || strings.Contains(release.Tag, "..") {
		return "", fmt.Errorf("official release returned an unsafe tag")
	}
	if asset.Name == "" || asset.Name != filepath.Base(asset.Name) || asset.Size <= 0 || asset.Size > maximumRuntimeArchiveBytes {
		return "", fmt.Errorf("official release asset metadata is invalid or exceeds the %d GiB archive limit", maximumRuntimeArchiveBytes>>30)
	}
	if !secureDownloadURL(asset.URL) {
		return "", fmt.Errorf("official release asset URL must use HTTPS")
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
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
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
	committed = true
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
	if asset.Size <= 0 || asset.Size > maximumRuntimeArchiveBytes {
		return fmt.Errorf("runtime archive size must be between 1 byte and %d GiB", maximumRuntimeArchiveBytes>>30)
	}
	if !secureDownloadURL(asset.URL) {
		return fmt.Errorf("runtime download URL must use HTTPS")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	client := &http.Client{Timeout: 0, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many runtime download redirects")
		}
		if !secureDownloadURL(req.URL.String()) {
			return fmt.Errorf("runtime download redirect must use HTTPS")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("runtime download returned %s", resp.Status)
	}
	if resp.ContentLength > 0 && resp.ContentLength != asset.Size {
		return fmt.Errorf("runtime download size differs from release metadata")
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
			if int64(n) > asset.Size-received || int64(n) > maximumRuntimeArchiveBytes-received {
				file.Close()
				return fmt.Errorf("runtime download exceeded its declared or configured size")
			}
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
	if received != asset.Size {
		return fmt.Errorf("runtime download size differs from release metadata")
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
	if len(reader.File) > maximumRuntimeArchiveEntries {
		return fmt.Errorf("runtime archive contains more than %d entries", maximumRuntimeArchiveEntries)
	}
	var extracted int64
	seen := map[string]struct{}{}
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
		if item.FileInfo().Mode()&os.ModeType != 0 {
			return fmt.Errorf("unsupported special entry in runtime archive: %s", item.Name)
		}
		entrySize := int64(item.UncompressedSize64)
		if entrySize < 0 || entrySize > maximumRuntimeEntryBytes || entrySize > maximumRuntimeExtractedBytes-extracted {
			return fmt.Errorf("runtime archive exceeds extraction limits")
		}
		key := strings.ToLower(filepath.Clean(path))
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate path in runtime archive: %s", item.Name)
		}
		seen[key] = struct{}{}
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
		written, copyErr := io.CopyN(destination, source, entrySize+1)
		source.Close()
		destination.Close()
		if copyErr != nil {
			if copyErr != io.EOF {
				return copyErr
			}
		}
		if written != entrySize {
			return fmt.Errorf("runtime archive entry size mismatch: %s", item.Name)
		}
		extracted += written
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
	entries := 0
	var extracted int64
	seen := map[string]struct{}{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maximumRuntimeArchiveEntries {
			return fmt.Errorf("runtime archive contains more than %d entries", maximumRuntimeArchiveEntries)
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
			if header.Size < 0 || header.Size > maximumRuntimeEntryBytes || header.Size > maximumRuntimeExtractedBytes-extracted {
				return fmt.Errorf("runtime archive exceeds extraction limits")
			}
			key := strings.ToLower(filepath.Clean(path))
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate path in runtime archive: %s", header.Name)
			}
			seen[key] = struct{}{}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			destination, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(destination, reader, header.Size+1)
			destination.Close()
			if copyErr != nil && copyErr != io.EOF {
				return copyErr
			}
			if written != header.Size {
				return fmt.Errorf("runtime archive entry size mismatch: %s", header.Name)
			}
			extracted += written
		}
	}
	return nil
}

func secureDownloadURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && parsed.Host != "" && parsed.User == nil
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
