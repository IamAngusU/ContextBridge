package resourcepacks

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if err != nil || len(packs) != 1 || packs[0].ID != "portable.duplicate" || packs[0].Warning == "" {
		t.Fatalf("duplicate or recursion boundary failed: %#v, %v", packs, err)
	}
}
