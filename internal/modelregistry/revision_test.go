package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestPullResolvesAndDownloadsImmutableRevision(t *testing.T) {
	data := []byte("small deterministic gguf fixture")
	digest := sha256.Sum256(data)
	fileDigest := hex.EncodeToString(digest[:])
	revision := strings.Repeat("a", 40)
	var downloadedPath string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/models/owner/repo":
			_ = json.NewEncoder(response).Encode(map[string]interface{}{
				"sha":      revision,
				"siblings": []interface{}{map[string]interface{}{"rfilename": "model.gguf", "lfs": map[string]interface{}{"sha256": fileDigest}}},
			})
		case "/owner/repo/resolve/" + revision + "/model.gguf":
			downloadedPath = request.URL.Path
			_, _ = response.Write(data)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	previousBase := huggingFaceBaseURL
	huggingFaceBaseURL = server.URL
	defer func() { huggingFaceBaseURL = previousBase }()

	cfg := config.Config{
		Storage: config.Storage{Models: filepath.Join(t.TempDir(), "models")},
		Models:  map[string]config.Model{"fixture": {Repository: "owner/repo", File: "model.gguf"}},
	}
	entries, err := Pull(context.Background(), cfg, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Revision != revision || entries[0].SHA256 != fileDigest || downloadedPath == "" {
		t.Fatalf("pull was not bound to registry revision and digest: %#v path=%q", entries, downloadedPath)
	}
	listed := List(cfg)
	if len(listed) != 1 || listed[0].Revision != revision || listed[0].SHA256 != fileDigest || listed[0].Evidence != "verified_download_manifest" {
		t.Fatalf("verified download identity did not survive process-local pull state: %#v", listed)
	}
	if err := os.WriteFile(listed[0].Path, append(data, '!'), 0600); err != nil {
		t.Fatal(err)
	}
	listed = List(cfg)
	if len(listed) != 1 || listed[0].Evidence != "" || listed[0].Revision != "" || listed[0].SHA256 != "" {
		t.Fatalf("size-changed model retained verified installation evidence: %#v", listed)
	}
}

func TestInstallationManifestCannotOverridePinnedConfiguration(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "fixture")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("fixture")
	digest := sha256.Sum256(data)
	fileDigest := hex.EncodeToString(digest[:])
	if err := os.WriteFile(filepath.Join(directory, "model.gguf"), data, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := installationManifest{
		Schema: installationManifestSchema, Alias: "fixture", Repository: "owner/repo",
		Revision: strings.Repeat("b", 40),
		Files:    map[string]installationManifestFile{"model.gguf": {SHA256: fileDigest, Size: int64(len(data))}},
	}
	if err := writeInstallationManifest(filepath.Join(directory, installationManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Storage: config.Storage{Models: root}, Models: map[string]config.Model{
		"fixture": {Repository: "owner/repo", File: "model.gguf", Revision: strings.Repeat("a", 40), SHA256: fileDigest},
	}}
	listed := List(cfg)
	if len(listed) != 1 || listed[0].Revision != strings.Repeat("a", 40) || listed[0].Evidence != "operator_config" {
		t.Fatalf("conflicting manifest overrode pinned operator configuration: %#v", listed)
	}
}

func TestHuggingFaceMetadataRejectsMutableOrMismatchedRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]interface{}{"sha": strings.Repeat("b", 40), "siblings": []interface{}{}})
	}))
	defer server.Close()
	previousBase := huggingFaceBaseURL
	huggingFaceBaseURL = server.URL
	defer func() { huggingFaceBaseURL = previousBase }()
	if _, err := huggingFaceMetadata(context.Background(), "owner/repo", strings.Repeat("a", 40)); err == nil {
		t.Fatal("registry response silently changed an explicitly approved revision")
	}
}

func TestRegistryEgressPolicyRejectsUntrustedRedirectDestinations(t *testing.T) {
	var reached bool
	privateTarget := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		reached = true
		_, _ = response.Write([]byte(`{"sha":"` + strings.Repeat("a", 40) + `"}`))
	}))
	defer privateTarget.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, privateTarget.URL+"/metadata", http.StatusFound)
	}))
	defer entry.Close()
	previousBase := huggingFaceBaseURL
	huggingFaceBaseURL = entry.URL
	defer func() { huggingFaceBaseURL = previousBase }()
	if _, err := huggingFaceMetadata(context.Background(), "owner/repo", ""); err == nil {
		t.Fatalf("cross-origin private redirect was not rejected: %v", err)
	}
	if reached {
		t.Fatal("registry client connected to the rejected redirect destination")
	}
}

func TestRegistryEgressPolicyAllowsOnlyOfficialHTTPSOrigins(t *testing.T) {
	policy := &registryEgressTransport{}
	for _, raw := range []string{
		"https://huggingface.co/owner/repo",
		"https://cas-server.xethub.hf.co/path",
		"https://cas-server.xethub-eu.hf.co/path",
		"https://us.aws.cdn.hf.co/path",
		"https://cdn-lfs-eu-1.hf.co/path",
	} {
		destination, _ := url.Parse(raw)
		if err := policy.validateDestination(destination); err != nil {
			t.Fatalf("official registry destination %q was rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://huggingface.co/owner/repo",
		"https://huggingface.co:8443/owner/repo",
		"https://huggingface.co.evil.example/owner/repo",
		"https://127.0.0.1/metadata",
		"https://user:secret@huggingface.co/owner/repo",
	} {
		destination, _ := url.Parse(raw)
		if err := policy.validateDestination(destination); err == nil {
			t.Fatalf("unsafe registry destination %q was accepted", raw)
		}
	}
}

func TestRegistryEgressPolicyRejectsNonPublicAddresses(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "::1", "fc00::1", "fe80::1"} {
		if isPublicRegistryIP(net.ParseIP(raw)) {
			t.Fatalf("non-public address %s was accepted", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if !isPublicRegistryIP(net.ParseIP(raw)) {
			t.Fatalf("public address %s was rejected", raw)
		}
	}
}
