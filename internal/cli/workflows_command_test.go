package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWorkflowFixture creates a .mivia/workflows/<name>.toml file in dir.
func writeWorkflowFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	wfDir := filepath.Join(dir, ".mivia", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pipelineExplainFixture is the TOML source for TestWorkflowsExplainValidWorkflow.
const pipelineExplainFixture = `version = 1
name = "pipeline"
description = "Plan, implement, review, deliver."
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 12000

[limits]
max_step_attempts = 12
max_duration_seconds = 7200

[[steps]]
id = "plan"
kind = "agent"
agent = "planner"
template = "templates/plan.md"
context = [{ from = "inputs.task", as = "task", max_bytes = 12000 }]
on_failure = "failure"

[[steps]]
id = "implement"
kind = "agent"
agent = "go-engineer"
on_failure = "failure"

[[steps]]
id = "review"
kind = "evidence_gate"
verifier = "go-test"

[[transitions]]
from = "plan"
to = "implement"
match = { status = "succeeded" }

[[transitions]]
from = "implement"
to = "review"
match = { status = "succeeded" }

[[transitions]]
from = "implement"
to = "implement"
match = { status = "failed" }
loop = "fix-loop"
max_iterations = 3

[[transitions]]
from = "review"
to = "success"
match = { status = "succeeded", output = { verdict = "approved" } }

[[transitions]]
from = "review"
to = "failure"
match = { status = "succeeded", output = { verdict = "rejected" } }

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"
`

// assertExplainContains checks that the explain output contains every needle.
func assertExplainContains(t *testing.T, text string, needles []string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(text, n) {
			t.Fatalf("missing %q in explain output:\n%s", n, text)
		}
	}
}

func TestWorkflowsNoSubcommandReturnsError(t *testing.T) {
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for no subcommand")
	}
	if !strings.Contains(err.Error(), "expected list, show, validate, or explain") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsUnknownSubcommandReturnsError(t *testing.T) {
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"bogus"}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsListEmptyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"list", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("list on empty workspace returned error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "No workflows found") {
		t.Fatalf("expected 'No workflows found', got: %s", text)
	}
}

func TestWorkflowsShowNonexistentNameReturnsError(t *testing.T) {
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"show", "no-such-workflow", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for nonexistent workflow name")
	}
	if !strings.Contains(err.Error(), "unknown workflow") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsShowRequiresExactlyOneName(t *testing.T) {
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"show"}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for show with no name")
	}
	if !strings.Contains(err.Error(), "expected exactly one workflow name") {
		t.Fatalf("unexpected error message: %v", err)
	}
	var out2, errOut2 strings.Builder
	err = runWorkflowsWithIO([]string{"show", "a", "b"}, &out2, &errOut2)
	if err == nil {
		t.Fatal("expected error for show with two names")
	}
	if !strings.Contains(err.Error(), "expected exactly one workflow name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsValidateEmptyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"validate", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("validate on empty workspace returned error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "No workflows found") {
		t.Fatalf("expected 'No workflows found', got: %s", text)
	}
}

func TestWorkflowsListShowsDiscoveredWorkflows(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "alpha", `version = 1
name = "alpha"
description = "first workflow"
initial_step = "step1"

[[steps]]
id = "step1"
kind = "agent"
agent = "worker"

[[transitions]]
from = "step1"
to = "success"
match = { status = "succeeded" }
`)
	writeWorkflowFixture(t, workspace, "beta", `version = 1
name = "beta"
description = "second workflow"
initial_step = "s1"

[[steps]]
id = "s1"
kind = "agent"
agent = "worker"

[[transitions]]
from = "s1"
to = "success"
match = { status = "succeeded" }
`)

	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"list", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("list error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "alpha") {
		t.Fatalf("expected 'alpha' in list output: %s", text)
	}
	if !strings.Contains(text, "beta") {
		t.Fatalf("expected 'beta' in list output: %s", text)
	}
}

func TestWorkflowsShowFullPipeline(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "feature-delivery", `version = 1
name = "feature-delivery"
description = "Plan, implement, and deliver."
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 12000

[limits]
max_step_attempts = 12
max_duration_seconds = 7200

[[steps]]
id = "plan"
kind = "agent"
agent = "planner"
template = "templates/plan.md"
context = [{ from = "inputs.task", as = "task", max_bytes = 12000 }]
on_failure = "failure"

[[steps]]
id = "implement"
kind = "agent"
agent = "go-engineer"
on_failure = "failure"

[[transitions]]
from = "plan"
to = "implement"
match = { status = "succeeded" }

[[transitions]]
from = "implement"
to = "success"
match = { status = "succeeded" }

[delivery]
kind = "pull_request"
mode = "draft"
provider = "github"
base = "main"
`)

	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"show", "feature-delivery", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("show error: %v", err)
	}
	text := out.String()
	// Verify key fields are present in the formatted output.
	for _, field := range []string{"feature-delivery", "Plan, implement, and deliver.", "plan", "implement", "pull_request"} {
		if !strings.Contains(text, field) {
			t.Fatalf("expected %q in show output: %s", field, text)
		}
	}
}

func TestWorkflowsValidateAllValid(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "simple", `version = 1
name = "simple"
description = "A simple workflow"
initial_step = "do"

[[steps]]
id = "do"
kind = "agent"
agent = "worker"

[[transitions]]
from = "do"
to = "success"
match = { status = "succeeded" }
`)

	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"validate", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("validate error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "✓") {
		t.Fatalf("expected checkmark in validate output: %s", text)
	}
}

func TestWorkflowsValidateByName(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "good", `version = 1
name = "good"
description = "Valid workflow"
initial_step = "s1"

[[steps]]
id = "s1"
kind = "agent"
agent = "worker"

[[transitions]]
from = "s1"
to = "success"
match = { status = "succeeded" }
`)
	writeWorkflowFixture(t, workspace, "bad", `version = 1
name = "bad"
description = "Invalid workflow"
initial_step = "x"
`)

	// Validate only "good".
	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"validate", "good", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("validate good error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "✓ good") {
		t.Fatalf("expected '✓ good' in validate output: %s", text)
	}
	if strings.Contains(text, "bad") {
		t.Fatalf("unexpected 'bad' in filtered validate output: %s", text)
	}

	// Validate only "bad" (should fail).
	var out2, errOut2 strings.Builder
	err := runWorkflowsWithIO([]string{"validate", "bad", "--workspace", workspace}, &out2, &errOut2)
	if err == nil {
		t.Fatal("expected error validating bad workflow")
	}
	text2 := out2.String()
	if !strings.Contains(text2, "✗ bad") {
		t.Fatalf("expected '✗ bad' in validate output: %s", text2)
	}
}

func TestWorkflowsValidateInvalidWorkflowReportsError(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "broken", `version = 1
name = "broken"
description = "Missing steps"
initial_step = "nope"
`)

	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"validate", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error validating broken workflow")
	}
	text := out.String()
	if !strings.Contains(text, "✗ broken") {
		t.Fatalf("expected '✗ broken' in validate output: %s", text)
	}
}

func TestWorkflowsWorkspaceFlagEqualsSyntax(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "eq-test", `version = 1
name = "eq-test"
description = "Equals syntax test"
initial_step = "s1"

[[steps]]
id = "s1"
kind = "agent"
agent = "worker"

[[transitions]]
from = "s1"
to = "success"
match = { status = "succeeded" }
`)

	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"list", "--workspace=" + workspace}, &out, &errOut); err != nil {
		t.Fatalf("list with --workspace=DIR error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "eq-test") {
		t.Fatalf("expected 'eq-test' with --workspace= syntax: %s", text)
	}
}

func TestWorkflowsExplainNonexistentName(t *testing.T) {
	workspace := t.TempDir()
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"explain", "no-such-workflow", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for nonexistent workflow name")
	}
	if !strings.Contains(err.Error(), "unknown workflow") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsExplainRequiresExactlyOneName(t *testing.T) {
	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"explain"}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for explain with no name")
	}
	if !strings.Contains(err.Error(), "expected exactly one workflow name") {
		t.Fatalf("unexpected error message: %v", err)
	}
	var out2, errOut2 strings.Builder
	err = runWorkflowsWithIO([]string{"explain", "a", "b"}, &out2, &errOut2)
	if err == nil {
		t.Fatal("expected error for explain with two names")
	}
	if !strings.Contains(err.Error(), "expected exactly one workflow name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsExplainValidWorkflow(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "pipeline", pipelineExplainFixture)

	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"explain", "pipeline", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("explain error: %v", err)
	}
	text := out.String()

	// Header, state graph, and transitions
	assertExplainContains(t, text, []string{
		"Workflow: pipeline (v1)",
		"Plan, implement, review, deliver.",
		"Digest:",
		"State Graph",
		"→ [agent] plan",
		"[agent] implement",
		"[evidence_gate] review",
		"when status=succeeded → implement",
		"fix-loop: max 3 iterations",
		"verdict=approved → success",
		"verdict=rejected → failure",
	})
}

func TestWorkflowsExplainValidWorkflowMetadata(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "pipeline", pipelineExplainFixture)

	var out, errOut strings.Builder
	if err := runWorkflowsWithIO([]string{"explain", "pipeline", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("explain error: %v", err)
	}
	text := out.String()

	// Declared authority, references, delivery, limits
	assertExplainContains(t, text, []string{
		"Loop Caps",
		"fix-loop: max 3 iterations",
		"Declared Authority",
		"agent: go-engineer",
		"agent: planner",
		"References",
		"template: templates/plan.md",
		"Delivery",
		"kind:    pull_request",
		"Limits",
		"max_step_attempts:    12",
		"max_duration_seconds: 7200",
	})
}

func TestWorkflowsExplainInvalidWorkflow(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFixture(t, workspace, "broken", `version = 1
name = "broken"
description = "Invalid workflow"
initial_step = "nope"
`)

	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"explain", "broken", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error explaining invalid workflow")
	}
	if !strings.Contains(err.Error(), "workflows explain") {
		t.Fatalf("error should be prefixed with 'workflows explain': %v", err)
	}
}
