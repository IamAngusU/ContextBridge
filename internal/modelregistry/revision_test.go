package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
