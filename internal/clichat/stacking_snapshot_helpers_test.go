package clichat

// stacking_snapshot_helpers_test.go duplicates cliworkflow's snapshot fixtures
// (workflow_resume_stack_test.go): they build admitted-run snapshots for the
// stacking drive tests that stay in cli.

import (
	"regexp"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// stackingResumeSnapshot builds the admitted run row and raw snapshot for a
// stacking workflow at the given inputs.
func stackingResumeSnapshot(t *testing.T, toml string, inputs map[string]string) (workflowledger.RunSnapshot, []byte) {
	t.Helper()
	name := workflowTOMLName(t, toml)
	wf, _, err := definition.ParseWorkflowTOML([]byte(toml), name+".toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(toml),
		DefinitionDigest: compiled.Digest,
		Inputs:           inputs,
		Agents: map[string]workflowledger.AgentSnapshot{
			"one": {Digest: "agent-one"},
			"two": {Digest: "agent-two"},
		},
	}
	raw, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		WorkflowName:   compiled.Name,
		WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(raw),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
	}
	return run, raw
}

// workflowTOMLName extracts the workflow's declared name from its TOML text
// so the snapshot filename matches the definition's in-file name.
func workflowTOMLName(t *testing.T, toml string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
	m := re.FindStringSubmatch(toml)
	if m == nil {
		t.Fatal("workflow TOML has no name")
	}
	return m[1]
}
