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

const (
	MarkerName        = ".contextbridge-pack.json"
	SidecarDirectory  = ".contextbridge-resources"
	maxEntriesPerRoot = 512
)

const (
	maximumManifestBytes     int64 = 64 << 10
	defaultMaxScanCandidates       = 4096
	maximumMaxScanCandidates       = 32768
	maximumReturnedPacks           = 128
)

var ErrScanCandidateLimit = errors.New("portable resource scan candidate limit reached")

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Settings struct {
	Enabled           bool
	ScanRoots         []string
	MaxPacks          int
	MaxScanCandidates int
}

type Manifest struct {
	SchemaVersion    int        `json:"schema_version"`
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Version          string     `json:"version,omitempty"`
	Kind             string     `json:"kind,omitempty"`
	RootRelativePath string     `json:"root_relative_path,omitempty"`
	Endpoints        []Endpoint `json:"endpoints,omitempty"`
}

type Endpoint struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	URL            string   `json:"url"`
	HealthPath     string   `json:"health_path,omitempty"`
	CapabilityPath string   `json:"capability_path,omitempty"`
	ExecutePath    string   `json:"execute_path,omitempty"`
	Model          string   `json:"model,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
}

type Pack struct {
	Manifest
	Path        string `json:"path"`
	MarkerPath  string `json:"marker_path"`
	Quarantined bool   `json:"quarantined,omitempty"`
	Warning     string `json:"warning,omitempty"`
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
	if settings.MaxPacks > maximumReturnedPacks {
		return nil, fmt.Errorf("portable resource max packs exceeds %d", maximumReturnedPacks)
	}
	if settings.MaxScanCandidates <= 0 {
		settings.MaxScanCandidates = defaultMaxScanCandidates
	}
	if settings.MaxScanCandidates > maximumMaxScanCandidates {
		return nil, fmt.Errorf("portable resource max scan candidates exceeds %d", maximumMaxScanCandidates)
	}
	remainingCandidates := settings.MaxScanCandidates
	takeCandidate := func() error {
		if remainingCandidates == 0 {
			return fmt.Errorf("%w (%d)", ErrScanCandidateLimit, settings.MaxScanCandidates)
		}
		remainingCandidates--
		return nil
	}
	roots := append([]string(nil), settings.ScanRoots...)
	explicit := len(roots) > 0
	if !explicit {
		roots = automaticRoots()
	}
	sort.Slice(roots, func(i, j int) bool { return strings.ToLower(roots[i]) < strings.ToLower(roots[j]) })
	seenPaths := map[string]bool{}
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
		if err := takeCandidate(); err != nil {
			return nil, err
		}
		if pack, ok := readPack(absolute); ok {
			packs = append(packs, pack)
		}
		entries, limited, readErr := readDirectoryBounded(absolute, maxEntriesPerRoot)
		if limited {
			return nil, fmt.Errorf("%w: root %s exceeds %d direct entries", ErrScanCandidateLimit, absolute, maxEntriesPerRoot)
		}
		if readErr == nil {
			for _, entry := range entries {
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				if err := takeCandidate(); err != nil {
					return nil, err
				}
				pack, ok := readPack(filepath.Join(absolute, entry.Name()))
				if !ok {
					continue
				}
				packs = append(packs, pack)
			}
		}
		// A sealed or checksum-verified resource tree must not be modified merely
		// to advertise it. Such volumes can keep bounded sidecar manifests at the
		// volume root and point to the pack with a relative path. Sidecars remain
		// passive metadata: they cannot name commands or non-loopback endpoints.
		sidecarRoot := filepath.Join(absolute, SidecarDirectory)
		sidecarInfo, sidecarErr := os.Lstat(sidecarRoot)
		if sidecarErr != nil || !sidecarInfo.IsDir() || sidecarInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		sidecars, limited, sidecarReadErr := readDirectoryBounded(sidecarRoot, maxEntriesPerRoot)
		if limited {
			return nil, fmt.Errorf("%w: sidecar directory %s exceeds %d entries", ErrScanCandidateLimit, sidecarRoot, maxEntriesPerRoot)
		}
		if sidecarReadErr != nil {
			continue
		}
		for _, entry := range sidecars {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				continue
			}
			if err := takeCandidate(); err != nil {
				return nil, err
			}
			pack, ok := readSidecarPack(absolute, filepath.Join(sidecarRoot, entry.Name()))
			if !ok {
				continue
			}
			packs = append(packs, pack)
		}
	}
	// A removable volume is untrusted input. Treat identity collisions as an
	// ambiguity, never as ordering authority: every manifest claiming the same
	// case-insensitive ID is visible for diagnostics but ineligible for routing.
	// Discovery remains bounded by the configured roots, 512 direct children
	// per root, 512 sidecars per root, MaxScanCandidates inspected candidates,
	// and MaxPacks returned diagnostics. Exhausting the global work envelope
	// fails closed rather than presenting a partial identity set as exhaustive.
	identityCounts := make(map[string]int, len(packs))
	for _, pack := range packs {
		identityCounts[strings.ToLower(pack.ID)]++
	}
	for index := range packs {
		count := identityCounts[strings.ToLower(packs[index].ID)]
		if count > 1 {
			packs[index].Quarantined = true
			packs[index].Warning = fmt.Sprintf("duplicate pack ID quarantined (%d manifests)", count)
		}
	}
	// Keep unambiguous resources ahead of diagnostics so a volume filled with
	// colliding manifests cannot crowd a valid pack out of the bounded result.
	sort.SliceStable(packs, func(i, j int) bool {
		return !packs[i].Quarantined && packs[j].Quarantined
	})
	if len(packs) > settings.MaxPacks {
		packs = packs[:settings.MaxPacks]
	}
	return packs, nil
}

// readDirectoryBounded avoids os.ReadDir's whole-directory materialization.
// One extra entry makes truncation observable so discovery never treats a
// partial untrusted identity set as complete.
func readDirectoryBounded(path string, maximum int) ([]os.DirEntry, bool, error) {
	// #nosec G304 -- path is an operator-selected or OS-enumerated root already
	// Lstat-verified as a real non-symlink directory by Discover.
	directory, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maximum + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if len(entries) > maximum {
		return entries[:maximum], true, err
	}
	return entries, false, err
}

func Resolve(packs []Pack, packID, endpointID, engineType string) (Endpoint, bool) {
	for _, pack := range packs {
		if pack.Quarantined {
			continue
		}
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
	manifest, ok := readManifest(marker)
	if !ok || manifest.RootRelativePath != "" {
		return Pack{}, false
	}
	return Pack{Manifest: manifest, Path: directory, MarkerPath: marker}, true
}

func readSidecarPack(root, marker string) (Pack, bool) {
	manifest, ok := readManifest(marker)
	if !ok || strings.TrimSpace(manifest.RootRelativePath) == "" || filepath.IsAbs(manifest.RootRelativePath) {
		return Pack{}, false
	}
	clean := filepath.Clean(manifest.RootRelativePath)
	if clean == "." || clean == ".." || filepath.Base(clean) != clean || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return Pack{}, false
	}
	target := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return Pack{}, false
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Pack{}, false
	}
	return Pack{Manifest: manifest, Path: target, MarkerPath: marker}, true
}

func readManifest(marker string) (Manifest, bool) {
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximumManifestBytes {
		return Manifest{}, false
	}
	// #nosec G304 -- marker is derived only from a validated scan root/direct
	// child or a fixed sidecar entry and is rechecked as a regular non-symlink.
	file, err := os.Open(marker)
	if err != nil {
		return Manifest{}, false
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maximumManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateManifest(manifest) != nil {
		return Manifest{}, false
	}
	return manifest, true
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != 1 || !safeID.MatchString(manifest.ID) || strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 120 || len(manifest.Version) > 60 || len(manifest.Kind) > 60 || len(manifest.RootRelativePath) > 240 || len(manifest.Endpoints) > 16 {
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
		for label, path := range map[string]string{"health": endpoint.HealthPath, "capability": endpoint.CapabilityPath, "execute": endpoint.ExecutePath} {
			if path != "" && (!strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, "?#") || len(path) > 160) {
				return fmt.Errorf("invalid %s path", label)
			}
		}
		if endpoint.Type != "service" && (endpoint.CapabilityPath != "" || endpoint.ExecutePath != "") {
			return errors.New("capability and execute paths require a service endpoint")
		}
		for _, capability := range endpoint.Capabilities {
			switch strings.ToLower(strings.TrimSpace(capability)) {
			case "text", "vision", "embedding", "tools", "audio", "image", "video", "retrieval", "typed-execution", "workflows", "artifact-lineage":
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
