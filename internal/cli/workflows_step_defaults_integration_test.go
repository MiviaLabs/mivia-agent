package cli

// Integration tests for [step_defaults] decode-time sugar, driven through the
// real CLI surface (runWorkflowsWithIO) and the real parse+compile pipeline.
// These tests are black-box: they use only TOML fixtures and public APIs, so
// they compile before the feature exists and fail red with the decoder's
// unknown-key rejection until ParseWorkflowTOML desugars [step_defaults].

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// stepDefaultsSugared is the sugared form: every shared repair-step field
// lives in [step_defaults]; steps carry only id, kind, and step-unique data.
const stepDefaultsSugared = `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[step_defaults]
kind = "agent"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
on_failure = "failure"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "plan"

[[steps]]
id = "verify"
kind = "agent_gate"
agent = "worker"
template = "templates/plan.md"
output_schema = "schemas/result.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[transitions]]
from = "plan"
to = "verify"
match = { status = "succeeded" }

[[transitions]]
from = "verify"
to = "success"
match = { status = "succeeded" }
`

// stepDefaultsExpanded is the hand-expanded twin of stepDefaultsSugared. The
// two files must compile to identical steps and identical digests.
const stepDefaultsExpanded = `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "plan"
kind = "agent"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
on_failure = "failure"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "verify"
kind = "agent_gate"
agent = "worker"
template = "templates/plan.md"
output_schema = "schemas/result.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[transitions]]
from = "plan"
to = "verify"
match = { status = "succeeded" }

[[transitions]]
from = "verify"
to = "success"
match = { status = "succeeded" }
`

// TestWorkflowsValidateAcceptsStepDefaults drives the full CLI validate path
// (discovery -> parse -> compile -> reference validation) over a workspace
// whose workflow gets agent, skill, template, schema, and on_failure only
// from [step_defaults]. The references are real files in the workspace, so a
// pass proves the defaults reached the steps before reference validation.
func TestWorkflowsValidateAcceptsStepDefaults(t *testing.T) {
	root := newWorkflowValidationFixture(t)
	writeWorkflowFixture(t, root, "delivery", stepDefaultsSugared)

	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"validate", "delivery", "--workspace", root}, &out, &errOut)
	if err != nil {
		t.Fatalf("validate rejected a [step_defaults] workflow: %v\noutput: %s", err, out.String())
	}
}

// TestWorkflowsShowStepDefaultsMatchesExpanded pins semantic equivalence at
// the user-visible surface: `workflows show` of the sugared file and of its
// hand-expanded twin must print byte-identical output (the table is invisible
// after decode).
func TestWorkflowsShowStepDefaultsMatchesExpanded(t *testing.T) {
	showOutput := func(body string) string {
		t.Helper()
		root := newWorkflowValidationFixture(t)
		writeWorkflowFixture(t, root, "delivery", body)
		var out, errOut strings.Builder
		if err := runWorkflowsWithIO([]string{"show", "delivery", "--workspace", root}, &out, &errOut); err != nil {
			t.Fatalf("show failed: %v", err)
		}
		return out.String()
	}
	sugared := showOutput(stepDefaultsSugared)
	expanded := showOutput(stepDefaultsExpanded)
	if sugared != expanded {
		t.Fatalf("show output differs between sugared and expanded forms:\n--- sugared ---\n%s\n--- expanded ---\n%s", sugared, expanded)
	}
}

// TestStepDefaultsDigestEqualsExpanded pins the digest contract: the sugared
// file and its hand-expanded twin compile to the SAME digest. This test also
// guards the json:"-" tag on the WorkflowFile step-defaults field - removing
// the tag makes the sugared digest diverge and this test fail.
func TestStepDefaultsDigestEqualsExpanded(t *testing.T) {
	digestOf := func(body string) string {
		t.Helper()
		wf, _, err := definition.ParseWorkflowTOML([]byte(body), "delivery.toml")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		compiled, err := compiler.Compile(&wf)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		return compiled.Digest
	}
	if s, e := digestOf(stepDefaultsSugared), digestOf(stepDefaultsExpanded); s != e {
		t.Fatalf("digest mismatch: sugared %s != expanded %s", s, e)
	}
}

// TestStepDefaultsDigestSafeForExistingFiles pins that a workflow WITHOUT a
// [step_defaults] table keeps its exact pre-feature digest: the checked-in
// bug-fix.toml must compile to the same digest before and after the feature
// lands. The digest is re-derived from the expanded twin technique instead of
// a hard-coded constant: parsing the same bytes twice must be deterministic,
// and the decoded struct must be unaffected by the new field.
func TestStepDefaultsDigestSafeForExistingFiles(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".mivia", "workflows", "bug-fix.toml"))
	if err != nil {
		t.Fatalf("read shipped bug-fix.toml: %v", err)
	}
	digest := func() string {
		wf, _, perr := definition.ParseWorkflowTOML(raw, "bug-fix.toml")
		if perr != nil {
			t.Fatalf("parse shipped bug-fix.toml: %v", perr)
		}
		compiled, cerr := compiler.Compile(&wf)
		if cerr != nil {
			t.Fatalf("compile shipped bug-fix.toml: %v", cerr)
		}
		return compiled.Digest
	}
	// Pre-feature digest of the shipped file, captured on 2026-08-15 at
	// commit 6770d98d, BEFORE the [step_defaults] desugar change. The
	// feature must not move this value. Update ONLY when bug-fix.toml
	// itself is deliberately edited (e.g. the follow-up that rewrites its
	// repair steps to use [step_defaults]).
	const pinnedPreFeatureDigest = "af6c8de8f30952a1db1e58a61b086331a069118e8d801b0152a775c47860b5f2"
	first, second := digest(), digest()
	if first != second {
		t.Fatalf("digest not deterministic: %s != %s", first, second)
	}
	if pinnedPreFeatureDigest != "" && first != pinnedPreFeatureDigest {
		t.Fatalf("shipped bug-fix.toml digest changed: got %s, pinned %s", first, pinnedPreFeatureDigest)
	}
	t.Logf("bug-fix.toml digest: %s", first)
}

// stepDefaultsPanelSugared exercises the fix in 2119c4d4: an agent_panel
// step's top-level synthesis fields (agent, skill, template, output_schema,
// context) come from [step_defaults] exactly like an agent step's, while its
// panel (member list) stays entirely step-specific, matching the real
// review_panel shape in bug-fix.toml and feature-delivery.toml.
const stepDefaultsPanelSugared = `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[step_defaults]
kind = "agent"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
on_failure = "failure"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "plan"

[[steps]]
id = "review_panel"
kind = "agent_panel"

[steps.panel]
failure_policy = "require_all"
require_distinct_bindings = true
members = [
  { id = "a", agent = "worker", provider = "deepseek", model = "deepseek-v4-flash", skill = "delivery-skill", template = "templates/plan.md", output_schema = "schemas/result.json" },
  { id = "b", agent = "worker", provider = "openrouter", model = "tencent/hy3-preview", skill = "delivery-skill", template = "templates/plan.md", output_schema = "schemas/result.json" },
]

[[transitions]]
from = "plan"
to = "review_panel"
match = { status = "succeeded" }

[[transitions]]
from = "review_panel"
to = "success"
match = { status = "succeeded" }
`

// stepDefaultsPanelExpanded is the hand-expanded twin of
// stepDefaultsPanelSugared: review_panel's top-level fields are written
// directly instead of inherited. Must validate and digest identically.
const stepDefaultsPanelExpanded = `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "plan"
kind = "agent"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
on_failure = "failure"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "review_panel"
kind = "agent_panel"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
on_failure = "failure"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[steps.panel]
failure_policy = "require_all"
require_distinct_bindings = true
members = [
  { id = "a", agent = "worker", provider = "deepseek", model = "deepseek-v4-flash", skill = "delivery-skill", template = "templates/plan.md", output_schema = "schemas/result.json" },
  { id = "b", agent = "worker", provider = "openrouter", model = "tencent/hy3-preview", skill = "delivery-skill", template = "templates/plan.md", output_schema = "schemas/result.json" },
]

[[transitions]]
from = "plan"
to = "review_panel"
match = { status = "succeeded" }

[[transitions]]
from = "review_panel"
to = "success"
match = { status = "succeeded" }
`

// TestWorkflowsValidateAcceptsStepDefaultsForAgentPanel proves the
// agent_panel fix at the real CLI pipeline layer (discovery -> parse ->
// compile -> agent/skill/schema reference validation), not just the
// definition-package unit tests that construct Go structs directly. A pass
// proves step_defaults reaches an agent_panel step's top-level fields while
// its panel member list, resolved by the same real agent/skill/schema
// references, is left untouched.
func TestWorkflowsValidateAcceptsStepDefaultsForAgentPanel(t *testing.T) {
	root := newWorkflowValidationFixture(t)
	writeWorkflowFixture(t, root, "delivery", stepDefaultsPanelSugared)

	var out, errOut strings.Builder
	err := runWorkflowsWithIO([]string{"validate", "delivery", "--workspace", root}, &out, &errOut)
	if err != nil {
		t.Fatalf("validate rejected a [step_defaults] agent_panel workflow: %v\noutput: %s", err, out.String())
	}
}

// TestStepDefaultsPanelDigestEqualsExpanded proves digest equality for the
// agent_panel case through the real Compile() path, closing the gap flagged
// in review: the field-level unit tests (TestApplyStepDefaults_
// FillsAgentPanelTopLevelFields) never called Compile, so nothing proved the
// digest path treats a step_defaults-filled agent_panel step identically to
// a hand-written one.
func TestStepDefaultsPanelDigestEqualsExpanded(t *testing.T) {
	digestOf := func(body string) string {
		t.Helper()
		wf, _, err := definition.ParseWorkflowTOML([]byte(body), "delivery.toml")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		compiled, err := compiler.Compile(&wf)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		return compiled.Digest
	}
	if s, e := digestOf(stepDefaultsPanelSugared), digestOf(stepDefaultsPanelExpanded); s != e {
		t.Fatalf("digest mismatch: sugared %s != expanded %s", s, e)
	}
}

// TestStepDefaultsDoNotLeakIntoGates pins the containment rule: only steps
// whose resolved kind is "agent" inherit scalar defaults. An evidence_gate
// step declaring only its own required command must come out of parse with
// no agent, skill, template, schema, or context, even though a full set of
// scalar defaults (and a context binding) is declared alongside it.
func TestStepDefaultsDoNotLeakIntoGates(t *testing.T) {
	const body = `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[step_defaults]
kind = "agent"
agent = "worker"
skill = "delivery-skill"
template = "templates/plan.md"
output_schema = "schemas/result.json"
on_failure = "failure"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "plan"

[[steps]]
id = "verify"
kind = "evidence_gate"
command = { check = "unit tests pass", program = "go", args = ["test", "./..."] }

[[transitions]]
from = "plan"
to = "verify"
match = { status = "succeeded" }

[[transitions]]
from = "verify"
to = "success"
match = { status = "succeeded" }
`
	wf, _, err := definition.ParseWorkflowTOML([]byte(body), "delivery.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var gate *definition.Step
	for i := range wf.Steps {
		if wf.Steps[i].ID == "verify" {
			gate = &wf.Steps[i]
		}
	}
	if gate == nil {
		t.Fatal("verify step missing after parse")
	}
	if gate.Agent != "" || gate.Skill != "" || gate.Template != "" || gate.OutputSchema != "" || gate.OnFailure != "" || len(gate.Context) != 0 {
		t.Fatalf("evidence_gate inherited agent-step defaults: %+v", gate)
	}
	if gate.Kind != "evidence_gate" {
		t.Fatalf("evidence_gate kind overwritten: %+v", gate)
	}
}

// TestStepDefaultsStepOverrideWins pins the merge rules: an explicit step
// value beats the default, and a step context binding with the same "as"
// name suppresses the default binding while other default bindings append.
func TestStepDefaultsStepOverrideWins(t *testing.T) {
	const body = `version = 1
name = "delivery"
initial_step = "plan"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[inputs.scope]
type = "string"
max_bytes = 100

[step_defaults]
kind = "agent"
agent = "worker"
template = "templates/default.md"
on_failure = "failure"
context = [
  { from = "inputs.task", as = "task", max_bytes = 100 },
  { from = "inputs.scope", as = "scope", max_bytes = 100 },
]

[[steps]]
id = "plan"
template = "templates/override.md"
context = [{ from = "inputs.task", as = "task", max_bytes = 50 }]

[[transitions]]
from = "plan"
to = "success"
match = { status = "succeeded" }
`
	wf, _, err := definition.ParseWorkflowTOML([]byte(body), "delivery.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	step := wf.Steps[0]
	if step.Kind != "agent" || step.Agent != "worker" || step.OnFailure != "failure" {
		t.Fatalf("defaults not applied to empty fields: %+v", step)
	}
	if step.Template != "templates/override.md" {
		t.Fatalf("step template overridden by default: %q", step.Template)
	}
	if len(step.Context) != 2 {
		t.Fatalf("context = %+v, want step 'task' binding plus default 'scope' binding", step.Context)
	}
	if step.Context[0].As != "task" || step.Context[0].MaxBytes != 50 {
		t.Fatalf("step's own 'task' binding lost to the default: %+v", step.Context[0])
	}
	if step.Context[1].As != "scope" {
		t.Fatalf("default 'scope' binding not appended: %+v", step.Context[1])
	}
}
