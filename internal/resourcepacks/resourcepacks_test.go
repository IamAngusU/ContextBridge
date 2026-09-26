package resourcepacks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCaseDistinctUnixRootsRemainVisibleForIdentityQuarantine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths are case-insensitive")
	}
	parent := t.TempDir()
	upper, lower := filepath.Join(parent, "Pack"), filepath.Join(parent, "pack")
	for index, root := range []string{upper, lower} {
		if err := os.Mkdir(root, 0700); err != nil {
			if index == 1 && os.IsExist(err) {
				t.Skip("test filesystem is case-insensitive")
			}
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, MarkerName), []byte(`{"schema_version":1,"id":"same-id","name":"Duplicate"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{upper, lower}, MaxPacks: 8})
	if err != nil || len(packs) != 2 {
		t.Fatalf("case-distinct roots were collapsed: packs=%#v err=%v", packs, err)
	}
	for _, pack := range packs {
		if !pack.Quarantined {
			t.Fatalf("duplicate identity was not quarantined: %#v", packs)
		}
	}
	aliases, err := Discover(Settings{Enabled: true, ScanRoots: []string{upper, filepath.Join(upper, ".")}, MaxPacks: 8})
	if err != nil || len(aliases) != 1 || aliases[0].Quarantined {
		t.Fatalf("one root alias became a false identity collision: %#v %v", aliases, err)
	}
}

func TestDiscoverIsBoundedAndResolvesStableID(t *testing.T) {
	root := t.TempDir()
	packRoot := filepath.Join(root, "changing-drive-letter-does-not-matter")
	if err := os.MkdirAll(packRoot, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"id":"example.modelkit","name":"ModelKit","version":"1","kind":"models","endpoints":[{"id":"ollama","type":"ollama","url":"http://127.0.0.1:11436","capabilities":["text","vision"]}]}`
	if err := os.WriteFile(filepath.Join(packRoot, MarkerName), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 1 || packs[0].ID != "example.modelkit" {
		t.Fatalf("unexpected discovery: %#v, %v", packs, err)
	}
	endpoint, ok := Resolve(packs, "EXAMPLE.MODELKIT", "ollama", "ollama")
	if !ok || endpoint.URL != "http://127.0.0.1:11436" {
		t.Fatalf("stable pack lookup failed: %#v, %v", endpoint, ok)
	}
}

func TestManifestCannotPublishRemoteEndpointOrCommands(t *testing.T) {
	root := t.TempDir()
	remote := `{"schema_version":1,"id":"evil.pack","name":"Evil","endpoints":[{"id":"api","type":"openai_compatible","url":"https://example.com/v1"}]}`
	if err := os.WriteFile(filepath.Join(root, MarkerName), []byte(remote), 0600); err != nil {
		t.Fatal(err)
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 0 {
		t.Fatalf("remote endpoint escaped portable boundary: %#v, %v", packs, err)
	}
	unknown := `{"schema_version":1,"id":"evil.pack","name":"Evil","command":"run-me"}`
	if err := os.WriteFile(filepath.Join(root, MarkerName), []byte(unknown), 0600); err != nil {
		t.Fatal(err)
	}
	packs, err = Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 0 {
		t.Fatalf("unknown executable field escaped strict schema: %#v, %v", packs, err)
	}
}

func TestServiceEndpointAcceptsPassiveTypedExecutionMetadata(t *testing.T) {
	root := t.TempDir()
	manifest := `{"schema_version":1,"id":"example.toolbox","name":"Toolbox","endpoints":[{"id":"control","type":"service","url":"http://127.0.0.1:4310","health_path":"/api/status","capability_path":"/api/node/capabilities","execute_path":"/api/node/execute","capabilities":["tools","typed-execution","workflows","artifact-lineage"]}]}`
	if err := os.WriteFile(filepath.Join(root, MarkerName), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 1 || len(packs[0].Endpoints) != 1 {
		t.Fatalf("service metadata was not discovered: %#v, %v", packs, err)
	}
	endpoint := packs[0].Endpoints[0]
	if endpoint.CapabilityPath != "/api/node/capabilities" || endpoint.ExecutePath != "/api/node/execute" {
		t.Fatalf("bounded passive paths were lost: %#v", endpoint)
	}
	// The paths remain metadata. Resolve never turns a service endpoint into an
	// OpenAI-compatible execution route merely because it advertises tools.
	if _, ok := Resolve(packs, "example.toolbox", "control", "openai_compatible"); ok {
		t.Fatal("service metadata widened into an incompatible execution engine")
	}

	unsafe := `{"schema_version":1,"id":"example.toolbox","name":"Toolbox","endpoints":[{"id":"control","type":"service","url":"http://127.0.0.1:4310","execute_path":"/api/../admin"}]}`
	if err := os.WriteFile(filepath.Join(root, MarkerName), []byte(unsafe), 0600); err != nil {
		t.Fatal(err)
	}
	packs, err = Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 0 {
		t.Fatalf("unsafe service path was accepted: %#v, %v", packs, err)
	}
}

func TestHotPlugAbsenceAndChangedMountPath(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "volume-a", MarkerName)
	if err := os.MkdirAll(filepath.Dir(first), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schema_version":1,"id":"portable.same-id","name":"Portable","endpoints":[{"id":"api","type":"service","url":"http://127.0.0.1:4310"}]}`)
	if err := os.WriteFile(first, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	settings := Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8}
	packs, err := Discover(settings)
	if err != nil || len(packs) != 1 {
		t.Fatalf("inserted pack missing: %#v, %v", packs, err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	packs, err = Discover(settings)
	if err != nil || len(packs) != 0 {
		t.Fatalf("removed pack remained eligible: %#v, %v", packs, err)
	}
	second := filepath.Join(root, "different-mount-name", MarkerName)
	if err := os.MkdirAll(filepath.Dir(second), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	packs, err = Discover(settings)
	if err != nil || len(packs) != 1 || packs[0].ID != "portable.same-id" || packs[0].Path != filepath.Dir(second) {
		t.Fatalf("stable identity did not survive a path change: %#v, %v", packs, err)
	}
}

func TestSidecarDiscoversSealedTreeWithoutModifyingIt(t *testing.T) {
	root := t.TempDir()
	sealed := filepath.Join(root, "sealed-arsenal")
	if err := os.MkdirAll(sealed, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(sealed, "integrity-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	sidecars := filepath.Join(root, SidecarDirectory)
	if err := os.MkdirAll(sidecars, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"id":"example.sealed","name":"Sealed","root_relative_path":"sealed-arsenal","endpoints":[{"id":"api","type":"service","url":"http://127.0.0.1:4310"}]}`
	marker := filepath.Join(sidecars, "sealed.json")
	if err := os.WriteFile(marker, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 1 || packs[0].ID != "example.sealed" || packs[0].Path != sealed || packs[0].MarkerPath != marker {
		t.Fatalf("sidecar discovery failed: %#v, %v", packs, err)
	}
	if _, err := os.Stat(filepath.Join(sealed, MarkerName)); !os.IsNotExist(err) {
		t.Fatalf("discovery modified the sealed resource tree: %v", err)
	}
	if value, err := os.ReadFile(sentinel); err != nil || string(value) != "unchanged" {
		t.Fatalf("sealed contents changed: %q, %v", value, err)
	}
}

func TestSidecarRejectsEscapesSymlinksAndMissingTargets(t *testing.T) {
	root := t.TempDir()
	sidecars := filepath.Join(root, SidecarDirectory)
	if err := os.MkdirAll(sidecars, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, target string) {
		t.Helper()
		value := `{"schema_version":1,"id":"example.` + name + `","name":"Invalid","root_relative_path":"` + target + `"}`
		if err := os.WriteFile(filepath.Join(sidecars, name+".json"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("escape", "../outside")
	write("nested", filepath.Join("inside", "nested"))
	write("missing", "not-present")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err == nil {
		write("symlink", "linked")
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 0 {
		t.Fatalf("unsafe sidecar target escaped discovery: %#v, %v", packs, err)
	}
}

func TestDiscoveryDoesNotRecurseOrAcceptDuplicateIdentity(t *testing.T) {
	root := t.TempDir()
	manifest := []byte(`{"schema_version":1,"id":"portable.duplicate","name":"Portable"}`)
	for _, directory := range []string{"first", "second"} {
		path := filepath.Join(root, directory)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, MarkerName), manifest, 0600); err != nil {
			t.Fatal(err)
		}
	}
	deep := filepath.Join(root, "not-scanned", "nested")
	if err := os.MkdirAll(deep, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, MarkerName), []byte(`{"schema_version":1,"id":"portable.deep","name":"Deep"}`), 0600); err != nil {
		t.Fatal(err)
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 8})
	if err != nil || len(packs) != 2 {
		t.Fatalf("duplicate or recursion boundary failed: %#v, %v", packs, err)
	}
	for _, pack := range packs {
		if pack.ID != "portable.duplicate" || !pack.Quarantined || pack.Warning != "duplicate pack ID quarantined (2 manifests)" {
			t.Fatalf("duplicate identity was not quarantined symmetrically: %#v", packs)
		}
	}
	if _, ok := Resolve(packs, "portable.duplicate", "anything", "service"); ok {
		t.Fatal("a quarantined duplicate identity remained routable")
	}
}

func TestDuplicateDiagnosticsCannotCrowdOutValidPack(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"00-duplicate": `{"schema_version":1,"id":"portable.duplicate","name":"Duplicate"}`,
		"01-duplicate": `{"schema_version":1,"id":"PORTABLE.DUPLICATE","name":"Duplicate"}`,
		"99-valid":     `{"schema_version":1,"id":"portable.valid","name":"Valid","endpoints":[{"id":"api","type":"service","url":"http://127.0.0.1:4310"}]}`,
	}
	for directory, manifest := range fixtures {
		path := filepath.Join(root, directory)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, MarkerName), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
	}
	packs, err := Discover(Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 1})
	if err != nil || len(packs) != 1 || packs[0].ID != "portable.valid" || packs[0].Quarantined {
		t.Fatalf("duplicate diagnostics crowded out a valid pack: %#v, %v", packs, err)
	}
	if _, ok := Resolve(packs, "portable.valid", "api", "service"); !ok {
		t.Fatal("the bounded valid pack was not routable")
	}
}

func TestDiscoveryCandidateBudgetFailsClosed(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	packs, err := Discover(Settings{
		Enabled:           true,
		ScanRoots:         []string{root},
		MaxPacks:          1,
		MaxScanCandidates: 1,
	})
	if !errors.Is(err, ErrScanCandidateLimit) {
		t.Fatalf("candidate exhaustion was not observable: packs=%#v err=%v", packs, err)
	}
	if len(packs) != 0 {
		t.Fatalf("partial discovery escaped a failed-closed scan: %#v", packs)
	}
}

func TestDiscoveryPerRootEntryLimitIsObservable(t *testing.T) {
	root := t.TempDir()
	for index := 0; index <= maxEntriesPerRoot; index++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%04d", index)), []byte("not a manifest"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	packs, err := Discover(Settings{
		Enabled:           true,
		ScanRoots:         []string{root},
		MaxPacks:          1,
		MaxScanCandidates: defaultMaxScanCandidates,
	})
	if !errors.Is(err, ErrScanCandidateLimit) || !strings.Contains(err.Error(), "direct entries") {
		t.Fatalf("per-root truncation was not reported: packs=%#v err=%v", packs, err)
	}
	if len(packs) != 0 {
		t.Fatalf("partial discovery escaped a truncated root: %#v", packs)
	}
}

func TestDiscoveryRejectsInvalidProgrammaticBounds(t *testing.T) {
	if _, err := Discover(Settings{Enabled: true, MaxPacks: maximumReturnedPacks + 1}); err == nil {
		t.Fatal("oversized return bound was accepted")
	}
	if _, err := Discover(Settings{Enabled: true, MaxPacks: 1, MaxScanCandidates: maximumMaxScanCandidates + 1}); err == nil {
		t.Fatal("oversized scan bound was accepted")
	}
}

func BenchmarkDiscoverCandidateBudget(b *testing.B) {
	for _, count := range []int{32, 128, 512} {
		b.Run(fmt.Sprintf("candidates_%d", count), func(b *testing.B) {
			root := b.TempDir()
			for index := 0; index < count; index++ {
				directory := filepath.Join(root, fmt.Sprintf("pack-%04d", index))
				if err := os.Mkdir(directory, 0700); err != nil {
					b.Fatal(err)
				}
				manifest := fmt.Sprintf(`{"schema_version":1,"id":"portable.%04d","name":"Portable"}`, index)
				if err := os.WriteFile(filepath.Join(directory, MarkerName), []byte(manifest), 0600); err != nil {
					b.Fatal(err)
				}
			}
			settings := Settings{Enabled: true, ScanRoots: []string{root}, MaxPacks: 1, MaxScanCandidates: count + 1}
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				packs, err := Discover(settings)
				if err != nil || len(packs) != 1 {
					b.Fatalf("discover = %d packs, %v", len(packs), err)
				}
			}
		})
	}
}
