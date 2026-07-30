package cluster

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderPipelineInputIsBoundedToDeclaredValues(t *testing.T) {
	values := map[string]json.RawMessage{"input": json.RawMessage(`{"ticket":"hello"}`), "steps.lookup.output": json.RawMessage(`{"context":"known"}`)}
	raw, err := renderPipelineInput(`{"route":"default","text":${steps.lookup.output}}`, values)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("valid template failed: %v", err)
	}
	if _, err := renderPipelineInput(`{"text":${steps.unknown.output}}`, values); err == nil {
		t.Fatal("unresolved step must fail")
	}
	if _, err := renderPipelineInput(`run $(dangerous)`, values); err == nil {
		t.Fatal("non-JSON template must fail")
	}
}

func TestDashboardHasNoRemoteRuntimeOrPersistentTokenStorage(t *testing.T) {
	value := string(dashboardHTML)
	for _, forbidden := range []string{"https://", "http://", "localStorage", "eval("} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("dashboard contains forbidden runtime dependency %s", forbidden)
		}
	}
}
