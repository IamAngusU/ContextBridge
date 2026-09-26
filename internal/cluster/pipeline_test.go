package cluster

import (
	"bytes"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestPipelinePanicBoundaryPersistsAmbiguousFailure(t *testing.T) {
	var logs bytes.Buffer
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: "admin_pipeline_panic_012345678901234567890123"}, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	run := PipelineRun{ID: "run-panic", Pipeline: "test", Status: "running", CreatedAt: time.Now().UTC()}
	if err := relay.store.SavePipelineRun(run); err != nil {
		t.Fatal(err)
	}
	const secret = "PIPELINE-PANIC-VALUE-MUST-NOT-PERSIST"
	func() {
		defer relay.recoverPipelinePanic(&run)
		panic(secret)
	}()
	stored, err := relay.store.GetPipelineRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || !strings.Contains(stored.Error, "execution state is ambiguous") || strings.Contains(stored.Error, secret) {
		t.Fatalf("panic did not become a bounded terminal record: %#v", stored)
	}
	if strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), "recovered an internal panic") {
		t.Fatalf("panic log was unsafe or missing: %q", logs.String())
	}

	control := PipelineRun{ID: "run-control", Pipeline: "test", Status: "running", CreatedAt: time.Now().UTC()}
	func() {
		defer relay.recoverPipelinePanic(&control)
	}()
	if control.Status != "running" || control.Error != "" {
		t.Fatalf("non-panicking pipeline was changed: %#v", control)
	}
}

func TestDashboardHasNoRemoteRuntimeOrPersistentTokenStorage(t *testing.T) {
	value := string(dashboardHTML)
	for _, forbidden := range []string{"https://", "http://", "localStorage", "eval("} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("dashboard contains forbidden runtime dependency %s", forbidden)
		}
	}
	if !strings.Contains(value, "gpus.slice(0,8)") || !strings.Contains(value, "more GPUs") {
		t.Fatal("dashboard does not render bounded details for every reported GPU")
	}
	for _, required := range []string{"fragment.get('pair')", "requested by this link", ".pair.target"} {
		if !strings.Contains(value, required) {
			t.Fatalf("dashboard does not preserve the explicit pairing-link behavior: missing %q", required)
		}
	}
}
