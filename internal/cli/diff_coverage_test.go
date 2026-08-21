package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file cover the branches the diff-coverage gate requires:
// the workflows CLI dispatch error paths.

func TestExecuteWorkflowsSubcommand(t *testing.T) {
	err := Execute([]string{"workflows"})
	if err == nil || !strings.Contains(err.Error(), "expected list, show, validate, or explain") {
		t.Fatalf("Execute(workflows) = %v, want a usage error", err)
	}
}

// --- workflows CLI error branches ---

func TestRunWorkflowsNoArgs(t *testing.T) {
	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{}, &out, &errOut); err == nil {
		t.Fatal("workflows with no subcommand must error")
	}
}

// TestRunWorkflowsFallbackWorkspace covers the "." workspace fallback in
// list and validate when no --workspace flag is given.
func TestRunWorkflowsFallbackWorkspace(t *testing.T) {
	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"list"}, &out, &errOut); err != nil {
		t.Fatalf("list with default workspace: %v", err)
	}
	out.Reset()
	errOut.Reset()
	if err := runWorkflowsWithIO([]string{"validate"}, &out, &errOut); err != nil {
		t.Fatalf("validate with default workspace: %v", err)
	}
}

func TestRunWorkflowsListErrorWithFileWorkspace(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runWorkflowsWithIO([]string{"list", "--workspace", file}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "workflows list:") {
		t.Fatalf("list with file workspace = %v, want a list error", err)
	}
}

func TestRunWorkflowsShowErrorBranches(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowsWithIO([]string{"show", "x", "--workspace", file}, &out, &errOut); err == nil {
		t.Fatal("show with file workspace must error")
	}
	// A parse error in the discovered workflow.
	root := t.TempDir()
	writeWorkflowFixture(t, root, "broken", "version = 1\nname = [not a string")
	if err := runWorkflowsWithIO([]string{"show", "broken", "--workspace", root}, &out, &errOut); err == nil {
		t.Fatal("show with invalid TOML must error")
	}
	// A compile error: initial step that does not exist.
	root2 := t.TempDir()
	writeWorkflowFixture(t, root2, "bad", "version = 1\nname = \"bad\"\ninitial_step = \"missing\"\n\n[[steps]]\nid = \"plan\"\nkind = \"agent\"\nagent = \"planner\"\n")
	if err := runWorkflowsWithIO([]string{"show", "bad", "--workspace", root2}, &out, &errOut); err == nil {
		t.Fatal("show with a compile error must error")
	}
}

func TestRunWorkflowsValidateErrorBranches(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowsWithIO([]string{"validate", "--workspace", file}, &out, &errOut); err == nil {
		t.Fatal("validate with file workspace must error")
	}
	// An invalid workflow sets hasError and the run reports it.
	root := t.TempDir()
	writeWorkflowFixture(t, root, "bad", "version = 1\nname = \"bad\"\ninitial_step = \"missing\"\n\n[[steps]]\nid = \"plan\"\nkind = \"agent\"\nagent = \"planner\"\n")
	err := runWorkflowsWithIO([]string{"validate", "--workspace", root}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "one or more workflows are invalid") {
		t.Fatalf("validate with invalid workflow = %v", err)
	}
}

func TestRunWorkflowsExplainErrorBranches(t *testing.T) {
	var out, errOut strings.Builder
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowsWithIO([]string{"explain", "x", "--workspace", file}, &out, &errOut); err == nil {
		t.Fatal("explain with file workspace must error")
	}
	root := t.TempDir()
	writeWorkflowFixture(t, root, "bad", "version = 1\nname = \"bad\"\ninitial_step = \"missing\"\n\n[[steps]]\nid = \"plan\"\nkind = \"agent\"\nagent = \"planner\"\n")
	if err := runWorkflowsWithIO([]string{"explain", "bad", "--workspace", root}, &out, &errOut); err == nil {
		t.Fatal("explain with a compile error must error")
	}
}

// TestWorkflowsExplainOutputSchemaReference covers the schema: reference branch
// of buildExplainView (workflows_command.go).
func TestWorkflowsExplainOutputSchemaReference(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, "w", `version = 1
name = "w"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "agent"
agent = "planner"
template = "templates/plan.md"
output_schema = "schemas/plan.json"

[[steps]]
id = "review"
kind = "evidence_gate"
verifier = "go-test"

[[transitions]]
from = "plan"
to = "review"
match = { status = "succeeded" }

[[transitions]]
from = "review"
to = "success"
match = { status = "succeeded" }
`)
	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"explain", "w", "--workspace", root}, &out, &errOut); err != nil {
		t.Fatalf("explain with output_schema step: %v", err)
	}
	if !strings.Contains(out.String(), "schema: schemas/plan.json") {
		t.Fatalf("explain output must list the schema reference, got: %s", out.String())
	}
}
