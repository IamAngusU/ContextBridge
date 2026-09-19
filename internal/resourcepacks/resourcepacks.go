package resourcepacks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const MarkerName = ".contextbridge-pack.json"

const maximumManifestBytes int64 = 64 << 10

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Settings struct {
	Enabled   bool
	ScanRoots []string
	MaxPacks  int
}

type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Version       string     `json:"version,omitempty"`
	Kind          string     `json:"kind,omitempty"`
	Endpoints     []Endpoint `json:"endpoints,omitempty"`
}

type Endpoint struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	URL          string   `json:"url"`
	HealthPath   string   `json:"health_path,omitempty"`
	Model        string   `json:"model,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type Pack struct {
	Manifest
	Path       string `json:"path"`
	MarkerPath string `json:"marker_path"`
	Warning    string `json:"warning,omitempty"`
}

// Discover inspects only an exact marker at each selected root and its direct
// child directories. It does not recursively index a volume, follow links, or
// execute anything supplied by a pack. An empty root list means local
// fixed/removable volumes on Windows and common removable mount roots on Unix.
func Discover(settings Settings) ([]Pack, error) {
	if !settings.Enabled {
		return nil, nil
	}
	if settings.MaxPacks <= 0 {
		settings.MaxPacks = 32
	}
	roots := append([]string(nil), settings.ScanRoots...)
	explicit := len(roots) > 0
	if !explicit {
		roots = automaticRoots()
	}
	sort.Slice(roots, func(i, j int) bool { return strings.ToLower(roots[i]) < strings.ToLower(roots[j]) })
	seenPaths := map[string]bool{}
	seenIDs := map[string]int{}
	packs := make([]Pack, 0)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			if explicit {
				return nil, err
			}
			continue
		}
		canonical := strings.ToLower(filepath.Clean(absolute))
		if seenPaths[canonical] {
			continue
		}
		seenPaths[canonical] = true
		stat, err := os.Lstat(absolute)
		if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
			if explicit && err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("inspect portable resource root %s: %w", absolute, err)
			}
			continue
		}
		candidates := []string{absolute}
		entries, readErr := os.ReadDir(absolute)
		if readErr == nil {
			for index, entry := range entries {
				if index >= 512 {
					break
				}
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				candidates = append(candidates, filepath.Join(absolute, entry.Name()))
			}
		}
		for _, candidate := range candidates {
			pack, ok := readPack(candidate)
			if !ok {
				continue
			}
			if previous, duplicate := seenIDs[strings.ToLower(pack.ID)]; duplicate {
				if packs[previous].Warning == "" {
					packs[previous].Warning = "duplicate pack ID ignored"
				}
				continue
			}
			seenIDs[strings.ToLower(pack.ID)] = len(packs)
			packs = append(packs, pack)
			if len(packs) >= settings.MaxPacks {
				return packs, nil
			}
		}
	}
	return packs, nil
}

func Resolve(packs []Pack, packID, endpointID, engineType string) (Endpoint, bool) {
	for _, pack := range packs {
		if !strings.EqualFold(pack.ID, strings.TrimSpace(packID)) {
			continue
		}
		for _, endpoint := range pack.Endpoints {
			if strings.EqualFold(endpoint.ID, strings.TrimSpace(endpointID)) && strings.EqualFold(endpoint.Type, strings.TrimSpace(engineType)) {
				return endpoint, true
			}
		}
	}
	return Endpoint{}, false
}

func readPack(directory string) (Pack, bool) {
	marker := filepath.Join(directory, MarkerName)
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximumManifestBytes {
		return Pack{}, false
	}
	file, err := os.Open(marker)
	if err != nil {
		return Pack{}, false
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maximumManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateManifest(manifest) != nil {
		return Pack{}, false
	}
	return Pack{Manifest: manifest, Path: directory, MarkerPath: marker}, true
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != 1 || !safeID.MatchString(manifest.ID) || strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 120 || len(manifest.Version) > 60 || len(manifest.Kind) > 60 || len(manifest.Endpoints) > 16 {
		return errors.New("invalid resource-pack identity")
	}
	seen := map[string]bool{}
	for _, endpoint := range manifest.Endpoints {
		key := strings.ToLower(endpoint.ID)
		if !safeID.MatchString(endpoint.ID) || seen[key] || len(endpoint.Model) > 160 || len(endpoint.Capabilities) > 16 {
			return errors.New("invalid resource-pack endpoint")
		}
		seen[key] = true
		switch endpoint.Type {
		case "ollama", "openai_compatible", "service":
		default:
			return errors.New("unsupported resource-pack endpoint type")
		}
		if err := validateLoopbackURL(endpoint.URL); err != nil {
			return err
		}
		if endpoint.HealthPath != "" && (!strings.HasPrefix(endpoint.HealthPath, "/") || strings.Contains(endpoint.HealthPath, "..") || len(endpoint.HealthPath) > 160) {
			return errors.New("invalid health path")
		}
		for _, capability := range endpoint.Capabilities {
			switch strings.ToLower(strings.TrimSpace(capability)) {
			case "text", "vision", "embedding", "tools", "audio", "image", "video", "retrieval":
			default:
				return errors.New("unsupported endpoint capability")
			}
		}
	}
	return nil
}

func validateLoopbackURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("portable endpoints must use loopback HTTP without credentials, query, or fragment")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("portable endpoints must stay on loopback")
	}
	return nil
}
