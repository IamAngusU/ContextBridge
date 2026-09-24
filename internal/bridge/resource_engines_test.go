package bridge

import (
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
)

func TestPortableResourceEngineCannotInheritCredentials(t *testing.T) {
	packs := []resourcepacks.Pack{{Manifest: resourcepacks.Manifest{
		ID: "portable.test", Endpoints: []resourcepacks.Endpoint{{ID: "api", Type: "openai_compatible", URL: "http://127.0.0.1:4310"}},
	}}}
	for name, engine := range map[string]config.Engine{
		"inline":   {Type: "openai_compatible", ResourcePack: "portable.test", Endpoint: "api", APIKey: "secret"},
		"file":     {Type: "openai_compatible", ResourcePack: "portable.test", Endpoint: "api", APIKeyFile: "secret.txt"},
		"resolved": {Type: "openai_compatible", ResourcePack: "portable.test", Endpoint: "api", ResolvedAPIKey: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveResourceEngine(engine, packs); err == nil || !strings.Contains(err.Error(), "cannot inherit") {
				t.Fatalf("credential-bearing portable engine was accepted: %v", err)
			}
		})
	}
}
