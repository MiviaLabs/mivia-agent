package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// The binary must ship zero built-in verifier profiles: an empty declaration
// set yields an empty catalogue and every name fails closed at Lookup. This
// pins rule 60 (project/language-generic engine) against regression.
func TestNoBuiltInVerifierProfiles(t *testing.T) {
	catalogue, err := workflowVerifierCatalogue(nil, secretpath.Policy{})
	if err != nil {
		t.Fatalf("build empty catalogue: %v", err)
	}
	if names := catalogue.Names(); len(names) != 0 {
		t.Fatalf("catalogue must start empty, got built-ins: %v", names)
	}
	for _, name := range []string{"go-test", "go-verify", "go-final", "go-default"} {
		if _, err := catalogue.Lookup(name); err == nil {
			t.Fatalf("legacy built-in %q must not resolve", name)
		}
	}
}

func testVerifierProfiles() map[string]config.VerifierProfile {
	return map[string]config.VerifierProfile{
		"go-test": {GoModuleBaseline: true, Commands: []config.VerifierCommand{{Check: "go-test", Program: "go", Args: []string{"test", "./..."}}}},
		"lint":    {Commands: []config.VerifierCommand{{Check: "vet", Program: "go", Args: []string{"vet", "./..."}}}},
	}
}

func gateWorkflow(names ...string) *compiler.CompiledWorkflow {
	wf := &compiler.CompiledWorkflow{}
	for i, name := range names {
		wf.Steps = append(wf.Steps, definition.Step{ID: "g" + string(rune('a'+i)), Kind: "evidence_gate", Verifier: name})
	}
	return wf
}

func TestWorkflowVerifierCatalogueRegistersDeclaredProfiles(t *testing.T) {
	catalogue, err := workflowVerifierCatalogue(testVerifierProfiles(), secretpath.Policy{})
	if err != nil {
		t.Fatalf("build catalogue: %v", err)
	}
	for _, name := range []string{"go-test", "lint"} {
		if _, err := catalogue.Lookup(name); err != nil {
			t.Fatalf("declared profile %q must resolve: %v", name, err)
		}
	}
}

func TestValidateWorkflowVerifierReferences(t *testing.T) {
	profiles := testVerifierProfiles()
	if err := validateWorkflowVerifierReferences(gateWorkflow("go-test"), profiles); err != nil {
		t.Fatalf("declared reference must validate: %v", err)
	}
	err := validateWorkflowVerifierReferences(gateWorkflow("missing"), profiles)
	if err == nil || !strings.Contains(err.Error(), "[verifiers.missing]") {
		t.Fatalf("undeclared reference must name the missing table, got %v", err)
	}
}

// nil and empty Args must digest identically, or one definition would pin two
// different digests depending on how the TOML spelled an empty argv.
func TestVerifierDefinitionBytesNormalizesArgs(t *testing.T) {
	withNil, err := verifierDefinitionBytes("p", config.VerifierProfile{Commands: []config.VerifierCommand{{Check: "c", Program: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	withEmpty, err := verifierDefinitionBytes("p", config.VerifierProfile{Commands: []config.VerifierCommand{{Check: "c", Program: "go", Args: []string{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(withNil) != string(withEmpty) {
		t.Fatalf("nil and empty args must digest identically: %s vs %s", withNil, withEmpty)
	}
}

func pinnedSnapshot(t *testing.T, wf *compiler.CompiledWorkflow, profiles map[string]config.VerifierProfile) *workflowledger.Snapshot {
	t.Helper()
	raw, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pinWorkflowVerifierDefinitions(raw, wf, profiles)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &snapshot
}

func TestVerifierPinAndResumeVerification(t *testing.T) {
	wf := gateWorkflow("go-test")
	profiles := testVerifierProfiles()
	prior := pinnedSnapshot(t, wf, profiles)
	if _, ok := prior.Verifiers[workflowVerifierDefPrefix+"go-test"]; !ok {
		t.Fatalf("pin must write the namespaced definition key: %v", prior.Verifiers)
	}
	if err := verifyWorkflowVerifierSnapshot(wf, profiles, prior); err != nil {
		t.Fatalf("unchanged definition must verify: %v", err)
	}

	changed := testVerifierProfiles()
	changed["go-test"] = config.VerifierProfile{GoModuleBaseline: true, Commands: []config.VerifierCommand{{Check: "go-test", Program: "go", Args: []string{"test", "-run", "TestNothing"}}}}
	if err := verifyWorkflowVerifierSnapshot(wf, changed, prior); err == nil || !strings.Contains(err.Error(), "changed since admission") {
		t.Fatalf("drifted definition must fail closed, got %v", err)
	}

	if err := verifyWorkflowVerifierSnapshot(wf, map[string]config.VerifierProfile{}, prior); err == nil || !strings.Contains(err.Error(), "missing on resume") {
		t.Fatalf("deleted definition must fail closed, got %v", err)
	}
}

// A snapshot admitted before definitions were pinned has no verifier-def
// keys. It must resume without definition verification, and its baseline
// decision must fall back to the legacy built-in names.
func TestLegacySnapshotSkipsVerificationAndKeepsBaseline(t *testing.T) {
	wf := gateWorkflow("go-test")
	legacy := &workflowledger.Snapshot{Verifiers: map[string]workflowledger.RefSnapshot{
		workflowGoModBaselineRef: {Digest: "sha256:x"},
	}}
	if err := verifyWorkflowVerifierSnapshot(wf, map[string]config.VerifierProfile{}, legacy); err != nil {
		t.Fatalf("legacy snapshot must skip definition verification: %v", err)
	}
	need, err := workflowNeedsGoBaseline(wf, nil, legacy)
	if err != nil || !need {
		t.Fatalf("legacy go-test run must keep its module baseline, got %v, %v", need, err)
	}
	need, err = workflowNeedsGoBaseline(gateWorkflow("custom"), nil, legacy)
	if err != nil || need {
		t.Fatalf("legacy non-go run must not need a baseline, got %v, %v", need, err)
	}
}

func TestWorkflowNeedsGoBaseline(t *testing.T) {
	profiles := testVerifierProfiles()
	need, err := workflowNeedsGoBaseline(gateWorkflow("go-test"), profiles, nil)
	if err != nil || !need {
		t.Fatalf("fresh run with flagged profile must need a baseline, got %v, %v", need, err)
	}
	need, err = workflowNeedsGoBaseline(gateWorkflow("lint"), profiles, nil)
	if err != nil || need {
		t.Fatalf("fresh run without flagged profile must not need a baseline, got %v, %v", need, err)
	}

	// On resume the PINNED definition decides, not live config: flipping the
	// flag off in config must not silently drop the pinned baseline.
	wf := gateWorkflow("go-test")
	prior := pinnedSnapshot(t, wf, profiles)
	flippedOff := map[string]config.VerifierProfile{"go-test": {Commands: profiles["go-test"].Commands}}
	need, err = workflowNeedsGoBaseline(wf, flippedOff, prior)
	if err != nil || !need {
		t.Fatalf("resume must honor the pinned flag, got %v, %v", need, err)
	}
}

// Fresh pins must stamp the version marker even when the workflow references
// no profile: on resume, version >= 1 with a missing verifier-def key means
// the key was stripped, never that the binary did not write it.
func TestPinStampsVerifierPinsVersion(t *testing.T) {
	prior := pinnedSnapshot(t, gateWorkflow(), nil)
	if prior.VerifierPinsVersion != workflowVerifierPinsVersion {
		t.Fatalf("pins version = %d, want %d", prior.VerifierPinsVersion, workflowVerifierPinsVersion)
	}
	if err := verifyWorkflowVerifierSnapshot(gateWorkflow("go-test"), testVerifierProfiles(), prior); err == nil {
		t.Fatal("a marked snapshot without the referenced pin must fail closed")
	}
}

func TestAcceptWorkflowVerifierChanges(t *testing.T) {
	wf := gateWorkflow("go-test")
	profiles := testVerifierProfiles()
	prior := pinnedSnapshot(t, wf, profiles)

	// Unchanged definitions: acceptance is a no-op.
	drifted, err := acceptWorkflowVerifierChanges(prior, wf, profiles)
	if err != nil || len(drifted) != 0 {
		t.Fatalf("no drift expected, got %v, %v", drifted, err)
	}

	// Drifted commands: acceptance rewrites the in-memory pin so the
	// standard verification then passes against the new declaration.
	changed := testVerifierProfiles()
	changed["go-test"] = config.VerifierProfile{GoModuleBaseline: true, Commands: []config.VerifierCommand{{Check: "go-test", Program: "go", Args: []string{"test", "-count=1", "./..."}}}}
	if err := verifyWorkflowVerifierSnapshot(wf, changed, prior); err == nil {
		t.Fatal("drift must fail closed before acceptance")
	}
	drifted, err = acceptWorkflowVerifierChanges(prior, wf, changed)
	if err != nil || len(drifted) != 1 || drifted[0] != "go-test" {
		t.Fatalf("accept = %v, %v", drifted, err)
	}
	if err := verifyWorkflowVerifierSnapshot(wf, changed, prior); err != nil {
		t.Fatalf("verification must pass after acceptance: %v", err)
	}

	// A deleted declaration cannot be accepted.
	if _, err := acceptWorkflowVerifierChanges(prior, wf, map[string]config.VerifierProfile{}); err == nil {
		t.Fatal("acceptance must refuse an undeclared profile")
	}

	// Turning go_module_baseline ON mid-run cannot be accepted when the run
	// pinned no baseline at admission.
	off := map[string]config.VerifierProfile{"lint": {Commands: []config.VerifierCommand{{Check: "vet", Program: "go", Args: []string{"vet"}}}}}
	lintWf := gateWorkflow("lint")
	lintPrior := pinnedSnapshot(t, lintWf, off)
	on := map[string]config.VerifierProfile{"lint": {GoModuleBaseline: true, Commands: off["lint"].Commands}}
	if _, err := acceptWorkflowVerifierChanges(lintPrior, lintWf, on); err == nil {
		t.Fatal("acceptance must refuse a new baseline requirement without an admitted baseline")
	}

	// Legacy snapshot: nothing pinned, acceptance is a no-op.
	legacy := &workflowledger.Snapshot{}
	drifted, err = acceptWorkflowVerifierChanges(legacy, wf, changed)
	if err != nil || drifted != nil {
		t.Fatalf("legacy acceptance must be a no-op, got %v, %v", drifted, err)
	}
}

// Fresh admission stamps the stacking semantics marker, and the resume
// compile selector honors it: marked snapshots resume with the strict
// opt-in activation, unmarked (pre-marker) snapshots with the legacy one.
func TestPinStampsStackingSemanticsAndSelectorHonorsIt(t *testing.T) {
	prior := pinnedSnapshot(t, gateWorkflow(), nil)
	if prior.StackingSemanticsVersion != workflowledger.StackingSemanticsOptIn {
		t.Fatalf("stacking semantics version = %d, want %d", prior.StackingSemanticsVersion, workflowledger.StackingSemanticsOptIn)
	}
	// A table-less definition whose graph matches the old inference shape:
	// plan -> implement with the change-summary schema and a "plan" binding.
	wf := definition.WorkflowFile{
		Version:     1,
		Name:        "inferable",
		InitialStep: "plan",
		Steps: []definition.Step{
			{ID: "plan", Kind: "agent", Agent: "workflow-engineer"},
			{ID: "implement", Kind: "agent", Agent: "workflow-engineer",
				OutputSchema: "schemas/change-summary-v1.json",
				Context:      []definition.ContextBinding{{From: "steps.plan.output", As: "plan"}}},
		},
		Transitions: []definition.Transition{
			{From: "plan", To: "implement"},
			{From: "implement", To: "success"},
		},
	}
	optIn, err := compileWorkflowResumeSnapshot(*prior, &wf)
	if err != nil {
		t.Fatalf("opt-in resume compile failed: %v", err)
	}
	if optIn.Stacking != nil {
		t.Fatalf("marked snapshot resumed stacking-active: %+v", optIn.Stacking)
	}
	legacy, err := compileWorkflowResumeSnapshot(workflowledger.Snapshot{}, &wf)
	if err != nil {
		t.Fatalf("legacy resume compile failed: %v", err)
	}
	if legacy.Stacking == nil {
		t.Fatal("unmarked snapshot lost its legacy stacking activation")
	}
}
