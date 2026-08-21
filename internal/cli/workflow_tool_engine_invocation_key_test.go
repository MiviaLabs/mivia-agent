package cli

// Regression tests for invocation_key reuse (FIX: wrong-run continuation).
//
// When invocation_key K is bound to a run of workflow A and a caller requests
// workflow B with the same key, the engine used to silently (a) return A's
// terminal result as if B succeeded, or (b) resume A while dropping
// req.Workflow/req.Inputs. Both must now fail with an explicit error naming
// the bound workflow.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// soloWorkflowDefinition is a second single-step workflow for invocation-key
// reuse tests: reusing a key bound to "two-step" with this workflow must be
// refused, not silently continued.
const soloWorkflowDefinition = `version = 1
name = "solo"
description = "A second single-step workflow."
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
template = "templates/one.md"
output_schema = "schemas/out.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`

// newTwoWorkflowFixture builds the two-step fixture plus a second workflow so
// a test can request workflow B under a key already bound to workflow A.
func newTwoWorkflowFixture(t *testing.T) (root, configPath string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root = t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	writeWorkflowDefinition(t, root, "solo", soloWorkflowDefinition)
	return root, filepath.Join(root, "config.toml")
}

// newGatedKeyFixture parks a gated (human-gate) run at waiting_approval bound
// to the given invocation key — a genuinely resumable non-terminal run — and
// adds a second workflow so a mismatched workflow can be requested under the
// same key.
func newGatedKeyFixture(t *testing.T, key string) (root, configPath string) {
	t.Helper()
	root, configPath, storePath, raw, run := newGatedApprovalWorkspace(t)
	run.RunID = ledger.InvocationRunID(key)
	run.InvocationKey = key
	seedGatedApprovalHistory(t, storePath, raw, run)
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "solo.toml"), []byte(soloWorkflowDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, configPath
}

// TestSessionEngineInvocationKeyRejectsDifferentWorkflow: a key already bound
// to a run of workflow A must refuse a start request for workflow B instead of
// returning A's terminal result as if B succeeded.
func TestSessionEngineInvocationKeyRejectsDifferentWorkflow(t *testing.T) {
	root, configPath := newTwoWorkflowFixture(t)
	e := newSessionWorkflowEngine(root, configPath)
	key := "caller-request-1"

	first, err := e.Start(context.Background(), ledger.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "compile"}, InvocationKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionEngineIdle(t, e, first.RunID)

	_, err = e.Start(context.Background(), ledger.StartRequest{
		Workflow: "solo", Inputs: map[string]any{"task": "compile"}, InvocationKey: key,
	})
	if err == nil {
		t.Fatal("start with a key bound to another workflow succeeded; want an explicit error, not a wrong-run continuation")
	}
	if !strings.Contains(err.Error(), "already bound to workflow two-step") {
		t.Fatalf("error = %v, want it to name the bound workflow", err)
	}
}

// TestSessionEngineInvocationKeyResumeRejectsDifferentWorkflow: the silent-
// resume branch must also refuse a workflow change under the same key instead
// of resuming A while dropping the requested workflow.
func TestSessionEngineInvocationKeyResumeRejectsDifferentWorkflow(t *testing.T) {
	key := "caller-request-2"
	root, configPath := newGatedKeyFixture(t, key)
	e := newSessionWorkflowEngine(root, configPath)

	_, err := e.Start(context.Background(), ledger.StartRequest{
		Workflow: "solo", Inputs: map[string]any{"task": "test"}, InvocationKey: key,
	})
	if err == nil {
		t.Fatal("resume branch accepted a workflow change under a bound key; want an explicit error")
	}
	if !strings.Contains(err.Error(), "already bound to workflow gated") {
		t.Fatalf("error = %v, want it to name the bound workflow", err)
	}
}

// TestSessionEngineInvocationKeyResumeRejectsDifferentInputs: the silent-
// resume branch must refuse a retry whose inputs differ from the bound run's
// snapshot instead of dropping them and resuming the old request.
func TestSessionEngineInvocationKeyResumeRejectsDifferentInputs(t *testing.T) {
	key := "caller-request-3"
	root, configPath := newGatedKeyFixture(t, key)
	e := newSessionWorkflowEngine(root, configPath)

	_, err := e.Start(context.Background(), ledger.StartRequest{
		Workflow: "gated", Inputs: map[string]any{"task": "different"}, InvocationKey: key,
	})
	if err == nil {
		t.Fatal("resume branch accepted changed inputs under a bound key; want an explicit error")
	}
	if !strings.Contains(err.Error(), "different inputs") {
		t.Fatalf("error = %v, want it to mention the input mismatch", err)
	}
}
