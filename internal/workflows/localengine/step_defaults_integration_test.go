package localengine_test

// End-to-end integration tests for [step_defaults]: a real engine run over a
// sugared workflow (memory ledger + scripted runner + real controller), and
// the snapshot round-trip that proves resume re-desugars deterministically.
// Black-box: TOML fixtures and public APIs only, so this file compiles before
// the feature exists and fails red on the decoder's unknown-key rejection.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// sugaredTwoStepTOML mirrors the two-step workspace fixture, but both agent
// steps take kind, agent, and on_failure from [step_defaults]. Each step
// keeps only its id (plus per-step runner scripting keys by id, unchanged).
const sugaredTwoStepTOML = `version = 1
name = "two-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[step_defaults]
kind = "agent"
agent = "worker"
on_failure = "failure"

[[steps]]
id = "one"

[[steps]]
id = "two"

[[transitions]]
from = "one"
to = "two"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "two"
to = "success"
[transitions.match]
status = "succeeded"
`

// writeSugaredTwoStepWorkspace builds the same workspace shape as
// writeTwoStepWorkspace with the sugared workflow body instead.
func writeSugaredTwoStepWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root)
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfRoot, "two-step.toml"), []byte(sugaredTwoStepTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func newStepDefaultsEngine(t *testing.T) (*localengine.Engine, *agenttools.Service, workflowledger.Repository) {
	t.Helper()
	root := writeSugaredTwoStepWorkspace(t)
	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				ByStep: map[string]json.RawMessage{
					"one": json.RawMessage(`{"ok":true}`),
					"two": json.RawMessage(`{"ok":true,"done":true}`),
				},
			}
		},
	}
	return engine, mustService(t, engine, repo), repo
}

// TestIntegrationStepDefaultsRunToSuccess admits and drives a sugared
// workflow through the real engine and controller to the success terminal.
// A pass proves the desugared steps carried real agent identities and
// failure routing all the way through admission, execution, and settlement.
func TestIntegrationStepDefaultsRunToSuccess(t *testing.T) {
	engine, svc, _ := newStepDefaultsEngine(t)
	started := startTwoStep(t, svc)
	waitRun(t, engine, started.RunID)
	assertSucceededStatus(t, svc, started.RunID)
}

// TestIntegrationStepDefaultsSnapshotRoundTrip pins the resume contract:
// the admission snapshot freezes the RAW sugared TOML, and re-parsing those
// bytes (the CompileForResume path in Engine) reproduces the expanded steps
// and the admitted digest exactly.
func TestIntegrationStepDefaultsSnapshotRoundTrip(t *testing.T) {
	engine, svc, repo := newStepDefaultsEngine(t)
	started := startTwoStep(t, svc)
	waitRun(t, engine, started.RunID)

	rawSnap, err := repo.GetRunSnapshot(context.Background(), started.RunID)
	if err != nil {
		t.Fatalf("GetRunSnapshot: %v", err)
	}
	snap, err := workflowledger.UnmarshalSnapshot(rawSnap)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot: %v", err)
	}

	// The snapshot must hold the sugared source verbatim - desugar happens at
	// decode, never by rewriting the canonical artifact.
	if !strings.Contains(string(snap.DefinitionTOML), "[step_defaults]") {
		t.Fatalf("snapshot DefinitionTOML lost the [step_defaults] table:\n%s", snap.DefinitionTOML)
	}

	// Re-parse the frozen bytes twice: decode must be deterministic.
	first, _, err := definition.ParseWorkflowTOML(snap.DefinitionTOML, "two-step.toml")
	if err != nil {
		t.Fatalf("re-parse snapshot TOML: %v", err)
	}
	second, _, err := definition.ParseWorkflowTOML(snap.DefinitionTOML, "two-step.toml")
	if err != nil {
		t.Fatalf("re-parse snapshot TOML: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("re-parsing the same snapshot bytes produced different definitions")
	}

	// Both steps must come out expanded: the resume path hands these steps to
	// the controller, so an empty Agent here would wedge every resumed run.
	for _, s := range first.Steps {
		if s.Kind != "agent" || s.Agent != "worker" || s.OnFailure != "failure" {
			t.Fatalf("step %q not expanded on re-parse: %+v", s.ID, s)
		}
	}

	// The resume compile must reproduce the admitted digest, or resume would
	// reject the snapshot as drifted.
	resumed, err := compiler.CompileForResume(&first)
	if err != nil {
		t.Fatalf("CompileForResume: %v", err)
	}
	if resumed.Digest != snap.DefinitionDigest {
		t.Fatalf("resume digest %s != admitted digest %s", resumed.Digest, snap.DefinitionDigest)
	}
}
