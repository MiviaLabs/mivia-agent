package cliworkflow

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
	err := RunWorkflowsWithIO([]string{}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for no subcommand")
	}
	if !strings.Contains(err.Error(), "expected list, show, validate, or explain") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsUnknownSubcommandReturnsError(t *testing.T) {
	var out, errOut strings.Builder
	err := RunWorkflowsWithIO([]string{"bogus"}, &out, &errOut)
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
	err := RunWorkflowsWithIO([]string{"list", "--workspace", workspace}, &out, &errOut)
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
	err := RunWorkflowsWithIO([]string{"show", "no-such-workflow", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for nonexistent workflow name")
	}
	if !strings.Contains(err.Error(), "unknown workflow") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsShowRequiresExactlyOneName(t *testing.T) {
	var out, errOut strings.Builder
	err := RunWorkflowsWithIO([]string{"show"}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for show with no name")
	}
	if !strings.Contains(err.Error(), "expected exactly one workflow name") {
		t.Fatalf("unexpected error message: %v", err)
	}
	var out2, errOut2 strings.Builder
	err = RunWorkflowsWithIO([]string{"show", "a", "b"}, &out2, &errOut2)
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
	err := RunWorkflowsWithIO([]string{"validate", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("validate on empty workspace returned error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "No workflows found") {
		t.Fatalf("expected 'No workflows found', got: %s", text)
	}
}

func TestWorkflowsValidateLoadsWorkflowReferences(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(t *testing.T, root string)
		wantError string
	}{
		{
			name: "template",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeWorkflowFixture(t, root, "delivery", workflowValidationFixture("missing.md", "schemas/result.json", "worker", "delivery-skill", "go-test"))
			},
			wantError: "template",
		},
		{
			name: "schema",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeWorkflowFixture(t, root, "delivery", workflowValidationFixture("templates/plan.md", "schemas/missing.json", "worker", "delivery-skill", "go-test"))
			},
			wantError: "output_schema",
		},
		{
			name: "agent",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeWorkflowFixture(t, root, "delivery", workflowValidationFixture("templates/plan.md", "schemas/result.json", "missing", "", "go-test"))
			},
			wantError: "agent \"missing\"",
		},
		{
			name: "skill",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeWorkflowFixture(t, root, "delivery", workflowValidationFixture("templates/plan.md", "schemas/result.json", "worker", "missing-skill", "go-test"))
			},
			wantError: "skill \"missing-skill\"",
		},
		{
			name: "skill tools",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, ".agents", "skills", "delivery-skill", "SKILL.md")
				if err := os.WriteFile(path, []byte("---\nname: delivery-skill\ntools: [write_file]\n---\nDeliver the task.\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "requires tool \"write_file\"",
		},
		{
			name: "verifier",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeWorkflowFixture(t, root, "delivery", workflowValidationFixture("templates/plan.md", "schemas/result.json", "worker", "delivery-skill", "missing-profile"))
			},
			wantError: "verifier \"missing-profile\"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newWorkflowValidationFixture(t)
			tc.mutate(t, root)
			var out, errOut strings.Builder
			err := RunWorkflowsWithIO([]string{"validate", "delivery", "--workspace", root}, &out, &errOut)
			if err == nil {
				t.Fatal("validate accepted a missing workflow reference")
			}
			if !strings.Contains(out.String(), tc.wantError) {
				t.Fatalf("validate output = %q, want %q", out.String(), tc.wantError)
			}
		})
	}
}

func TestWorkflowsValidateRejectsTemplateBindingOutsideStepContext(t *testing.T) {
	root := newWorkflowValidationFixture(t)
	path := filepath.Join(root, ".mivia", "workflows", "templates", "plan.md")
	if err := os.WriteFile(path, []byte("Task: {{ inputs.task }}\nPlan: {{ evidence.plan }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	err := RunWorkflowsWithIO([]string{"validate", "delivery", "--workspace", root}, &out, &errOut)
	if err == nil {
		t.Fatal("validate accepted a template binding outside the step context")
	}
	if !strings.Contains(out.String(), `template binding "evidence.plan" is missing`) {
		t.Fatalf("validate output = %q, want missing evidence binding", out.String())
	}
}

func TestWorkflowsValidateAcceptsDeclaredTemplateBindings(t *testing.T) {
	root := newWorkflowValidationFixture(t)
	writeWorkflowFixture(t, root, "delivery", `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[limits]
max_step_attempts = 2
max_duration_seconds = 60

[[steps]]
id = "plan"
kind = "agent"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "review"
kind = "agent_gate"
agent = "worker"
template = "templates/review.md"
output_schema = "schemas/result.json"
context = [
  { from = "inputs.task", as = "task", max_bytes = 100 },
  { from = "steps.plan.output", as = "plan", max_bytes = 100 },
]

[[transitions]]
from = "plan"
to = "review"
match = { status = "succeeded" }

[[transitions]]
from = "review"
to = "success"
match = { status = "succeeded" }
`)
	path := filepath.Join(root, ".mivia", "workflows", "templates", "review.md")
	if err := os.WriteFile(path, []byte("Task: {{ inputs.task }}\nPlan: {{ evidence.plan }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	if err := RunWorkflowsWithIO([]string{"validate", "delivery", "--workspace", root}, &out, &errOut); err != nil {
		t.Fatalf("validate declared template bindings: %v\n%s", err, out.String())
	}
}

func newWorkflowValidationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range map[string]string{
		".mivia/agents/worker.toml":              "name = \"worker\"\ntools = [\"read_file\"]\nskills = [\"delivery-skill\"]\n",
		".agents/skills/delivery-skill/SKILL.md": "---\nname: delivery-skill\ntools: [read_file]\n---\nDeliver the task.\n",
		".mivia/workflows/templates/plan.md":     "Task: {{ inputs.task }}\n",
		".mivia/workflows/schemas/result.json":   `{"type":"object","additionalProperties":false}`,
	} {
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkflowFixture(t, root, "delivery", workflowValidationFixture("templates/plan.md", "schemas/result.json", "worker", "delivery-skill", "go-test"))
	return root
}

func writeWorkflowValidationAgent(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, ".mivia", "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte("name = \""+name+"\"\ntools = [\"read_file\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func workflowValidationFixture(templateRef, schemaRef, agent, skill, verifier string) string {
	skillLine := ""
	if skill != "" {
		skillLine = "skill = \"" + skill + "\"\n"
	}
	return "version = 1\nname = \"delivery\"\ninitial_step = \"plan\"\n" +
		"[inputs.task]\ntype = \"string\"\nrequired = true\nmax_bytes = 100\n" +
		"[limits]\nmax_step_attempts = 2\nmax_duration_seconds = 60\n" +
		"[[steps]]\nid = \"plan\"\nkind = \"agent\"\nagent = \"" + agent + "\"\n" + skillLine +
		"template = \"" + templateRef + "\"\noutput_schema = \"" + schemaRef + "\"\n" +
		"context = [{ from = \"inputs.task\", as = \"task\", max_bytes = 100 }]\n" +
		"[[steps]]\nid = \"verify\"\nkind = \"evidence_gate\"\nverifier = \"" + verifier + "\"\n" +
		"[[transitions]]\nfrom = \"plan\"\nto = \"verify\"\nmatch = { status = \"succeeded\" }\n" +
		"[[transitions]]\nfrom = \"verify\"\nto = \"success\"\nmatch = { status = \"succeeded\" }\n" +
		"[delivery]\nkind = \"pull_request\"\nmode = \"draft\"\nprovider = \"github\"\nbase = \"master\"\n"
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
	if err := RunWorkflowsWithIO([]string{"list", "--workspace", workspace}, &out, &errOut); err != nil {
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
	if err := RunWorkflowsWithIO([]string{"show", "feature-delivery", "--workspace", workspace}, &out, &errOut); err != nil {
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
	writeWorkflowValidationAgent(t, workspace, "worker")
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
	if err := RunWorkflowsWithIO([]string{"validate", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("validate error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "✓") {
		t.Fatalf("expected checkmark in validate output: %s", text)
	}
}

func TestWorkflowsValidateByName(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowValidationAgent(t, workspace, "worker")
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
	if err := RunWorkflowsWithIO([]string{"validate", "good", "--workspace", workspace}, &out, &errOut); err != nil {
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
	err := RunWorkflowsWithIO([]string{"validate", "bad", "--workspace", workspace}, &out2, &errOut2)
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
	err := RunWorkflowsWithIO([]string{"validate", "--workspace", workspace}, &out, &errOut)
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
	if err := RunWorkflowsWithIO([]string{"list", "--workspace=" + workspace}, &out, &errOut); err != nil {
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
	err := RunWorkflowsWithIO([]string{"explain", "no-such-workflow", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for nonexistent workflow name")
	}
	if !strings.Contains(err.Error(), "unknown workflow") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWorkflowsExplainRequiresExactlyOneName(t *testing.T) {
	var out, errOut strings.Builder
	err := RunWorkflowsWithIO([]string{"explain"}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for explain with no name")
	}
	if !strings.Contains(err.Error(), "expected exactly one workflow name") {
		t.Fatalf("unexpected error message: %v", err)
	}
	var out2, errOut2 strings.Builder
	err = RunWorkflowsWithIO([]string{"explain", "a", "b"}, &out2, &errOut2)
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
	if err := RunWorkflowsWithIO([]string{"explain", "pipeline", "--workspace", workspace}, &out, &errOut); err != nil {
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
	if err := RunWorkflowsWithIO([]string{"explain", "pipeline", "--workspace", workspace}, &out, &errOut); err != nil {
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
	err := RunWorkflowsWithIO([]string{"explain", "broken", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error explaining invalid workflow")
	}
	if !strings.Contains(err.Error(), "workflows explain") {
		t.Fatalf("error should be prefixed with 'workflows explain': %v", err)
	}
}
