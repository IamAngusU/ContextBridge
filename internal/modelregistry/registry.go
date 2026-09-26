package modelregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

type Entry struct {
	Name          string `json:"name"`
	Repository    string `json:"repository"`
	Revision      string `json:"revision,omitempty"`
	File          string `json:"file"`
	Path          string `json:"path"`
	Installed     bool   `json:"installed"`
	Size          int64  `json:"size_bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Kind          string `json:"kind,omitempty"`
	ProjectorFile string `json:"projector_file,omitempty"`
}

// DiscoveryEntry describes a model that can be used without requiring it to
// have been declared in ContextBridge's managed model catalog first.
type DiscoveryEntry struct {
	Name           string   `json:"name"`
	Provider       string   `json:"provider"`
	Path           string   `json:"path,omitempty"`
	Format         string   `json:"format,omitempty"`
	Size           int64    `json:"size_bytes,omitempty"`
	MemoryEstimate int64    `json:"memory_estimate_bytes,omitempty"`
	VRAM           int64    `json:"vram_bytes,omitempty"`
	Quantization   string   `json:"quantization,omitempty"`
	Parameters     string   `json:"parameters,omitempty"`
	Capabilities   []string `json:"capabilities"`
	// CapabilitiesVerified is true only when the running provider advertised
	// the capabilities itself. Name/family inference remains useful inventory
	// metadata for offline files, but must never authorize automatic routing.
	CapabilitiesVerified bool   `json:"capabilities_verified"`
	CapabilitySource     string `json:"capability_source,omitempty"`
	ContextWindowTokens  int    `json:"context_window_tokens,omitempty"`
	LimitsVerified       bool   `json:"limits_verified"`
	LimitSource          string `json:"limit_source,omitempty"`
	Installed            bool   `json:"installed"`
	Ready                bool   `json:"ready"`
	Loaded               bool   `json:"loaded"`
}

type Progress func(message string, received, total int64)

// Individual GGUF files can legitimately be very large. Keep the ceiling high
// enough for workstation/server models while still preventing an unbounded
// response or corrupt resume file from consuming the entire volume.
const maximumModelDownloadBytes int64 = 256 << 30

const maximumOllamaShowBytes int64 = 2 << 20

type ollamaCapabilityCacheEntry struct {
	capabilities        []string
	verified            bool
	source              string
	contextWindowTokens int
	expires             time.Time
}

// OllamaModelEvidence keeps modality and limit evidence separate. A daemon
// may prove vision support without publishing a context window, or vice versa;
// missing numeric fields therefore remain unknown rather than becoming zero.
type OllamaModelEvidence struct {
	Capabilities         []string
	CapabilitiesVerified bool
	CapabilitySource     string
	ContextWindowTokens  int
	LimitsVerified       bool
	LimitSource          string
}

var ollamaCapabilityCache = struct {
	sync.Mutex
	items map[string]ollamaCapabilityCacheEntry
}{items: map[string]ollamaCapabilityCacheEntry{}}

var huggingFaceBaseURL = "https://huggingface.co"

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
		entry := Entry{Name: name, Repository: model.Repository, Revision: model.Revision, File: model.File, Path: path, SHA256: model.SHA256, Kind: model.Kind, ProjectorFile: model.ProjectorFile}
		if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() {
			entry.Installed = true
			entry.Size = stat.Size()
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// Discover inventories Ollama plus user-selected model directories. It never
// modifies or loads models; "ready" means the files/API are currently usable.
func Discover(ctx context.Context, cfg config.Config, roots []string) ([]DiscoveryEntry, error) {
	result := discoverOllamaManifests()
	index := map[string]int{}
	for position, entry := range result {
		index[discoveryKey(entry)] = position
	}
	ollama, _ := cfg.Engine("ollama")
	for _, live := range discoverOllama(ctx, ollama.URL) {
		if position, ok := index[discoveryKey(live)]; ok {
			live.Path = result[position].Path
			result[position] = live
		} else {
			index[discoveryKey(live)] = len(result)
			result = append(result, live)
		}
	}
	explicitRoots := len(roots) > 0
	if len(roots) == 0 {
		roots = []string{cfg.Storage.Models}
	}
	seen := map[string]bool{}
	for _, entry := range result {
		seen[discoveryKey(entry)] = true
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		stat, err := os.Stat(absolute)
		if err != nil {
			if !explicitRoots && os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan model path %s: %w", absolute, err)
		}
		paths := []string{absolute}
		if stat.IsDir() {
			paths = nil
			count := 0
			err = filepath.WalkDir(absolute, func(path string, item os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				count++
				if count > 50000 {
					return errors.New("model scan exceeds 50000 filesystem entries")
				}
				if item.Type().IsRegular() && supportedModelFile(path) {
					paths = append(paths, path)
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("scan model path %s: %w", absolute, err)
			}
		}
		for _, path := range paths {
			if !supportedModelFile(path) {
				continue
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			entry := localDiscovery(path, info.Size())
			key := discoveryKey(entry)
			if !seen[key] {
				seen[key] = true
				result = append(result, entry)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func discoverOllama(ctx context.Context, base string) []DiscoveryEntry {
	if strings.TrimSpace(base) == "" {
		return nil
	}
	client := &http.Client{Timeout: 3 * time.Second}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/tags", nil)
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Models []struct {
			Name         string   `json:"name"`
			Digest       string   `json:"digest"`
			Size         int64    `json:"size"`
			Capabilities []string `json:"capabilities"`
			Details      struct {
				ParameterSize string   `json:"parameter_size"`
				Quantization  string   `json:"quantization_level"`
				Family        string   `json:"family"`
				Families      []string `json:"families"`
			} `json:"details"`
		} `json:"models"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&payload) != nil {
		return nil
	}
	loaded := map[string]int64{}
	request, _ = http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/ps", nil)
	if response, err := client.Do(request); err == nil {
		var running struct {
			Models []struct {
				Name     string `json:"name"`
				SizeVRAM int64  `json:"size_vram"`
			} `json:"models"`
		}
		if response.StatusCode == http.StatusOK {
			_ = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&running)
		}
		response.Body.Close()
		for _, model := range running.Models {
			loaded[strings.ToLower(model.Name)] = model.SizeVRAM
		}
	}
	result := make([]DiscoveryEntry, 0, min(len(payload.Models), 256))
	metadataContext, cancelMetadata := context.WithTimeout(ctx, 2*time.Second)
	defer cancelMetadata()
	for index, model := range payload.Models {
		if index >= 256 {
			break
		}
		vram, isLoaded := loaded[strings.ToLower(model.Name)]
		hint := model.Name + " " + model.Details.Family + " " + strings.Join(model.Details.Families, " ")
		evidence := ResolveOllamaModelEvidence(metadataContext, client, base, model.Name, model.Digest, model.Capabilities, hint)
		result = append(result, DiscoveryEntry{Name: model.Name, Provider: "ollama", Format: "ollama", Size: model.Size, MemoryEstimate: memoryEstimate(model.Size), VRAM: vram, Quantization: model.Details.Quantization, Parameters: model.Details.ParameterSize, Capabilities: evidence.Capabilities, CapabilitiesVerified: evidence.CapabilitiesVerified, CapabilitySource: evidence.CapabilitySource, ContextWindowTokens: evidence.ContextWindowTokens, LimitsVerified: evidence.LimitsVerified, LimitSource: evidence.LimitSource, Installed: true, Ready: true, Loaded: isLoaded})
	}
	return result
}

func discoverOllamaManifests() []DiscoveryEntry {
	root := strings.TrimSpace(os.Getenv("OLLAMA_MODELS"))
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".ollama", "models")
		}
	}
	manifestRoot := filepath.Join(root, "manifests")
	manifestDirectory, err := os.OpenRoot(manifestRoot)
	if err != nil {
		return nil
	}
	defer manifestDirectory.Close()
	blobDirectory, _ := os.OpenRoot(filepath.Join(root, "blobs"))
	if blobDirectory != nil {
		defer blobDirectory.Close()
	}
	var result []DiscoveryEntry
	count := 0
	_ = fs.WalkDir(manifestDirectory.FS(), ".", func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil || item.IsDir() || item.Type()&os.ModeSymlink != 0 {
			return nil
		}
		count++
		if count > 10000 {
			return fs.SkipAll
		}
		raw, err := readRootFileBounded(manifestDirectory, path, 1<<20)
		if err != nil {
			return nil
		}
		var manifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				MediaType string `json:"mediaType"`
				Size      int64  `json:"size"`
			} `json:"layers"`
		}
		if json.Unmarshal(raw, &manifest) != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		if len(parts) < 4 {
			return nil
		}
		tag, model, namespace := parts[len(parts)-1], parts[len(parts)-2], parts[len(parts)-3]
		name := model + ":" + tag
		if namespace != "library" {
			name = namespace + "/" + name
		}
		size := int64(0)
		for _, layer := range manifest.Layers {
			if strings.Contains(layer.MediaType, ".model") || strings.Contains(layer.MediaType, ".tensor") {
				if layer.Size <= 0 {
					continue
				}
				if size > math.MaxInt64-layer.Size {
					size = math.MaxInt64
					break
				}
				size += layer.Size
			}
		}
		var metadata struct {
			Format       string   `json:"model_format"`
			Family       string   `json:"model_family"`
			Families     []string `json:"model_families"`
			Parameters   string   `json:"model_type"`
			Quantization string   `json:"file_type"`
		}
		if digest := strings.TrimPrefix(manifest.Config.Digest, "sha256:"); blobDirectory != nil && len(digest) == 64 {
			if decoded, decodeErr := hex.DecodeString(digest); decodeErr == nil && len(decoded) == sha256.Size {
				configRaw, readErr := readRootFileBounded(blobDirectory, "sha256-"+digest, 1<<20)
				if readErr == nil {
					_ = json.Unmarshal(configRaw, &metadata)
				}
			}
		}
		hints := name + " " + metadata.Family + " " + strings.Join(metadata.Families, " ")
		result = append(result, DiscoveryEntry{Name: name, Provider: "ollama", Path: filepath.Join(manifestRoot, filepath.FromSlash(path)), Format: metadata.Format, Size: size, MemoryEstimate: memoryEstimate(size), Quantization: metadata.Quantization, Parameters: metadata.Parameters, Capabilities: modelCapabilities(hints), CapabilitySource: "name_inference", Installed: true})
		return nil
	})
	return result
}

func readRootFileBounded(root *os.Root, name string, maximum int64) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, errors.New("file is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("file exceeds bounded read limit")
	}
	return raw, nil
}

func discoveryKey(entry DiscoveryEntry) string {
	if entry.Provider == "ollama" {
		return strings.ToLower(entry.Provider + "\x00" + entry.Name)
	}
	return strings.ToLower(entry.Provider + "\x00" + entry.Name + "\x00" + entry.Path)
}

func supportedModelFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gguf", ".onnx", ".safetensors":
		return true
	default:
		return false
	}
}

func localDiscovery(path string, size int64) DiscoveryEntry {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	hints := name + " " + filepath.Dir(path)
	return DiscoveryEntry{Name: name, Provider: "local-file", Path: path, Format: format, Size: size, MemoryEstimate: memoryEstimate(size), Quantization: quantization(hints), Parameters: parameterSize(hints), Capabilities: modelCapabilities(hints), CapabilitySource: "name_inference", Installed: true, Ready: true}
}

func memoryEstimate(size int64) int64 {
	if size <= 0 {
		return 0
	}
	if size > math.MaxInt64-size/5 {
		return math.MaxInt64
	}
	return size + size/5
}

func quantization(name string) string {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"Q2_K", "Q3_K_S", "Q3_K_M", "Q3_K_L", "Q4_0", "Q4_1", "Q4_K_S", "Q4_K_M", "Q5_0", "Q5_1", "Q5_K_S", "Q5_K_M", "Q6_K", "Q8_0", "IQ2", "IQ3", "IQ4", "FP16", "BF16", "F16"} {
		if strings.Contains(upper, marker) {
			return marker
		}
	}
	return ""
}

func parameterSize(name string) string {
	lower := strings.ToLower(name)
	for _, suffix := range []string{"0.5b", "1b", "1.5b", "2b", "3b", "4b", "7b", "8b", "9b", "12b", "13b", "14b", "27b", "30b", "32b", "70b", "72b", "110b", "405b"} {
		if strings.Contains(lower, suffix) {
			return strings.ToUpper(suffix)
		}
	}
	return ""
}

func modelCapabilities(name string) []string {
	lower := strings.ToLower(name)
	result := []string{"text"}
	if strings.Contains(lower, "nuextract") {
		return []string{"text", "extraction", "vision"}
	}
	if strings.Contains(lower, "embed") || strings.Contains(lower, "jina") || strings.Contains(lower, "nomic") || strings.Contains(lower, "bge") {
		return []string{"embedding"}
	}
	if strings.Contains(lower, "vision") || strings.Contains(lower, "llava") || strings.Contains(lower, "moondream") || strings.Contains(lower, "gemma3") || strings.Contains(lower, "-vl") || strings.Contains(lower, "_vl") || strings.Contains(lower, "qwen25vl") || strings.Contains(lower, "qwen3vl") {
		result = append(result, "vision")
	}
	return result
}

// OllamaCapabilities converts Ollama's advertised runtime capabilities into
// ContextBridge modalities. Older Ollama releases did not include the field,
// so name/family inference remains a compatibility fallback only.
func OllamaCapabilities(advertised []string, hint string) []string {
	seen := map[string]bool{}
	hasAdvertised := false
	for _, capability := range advertised {
		normalized := strings.ToLower(strings.TrimSpace(capability))
		if normalized == "" {
			continue
		}
		hasAdvertised = true
		switch normalized {
		case "completion", "generate", "generation", "insert", "thinking", "tools":
			seen["text"] = true
		case "vision":
			seen["vision"] = true
		case "image", "images", "image_generation":
			// Ollama's image capability means image generation, not image
			// understanding. Preserve the distinction for inventory without
			// advertising it as a currently executable worker task.
			seen["image_generation"] = true
		case "embedding", "embeddings", "embed":
			seen["embedding"] = true
		}
	}
	if !hasAdvertised {
		return modelCapabilities(hint)
	}
	result := make([]string, 0, len(seen))
	for _, capability := range []string{"text", "vision", "embedding", "image_generation"} {
		if seen[capability] {
			result = append(result, capability)
		}
	}
	return result
}

// ResolveOllamaCapabilities returns display inventory. Callers making an
// automatic execution decision must use ResolveOllamaCapabilityEvidence and
// require verified=true. This wrapper deliberately preserves inferred labels
// for older daemons and offline inventory without turning those labels into a
// routing authorization.
func ResolveOllamaCapabilities(ctx context.Context, client *http.Client, base, name, digest string, tagCapabilities []string, hint string) []string {
	capabilities, _, _ := ResolveOllamaCapabilityEvidence(ctx, client, base, name, digest, tagCapabilities, hint)
	return capabilities
}

// ResolveOllamaCapabilityEvidence reads the authoritative top-level
// capabilities from POST /api/show. Some compatible runtimes also advertise
// the same field on /api/tags; that is provider evidence as well. Results are
// bounded and cached by daemon plus immutable model digest (or name for older
// servers), so a frequent status refresh does not repeatedly inspect every
// installed model. Name/family inference is returned only as unverified
// inventory metadata and must never make an automatic model eligible.
func ResolveOllamaCapabilityEvidence(ctx context.Context, client *http.Client, base, name, digest string, tagCapabilities []string, hint string) ([]string, bool, string) {
	evidence := ResolveOllamaModelEvidence(ctx, client, base, name, digest, tagCapabilities, hint)
	return evidence.Capabilities, evidence.CapabilitiesVerified, evidence.CapabilitySource
}

// ResolveOllamaModelEvidence returns provider-backed modalities plus any
// bounded numeric limits published by /api/show.
func ResolveOllamaModelEvidence(ctx context.Context, client *http.Client, base, name, digest string, tagCapabilities []string, hint string) OllamaModelEvidence {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	name = strings.TrimSpace(name)
	cacheIdentity := strings.TrimSpace(digest)
	if cacheIdentity == "" {
		cacheIdentity = strings.ToLower(name)
	}
	cacheKey := base + "|" + cacheIdentity
	now := time.Now()
	ollamaCapabilityCache.Lock()
	if cached, ok := ollamaCapabilityCache.items[cacheKey]; ok && now.Before(cached.expires) {
		result := OllamaModelEvidence{Capabilities: append([]string(nil), cached.capabilities...), CapabilitiesVerified: cached.verified, CapabilitySource: cached.source, ContextWindowTokens: cached.contextWindowTokens}
		if cached.contextWindowTokens > 0 {
			result.LimitsVerified = true
			result.LimitSource = "ollama_show"
		}
		ollamaCapabilityCache.Unlock()
		return result
	}
	ollamaCapabilityCache.Unlock()

	capabilities, contextWindowTokens, authoritative := fetchOllamaShowEvidence(ctx, client, base, name)
	source := "ollama_show"
	ttl := 5 * time.Minute
	if !authoritative {
		ttl = 30 * time.Second
		if len(tagCapabilities) > 0 {
			capabilities = OllamaCapabilities(tagCapabilities, hint)
			authoritative = true
			source = "ollama_tags"
		} else {
			capabilities = modelCapabilities(hint)
			source = "name_inference"
		}
	}
	ollamaCapabilityCache.Lock()
	if len(ollamaCapabilityCache.items) >= 1024 {
		for key, item := range ollamaCapabilityCache.items {
			if !now.Before(item.expires) {
				delete(ollamaCapabilityCache.items, key)
			}
		}
		if len(ollamaCapabilityCache.items) >= 1024 {
			ollamaCapabilityCache.items = map[string]ollamaCapabilityCacheEntry{}
		}
	}
	ollamaCapabilityCache.items[cacheKey] = ollamaCapabilityCacheEntry{capabilities: append([]string(nil), capabilities...), verified: authoritative, source: source, contextWindowTokens: contextWindowTokens, expires: now.Add(ttl)}
	ollamaCapabilityCache.Unlock()
	result := OllamaModelEvidence{Capabilities: capabilities, CapabilitiesVerified: authoritative, CapabilitySource: source, ContextWindowTokens: contextWindowTokens}
	if contextWindowTokens > 0 {
		result.LimitsVerified = true
		result.LimitSource = "ollama_show"
	}
	return result
}

func fetchOllamaShowEvidence(ctx context.Context, client *http.Client, base, name string) ([]string, int, bool) {
	if base == "" || name == "" || client == nil {
		return nil, 0, false
	}
	body, err := json.Marshal(map[string]interface{}{"model": name, "verbose": false})
	if err != nil {
		return nil, 0, false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil, 0, false
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return nil, 0, false
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumOllamaShowBytes+1))
	if err != nil || int64(len(raw)) > maximumOllamaShowBytes {
		return nil, 0, false
	}
	var payload struct {
		Capabilities []string               `json:"capabilities"`
		ModelInfo    map[string]interface{} `json:"model_info"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil, 0, false
	}
	contextWindowTokens := 0
	for key, value := range payload.ModelInfo {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "context_length" && !strings.HasSuffix(key, ".context_length") {
			continue
		}
		candidate := 0
		switch typed := value.(type) {
		case float64:
			if typed > 0 && typed <= 10_000_000 && typed == math.Trunc(typed) {
				candidate = int(typed)
			}
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil && parsed > 0 && parsed <= 10_000_000 {
				candidate = parsed
			}
		}
		if candidate > contextWindowTokens {
			contextWindowTokens = candidate
		}
	}
	if len(payload.Capabilities) == 0 {
		return nil, contextWindowTokens, false
	}
	return OllamaCapabilities(payload.Capabilities, ""), contextWindowTokens, true
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
	metadata, err := huggingFaceMetadata(ctx, model.Repository, model.Revision)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve immutable model revision: %w", err)
	}
	for _, file := range files {
		expected := model.SHA256
		if file != model.File {
			expected = ""
		}
		if expected == "" {
			expected = metadata.Files[file]
			if expected == "" {
				return nil, fmt.Errorf("cannot verify %s: file was not found with LFS SHA256 metadata in %s at %s", file, model.Repository, metadata.Revision)
			}
		}
		path := filepath.Join(dir, filepath.Base(file))
		url := strings.TrimRight(huggingFaceBaseURL, "/") + "/" + strings.Trim(model.Repository, "/") + "/resolve/" + metadata.Revision + "/" + file
		if err := download(ctx, url, path, expected, progress); err != nil {
			return nil, err
		}
		stat, _ := os.Stat(path)
		entries = append(entries, Entry{Name: alias, Repository: model.Repository, Revision: metadata.Revision, File: file, Path: path, Installed: true, Size: stat.Size(), SHA256: expected, Kind: model.Kind})
	}
	return entries, nil
}

type huggingFaceModelMetadata struct {
	Revision string
	Files    map[string]string
}

func huggingFaceMetadata(ctx context.Context, repository, requestedRevision string) (huggingFaceModelMetadata, error) {
	requestedRevision = strings.ToLower(strings.TrimSpace(requestedRevision))
	endpoint := strings.TrimRight(huggingFaceBaseURL, "/") + "/api/models/" + strings.Trim(repository, "/")
	if requestedRevision != "" {
		endpoint += "/revision/" + requestedRevision
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?blobs=true", nil)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return huggingFaceModelMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return huggingFaceModelMetadata{}, fmt.Errorf("model registry returned %s", resp.Status)
	}
	var model struct {
		SHA      string `json:"sha"`
		Siblings []struct {
			Filename string `json:"rfilename"`
			LFS      struct {
				SHA256 string `json:"sha256"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&model) != nil {
		return huggingFaceModelMetadata{}, errors.New("invalid model registry response")
	}
	revision := strings.ToLower(strings.TrimSpace(model.SHA))
	if requestedRevision != "" && revision != requestedRevision {
		return huggingFaceModelMetadata{}, errors.New("model registry returned a different revision than requested")
	}
	if len(revision) != 40 && len(revision) != 64 {
		return huggingFaceModelMetadata{}, errors.New("model registry did not return an immutable commit revision")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return huggingFaceModelMetadata{}, errors.New("model registry returned a malformed commit revision")
	}
	metadata := huggingFaceModelMetadata{Revision: revision, Files: map[string]string{}}
	for _, file := range model.Siblings {
		sha := strings.ToLower(strings.TrimSpace(file.LFS.SHA256))
		if len(sha) == 64 {
			if _, err := hex.DecodeString(sha); err == nil {
				metadata.Files[file.Filename] = sha
			}
		}
	}
	return metadata, nil
}

func download(ctx context.Context, url, target, expected string, progress Progress) error {
	return downloadAttempt(ctx, url, target, expected, progress, true)
}

type partialDownloadMetadata struct {
	URL      string `json:"url"`
	Expected string `json:"expected_sha256"`
}

func downloadAttempt(ctx context.Context, url, target, expected string, progress Progress, allowFreshRestart bool) error {
	partial := target + ".partial"
	metadataPath := partial + ".json"
	expected = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(expected), "sha256:"))
	var offset int64
	metadataReady := false
	if stat, err := os.Stat(partial); err == nil {
		metadata, metadataErr := readPartialDownloadMetadata(metadataPath)
		if metadataErr != nil || metadata.URL != url || metadata.Expected != expected {
			_ = os.Remove(partial)
			_ = os.Remove(metadataPath)
		} else {
			metadataReady = true
			offset = stat.Size()
			if err := validateModelDownloadWindow(offset, -1); err != nil {
				return fmt.Errorf("partial model exceeds %d GiB download limit", maximumModelDownloadBytes>>30)
			}
		}
	} else if os.IsNotExist(err) {
		_ = os.Remove(metadataPath)
	} else {
		return err
	}
	if !metadataReady {
		if err := writePartialDownloadMetadata(metadataPath, partialDownloadMetadata{URL: url, Expected: expected}); err != nil {
			return err
		}
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
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 {
		if expected != "" {
			actual, hashErr := fileSHA(partial)
			if hashErr == nil && strings.EqualFold(actual, expected) {
				if err := os.Rename(partial, target); err != nil {
					return err
				}
				_ = os.Remove(metadataPath)
				progress("Installed "+filepath.Base(target), offset, offset)
				return nil
			}
		}
		_ = os.Remove(partial)
		_ = os.Remove(metadataPath)
		if allowFreshRestart {
			_ = resp.Body.Close()
			return downloadAttempt(ctx, url, target, expected, progress, false)
		}
		return errors.New("resumable model download was rejected after a clean restart")
	}
	if resp.StatusCode == http.StatusOK && offset > 0 {
		offset = 0
		_ = os.Remove(partial)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	if resp.StatusCode == http.StatusPartialContent {
		if err := validateDownloadContentRange(resp.Header.Get("Content-Range"), offset, resp.ContentLength); err != nil {
			return err
		}
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
		if err := validateModelDownloadWindow(offset, total); err != nil {
			file.Close()
			return err
		}
		total += offset
	}
	progress("Downloading "+filepath.Base(target), offset, total)
	buffer := make([]byte, 256<<10)
	received := offset
	last := time.Now()
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if int64(n) > maximumModelDownloadBytes-received {
				file.Close()
				return fmt.Errorf("model download exceeds %d GiB limit", maximumModelDownloadBytes>>30)
			}
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
			_ = os.Remove(partial)
			_ = os.Remove(metadataPath)
			return fmt.Errorf("SHA256 mismatch for %s", filepath.Base(target))
		}
	}
	if err := os.Rename(partial, target); err != nil {
		return err
	}
	_ = os.Remove(metadataPath)
	progress("Installed "+filepath.Base(target), received, total)
	return nil
}

func readPartialDownloadMetadata(path string) (partialDownloadMetadata, error) {
	var metadata partialDownloadMetadata
	raw, err := os.ReadFile(path)
	if err != nil {
		return metadata, err
	}
	if len(raw) > 16<<10 || json.Unmarshal(raw, &metadata) != nil || metadata.URL == "" {
		return partialDownloadMetadata{}, errors.New("invalid partial model metadata")
	}
	return metadata, nil
}

func writePartialDownloadMetadata(path string, metadata partialDownloadMetadata) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func validateDownloadContentRange(value string, offset, responseBytes int64) error {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return errors.New("resumable model response has an invalid Content-Range")
	}
	rangeAndTotal := strings.Split(fields[1], "/")
	if len(rangeAndTotal) != 2 {
		return errors.New("resumable model response has an invalid Content-Range")
	}
	bounds := strings.Split(rangeAndTotal[0], "-")
	if len(bounds) != 2 {
		return errors.New("resumable model response has an invalid Content-Range")
	}
	start, startErr := strconv.ParseInt(bounds[0], 10, 64)
	end, endErr := strconv.ParseInt(bounds[1], 10, 64)
	if startErr != nil || endErr != nil || start != offset || end < start {
		return errors.New("resumable model response does not start at the requested offset")
	}
	if responseBytes >= 0 && end-start+1 != responseBytes {
		return errors.New("resumable model response length does not match Content-Range")
	}
	if rangeAndTotal[1] != "*" {
		total, err := strconv.ParseInt(rangeAndTotal[1], 10, 64)
		if err != nil || total <= end || total > maximumModelDownloadBytes {
			return errors.New("resumable model response has an invalid total length")
		}
	}
	return nil
}

func validateModelDownloadWindow(offset, responseBytes int64) error {
	if offset < 0 || offset > maximumModelDownloadBytes {
		return fmt.Errorf("model download offset exceeds %d GiB limit", maximumModelDownloadBytes>>30)
	}
	if responseBytes > 0 && responseBytes > maximumModelDownloadBytes-offset {
		return fmt.Errorf("model download exceeds %d GiB limit", maximumModelDownloadBytes>>30)
	}
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
