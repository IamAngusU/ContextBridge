package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestConfigValidatesDAGContractBeforeStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Default(path); err != nil {
		t.Fatal(err)
	}
	base, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	valid := cluster.Pipeline{Mode: cluster.PipelineModeDAG, MaxParallel: 2, Steps: []cluster.PipelineStep{
		{Name: "extract", Requirements: cluster.Requirements{Task: "generation"}, Input: `${input}`},
		{Name: "summarize", DependsOn: []string{"extract"}, Requirements: cluster.Requirements{Task: "generation"}, Input: `${steps.extract.output}`},
	}}
	base.Cluster.Pipelines["dag-contract"] = valid
	if err := base.Validate(); err != nil {
		t.Fatalf("valid DAG contract was rejected: %v", err)
	}

	invalid := valid
	invalid.Steps = append([]cluster.PipelineStep(nil), valid.Steps...)
	invalid.Steps[1].Input = `${steps.undeclared.output}`
	base.Cluster.Pipelines["dag-contract"] = invalid
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "without a direct dependency") {
		t.Fatalf("invalid DAG contract error = %v", err)
	}
}
