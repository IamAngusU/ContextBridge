package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

type Entry struct {
	Name          string `json:"name"`
	Repository    string `json:"repository"`
	File          string `json:"file"`
	Path          string `json:"path"`
	Installed     bool   `json:"installed"`
	Size          int64  `json:"size_bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Kind          string `json:"kind,omitempty"`
	ProjectorFile string `json:"projector_file,omitempty"`
}

type Progress func(message string, received, total int64)

func Builtin(name string) (config.Model, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "numind/nuextract3", "nuextract3":
		return config.Model{Repository: "numind/NuExtract3-GGUF", File: "NuExtract3-Q4_K_M.gguf", ProjectorFile: "mmproj-NuExtract3-BF16.gguf", Kind: "extraction"}, true
	case "jinaai/jina-embeddings-v4-text-retrieval-gguf", "jina-v4-retrieval", "jina-v4":
		return config.Model{Repository: "jinaai/jina-embeddings-v4-text-retrieval-GGUF", File: "jina-embeddings-v4-text-retrieval-Q4_K_M.gguf", Kind: "embedding", QueryPrefix: "Query: ", PassagePrefix: "Passage: ", Dimensions: 2048}, true
	default:
		return config.Model{}, false
	}
}

func List(cfg config.Config) []Entry {
	entries := make([]Entry, 0, len(cfg.Models))
	for name, model := range cfg.Models {
		path := filepath.Join(cfg.Storage.Models, name, model.File)
		entry := Entry{Name: name, Repository: model.Repository, File: model.File, Path: path, SHA256: model.SHA256, Kind: model.Kind, ProjectorFile: model.ProjectorFile}
		if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() {
			entry.Installed = true
			entry.Size = stat.Size()
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func Path(cfg config.Config, alias string) (string, error) {
	model, ok := cfg.Models[alias]
	if !ok {
		return "", fmt.Errorf("unknown model %s", alias)
	}
	path := filepath.Join(cfg.Storage.Models, alias, model.File)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("model %s is not installed; run contextbridge pull %s", alias, alias)
	}
	return path, nil
}

func Pull(ctx context.Context, cfg config.Config, alias string, progress Progress) ([]Entry, error) {
	model, ok := cfg.Models[alias]
	if !ok {
		model, ok = Builtin(alias)
	}
	if !ok {
		return nil, fmt.Errorf("unknown model %s", alias)
	}
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	dir := filepath.Join(cfg.Storage.Models, safeName(alias))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	files := []string{model.File}
	if model.ProjectorFile != "" {
		files = append(files, model.ProjectorFile)
	}
	entries := make([]Entry, 0, len(files))
	for _, file := range files {
		expected := model.SHA256
		if file != model.File {
			expected = ""
		}
		if expected == "" {
			var err error
			expected, err = huggingFaceSHA(ctx, model.Repository, file)
			if err != nil {
				return nil, fmt.Errorf("cannot verify %s: %w", file, err)
			}
		}
		path := filepath.Join(dir, filepath.Base(file))
		url := "https://huggingface.co/" + strings.Trim(model.Repository, "/") + "/resolve/main/" + file
		if err := download(ctx, url, path, expected, progress); err != nil {
			return nil, err
		}
		stat, _ := os.Stat(path)
		entries = append(entries, Entry{Name: alias, Repository: model.Repository, File: file, Path: path, Installed: true, Size: stat.Size(), SHA256: expected, Kind: model.Kind})
	}
	return entries, nil
}

func huggingFaceSHA(ctx context.Context, repository, filename string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://huggingface.co/api/models/"+strings.Trim(repository, "/")+"?blobs=true", nil)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("model registry returned %s", resp.Status)
	}
	var model struct {
		Siblings []struct {
			Filename string `json:"rfilename"`
			LFS      struct {
				SHA256 string `json:"sha256"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&model) != nil {
		return "", errors.New("invalid model registry response")
	}
	for _, file := range model.Siblings {
		if file.Filename == filename {
			sha := strings.ToLower(file.LFS.SHA256)
			if len(sha) != 64 {
				return "", fmt.Errorf("%s has no LFS SHA256 metadata", filename)
			}
			return sha, nil
		}
	}
	return "", fmt.Errorf("file %s was not found in %s", filename, repository)
}

func download(ctx context.Context, url, target, expected string, progress Progress) error {
	partial := target + ".partial"
	var offset int64
	if stat, err := os.Stat(partial); err == nil {
		offset = stat.Size()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && offset > 0 {
		offset = 0
		os.Remove(partial)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partial, flags, 0600)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	if total > 0 {
		total += offset
	}
	progress("Downloading "+filepath.Base(target), offset, total)
	buffer := make([]byte, 256<<10)
	received := offset
	last := time.Now()
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, err := file.Write(buffer[:n]); err != nil {
				file.Close()
				return err
			}
			received += int64(n)
			if time.Since(last) > 500*time.Millisecond {
				progress("Downloading "+filepath.Base(target), received, total)
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
	progress("Verifying "+filepath.Base(target), received, total)
	if expected != "" {
		actual, err := fileSHA(partial)
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, strings.TrimPrefix(expected, "sha256:")) {
			return fmt.Errorf("SHA256 mismatch for %s", filepath.Base(target))
		}
	}
	if err := os.Rename(partial, target); err != nil {
		return err
	}
	progress("Installed "+filepath.Base(target), received, total)
	return nil
}

func fileSHA(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(value)
	if value == "" {
		return "model"
	}
	return value
}
