package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// loadFixture reads a test fixture and returns a parsed WorkflowFile, or fails the test.
// It uses filepath.Base to extract the filename for ParseWorkflowTOML.
// For fixtures where the filename doesn't match the in-file name, use loadFixtureAllowMismatch.
func loadFixture(t *testing.T, filename string) *definition.WorkflowFile {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("reading fixture %q: %v", filename, err)
	}
	// The valid fixture is named valid-feature-delivery.toml but the in-file name
	// is "feature-delivery", so ParseWorkflowTOML's name-matching would fail.
	// Use a synthetic filename that matches the in-file name.
	base := filepath.Base(filename)
	// For "valid-*.toml" fixtures, strip the "valid-" prefix.
	syntheticName := strings.TrimPrefix(base, "valid-")
	if syntheticName != base {
		// e.g. valid-feature-delivery.toml -> feature-delivery.toml
		wf, _, err := definition.ParseWorkflowTOML(data, syntheticName)
		if err != nil {
			t.Fatalf("parsing fixture %q: %v", filename, err)
		}
		return &wf
	}
	wf, _, err := definition.ParseWorkflowTOML(data, base)
	if err != nil {
		t.Fatalf("parsing fixture %q: %v", filename, err)
	}
	return &wf
}

// newMinimalWorkflow returns a WorkflowFile with a single agent step and a
// success transition. Use the returned value directly (it is heap-allocated).
func newMinimalWorkflow(name string) *definition.WorkflowFile {
	return &definition.WorkflowFile{
		Name:        name,
		Version:     1,
		InitialStep: "plan",
		Steps:       []definition.Step{{ID: "plan", Kind: "agent", Agent: "planner"}},
		Transitions: []definition.Transition{{
			From: "plan", To: "success", Match: definition.MatchCriteria{Status: "succeeded"},
		}},
	}
}

// assertCompileError verifies that Compile rejects the workflow and that the
// returned error contains the given substring.
func assertCompileError(t *testing.T, wf *definition.WorkflowFile, desc, substr string) {
	t.Helper()
	_, err := Compile(wf)
	if err == nil {
		t.Fatalf("expected compile error for %s, got nil", desc)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Errorf("error %q should mention %q", err.Error(), substr)
	}
}

func TestCompile_ValidFixture(t *testing.T) {
	wf := loadFixture(t, "../testdata/valid-feature-delivery.toml")

	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}
	if cw.Name != "feature-delivery" {
		t.Errorf("Name = %q, want %q", cw.Name, "feature-delivery")
	}
	if cw.InitialStep != "plan" {
		t.Errorf("InitialStep = %q, want %q", cw.InitialStep, "plan")
	}
	if len(cw.Steps) != 5 {
		t.Errorf("len(Steps) = %d, want 5", len(cw.Steps))
	}
	if len(cw.Transitions) != 7 {
		t.Errorf("len(Transitions) = %d, want 7", len(cw.Transitions))
	}
	if cw.Delivery == nil {
		t.Error("Delivery is nil, want non-nil")
	}
}

func TestCompile_CompiledWorkflowFields(t *testing.T) {
	wf := loadFixture(t, "../testdata/valid-feature-delivery.toml")

	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}

	// Check top-level fields
	if cw.Name != wf.Name {
		t.Errorf("Name = %q, want %q", cw.Name, wf.Name)
	}
	if cw.Description != wf.Description {
		t.Errorf("Description = %q, want %q", cw.Description, wf.Description)
	}
	if cw.Version != wf.Version {
		t.Errorf("Version = %d, want %d", cw.Version, wf.Version)
	}
	if cw.InitialStep != wf.InitialStep {
		t.Errorf("InitialStep = %q, want %q", cw.InitialStep, wf.InitialStep)
	}

	// Check StepIDs set
	expectedStepIDs := map[string]bool{
		"plan": true, "implement": true, "review": true, "verify": true, "approval": true,
	}
	for id := range expectedStepIDs {
		if !cw.StepIDs[id] {
			t.Errorf("StepIDs missing %q", id)
		}
	}
	if len(cw.StepIDs) != len(expectedStepIDs) {
		t.Errorf("StepIDs has %d entries, want %d", len(cw.StepIDs), len(expectedStepIDs))
	}

	// Check LoopNames set
	if !cw.LoopNames["review_repair"] {
		t.Error("LoopNames missing \"review_repair\"")
	}
	if len(cw.LoopNames) != 1 {
		t.Errorf("LoopNames has %d entries, want 1", len(cw.LoopNames))
	}
}

func TestCompile_UnreachableStep(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/unreachable-step.toml")

	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for unreachable step, got nil")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error %q should mention \"unreachable\"", err.Error())
	}
}

func TestCompile_UnboundedLoop(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/unbounded-loop.toml")

	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for unbounded loop, got nil")
	}
	// The fixture has no success path either, so both graph and loop errors are reported.
	// The self-loop without loop name error is produced by transition validation.
	if !strings.Contains(err.Error(), "loop") {
		t.Errorf("error %q should mention \"loop\"", err.Error())
	}
}

func TestCompile_MissingTerminalPath(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/missing-terminal-path.toml")

	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for missing terminal path, got nil")
	}
	if !strings.Contains(err.Error(), "success terminal") {
		t.Errorf("error %q should mention \"success terminal\"", err.Error())
	}
}

func TestCompile_OverlappingTransitions(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/overlapping-transitions.toml")

	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for overlapping transitions, got nil")
	}
	if !strings.Contains(err.Error(), "overlapping") {
		t.Errorf("error %q should mention \"overlapping\"", err.Error())
	}
}

func TestCompile_BadContextSource(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/bad-context-source.toml")

	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for bad context source, got nil")
	}
	if !strings.Contains(err.Error(), "unknown step") {
		t.Errorf("error %q should mention \"unknown step\"", err.Error())
	}
}

func TestCompile_BadContextPathTraversal(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/bad-context-path-traversal.toml")
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for path traversal in context binding")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("error %q should mention path traversal", err.Error())
	}
}

func TestCompile_EmptyStepID(t *testing.T) {
	// Empty step ID should be caught at ParseWorkflowTOML level.
	data, err := os.ReadFile("../testdata/invalid/empty-step-id.toml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	_, _, err = definition.ParseWorkflowTOML(data, "empty-step-id.toml")
	if err == nil {
		t.Fatal("expected parse error for empty step ID, got nil")
	}
	// Verify Compile also handles gracefully: if decode passes somehow, compile should catch it.
	// In practice, ParseWorkflowTOML rejects this first.
}

func TestCompile_ReservedStepID(t *testing.T) {
	// Reserved step ID should be caught at ParseWorkflowTOML level.
	data, err := os.ReadFile("../testdata/invalid/reserved-step-id.toml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	_, _, err = definition.ParseWorkflowTOML(data, "reserved-step-id.toml")
	if err == nil {
		t.Fatal("expected parse error for reserved step ID, got nil")
	}
}

func TestCompile_MissingAgent(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/missing-agent.toml")
	// Agent name validation is deferred to runtime agent resolution.
	// The fixture should parse and compile successfully since the compiler
	// does not validate agent names against a known set.
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("expected compile to succeed (agent validation is runtime): %v", err)
	}
	if cw.Name != "missing-agent" {
		t.Errorf("Name = %q, want %q", cw.Name, "missing-agent")
	}
}

func TestCompile_BadTransitionOutputKey(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/bad-transition-output-key.toml")
	// Schema-based output key validation is deferred to the transition matcher (Phase 2+).
	// The fixture should parse and compile since the compiler validates
	// structural graph properties, not schema-key membership.
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("expected compile to succeed (output key validation is deferred): %v", err)
	}
	if cw.Name != "bad-transition-output-key" {
		t.Errorf("Name = %q, want %q", cw.Name, "bad-transition-output-key")
	}
}

func TestCompile_BackEdgeLoopOmittedMaxIterations(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/back-edge-loop-no-max.toml")
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for back-edge loop without max_iterations")
	}
	if !strings.Contains(err.Error(), "max_iterations must be > 0, or -1 for unlimited") {
		t.Errorf("error %q should mention max_iterations must be > 0, or -1 for unlimited", err.Error())
	}
}

func TestCompile_NegativeMaxIterationsRejected(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "neg-loop-test",
		Version:     1,
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{
				From:          "implement",
				To:            "implement",
				Match:         definition.MatchCriteria{Status: "failed"},
				Loop:          "fix-loop",
				MaxIterations: -2,
			},
			{
				From:  "implement",
				To:    "success",
				Match: definition.MatchCriteria{Status: "succeeded"},
			},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for max_iterations=-2")
	}
	if !strings.Contains(err.Error(), "max_iterations") {
		t.Errorf("error %q should mention max_iterations", err.Error())
	}
}

func TestCompile_NegativeContextMaxBytes(t *testing.T) {
	wf := loadFixture(t, "../testdata/invalid/negative-context-max-bytes.toml")
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected compile error for negative context max_bytes")
	}
	if !strings.Contains(err.Error(), "max_bytes") {
		t.Errorf("error %q should mention max_bytes", err.Error())
	}
}

// --- Delivery validation tests ---

func TestCompile_DeliveryValidation(t *testing.T) {
	t.Run("valid delivery populates digest", func(t *testing.T) {
		wf := loadFixture(t, "../testdata/valid-feature-delivery.toml")
		cw, err := Compile(wf)
		if err != nil {
			t.Fatalf("unexpected compile error: %v", err)
		}
		if cw.Digest == "" {
			t.Error("Digest is empty, want non-empty")
		}
	})

	wantErr := []struct {
		name, desc, substr string
		modify             func(*definition.WorkflowFile)
	}{
		{"invalid mode", "test-delivery-invalid-mode", "mode",
			func(wf *definition.WorkflowFile) {
				wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "invalid"}
			}},
		{"empty provider", "test-delivery-no-provider", "provider",
			func(wf *definition.WorkflowFile) {
				wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "draft", Provider: "", Base: "main"}
			}},
		{"empty base", "test-delivery-no-base", "base",
			func(wf *definition.WorkflowFile) {
				wf.Delivery = &definition.Delivery{Kind: "pull_request", Mode: "draft", Provider: "github", Base: ""}
			}},
		{"unknown kind", "test-delivery-unknown-kind", "kind",
			func(wf *definition.WorkflowFile) { wf.Delivery = &definition.Delivery{Kind: "unknown"} }},
	}
	for _, tc := range wantErr {
		t.Run(tc.name, func(t *testing.T) {
			wf := newMinimalWorkflow(tc.desc)
			tc.modify(wf)
			assertCompileError(t, wf, tc.desc, tc.substr)
		})
	}
}

// --- Limits validation tests ---

func TestCompile_LimitsValidation(t *testing.T) {
	t.Run("both zero is valid (defaults apply at runtime)", func(t *testing.T) {
		wf := newMinimalWorkflow("test-limits-zero")
		wf.Limits = definition.Limits{MaxStepAttempts: 0, MaxDurationSeconds: 0}
		cw, err := Compile(wf)
		if err != nil {
			t.Fatalf("unexpected compile error for zero limits: %v", err)
		}
		if cw.Digest == "" {
			t.Error("Digest is empty, want non-empty")
		}
	})

	wantErr := []struct {
		name, desc, substr string
		limits             definition.Limits
	}{
		{"negative max_step_attempts", "test-limits-neg-attempts", "max_step_attempts",
			definition.Limits{MaxStepAttempts: -1}},
		{"max_step_attempts exceeds 100", "test-limits-high-attempts", "max_step_attempts",
			definition.Limits{MaxStepAttempts: 200}},
		{"negative max_duration_seconds", "test-limits-neg-duration", "max_duration_seconds",
			definition.Limits{MaxDurationSeconds: -1}},
		{"max_duration_seconds exceeds 86400", "test-limits-high-duration", "max_duration_seconds",
			definition.Limits{MaxDurationSeconds: 100000}},
	}
	for _, tc := range wantErr {
		t.Run(tc.name, func(t *testing.T) {
			wf := newMinimalWorkflow(tc.desc)
			wf.Limits = tc.limits
			assertCompileError(t, wf, tc.desc, tc.substr)
		})
	}
}

// --- Verifier name validation tests ---

func TestCompile_VerifierNameValidation(t *testing.T) {
	t.Run("bad verifier name format", func(t *testing.T) {
		wf := loadFixture(t, "../testdata/invalid/bad-verifier-name.toml")
		_, err := Compile(wf)
		if err == nil {
			t.Fatal("expected compile error for uppercase verifier name")
		}
		if !strings.Contains(err.Error(), "verifier name") {
			t.Errorf("error %q should mention verifier name format", err.Error())
		}
	})

	t.Run("empty verifier on evidence_gate", func(t *testing.T) {
		wf := &definition.WorkflowFile{
			Name:        "test-verifier-empty",
			Version:     1,
			InitialStep: "verify",
			Steps:       []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: ""}},
			Transitions: []definition.Transition{
				{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			},
		}
		_, err := Compile(wf)
		if err == nil {
			t.Fatal("expected compile error for empty verifier on evidence_gate")
		}
		if !strings.Contains(err.Error(), "evidence_gate requires a verifier") {
			t.Errorf("error %q should mention evidence_gate requires a verifier", err.Error())
		}
	})

	t.Run("valid verifier name passes", func(t *testing.T) {
		wf := &definition.WorkflowFile{
			Name:        "test-verifier-valid",
			Version:     1,
			InitialStep: "verify",
			Steps:       []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "go-default"}},
			Transitions: []definition.Transition{
				{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			},
		}
		_, err := Compile(wf)
		if err != nil {
			t.Fatalf("unexpected compile error for valid verifier name: %v", err)
		}
	})

	t.Run("verifier name starting with hyphen rejected", func(t *testing.T) {
		wf := &definition.WorkflowFile{
			Name:        "test-verifier-hyphen-start",
			Version:     1,
			InitialStep: "verify",
			Steps:       []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "-bad"}},
			Transitions: []definition.Transition{
				{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			},
		}
		_, err := Compile(wf)
		if err == nil {
			t.Fatal("expected compile error for verifier name starting with hyphen")
		}
		if !strings.Contains(err.Error(), "verifier name") {
			t.Errorf("error %q should mention verifier name format", err.Error())
		}
	})
}

// --- Digest stability tests ---

func TestCompile_UnlimitedLoop(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name:        "unlimited-loop-test",
		Version:     1,
		InitialStep: "implement",
		Steps: []definition.Step{
			{ID: "implement", Kind: "agent", Agent: "go-engineer"},
		},
		Transitions: []definition.Transition{
			{
				From:          "implement",
				To:            "implement",
				Match:         definition.MatchCriteria{Status: "failed"},
				Loop:          "fix-loop",
				MaxIterations: -1,
			},
			{
				From:  "implement",
				To:    "success",
				Match: definition.MatchCriteria{Status: "succeeded"},
			},
		},
	}
	cw, err := Compile(wf)
	if err != nil {
		t.Fatalf("expected unlimited loop (max_iterations=-1) to compile, got: %v", err)
	}
	if !cw.LoopNames["fix-loop"] {
		t.Error("LoopNames missing \"fix-loop\"")
	}
}

func TestCompile_LoopMaxIterationsBoundary(t *testing.T) {
	makeWF := func(max int) *definition.WorkflowFile {
		return &definition.WorkflowFile{
			Name: "boundary-test", Version: 1, InitialStep: "s",
			Steps: []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
			Transitions: []definition.Transition{
				{From: "s", To: "s", Match: definition.MatchCriteria{Status: "failed"}, Loop: "loop", MaxIterations: max},
				{From: "s", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			},
		}
	}
	// 100 should compile
	if _, err := Compile(makeWF(100)); err != nil {
		t.Errorf("max_iterations=100 should compile: %v", err)
	}
	// 101 should be rejected
	if _, err := Compile(makeWF(101)); err == nil {
		t.Error("max_iterations=101 should be rejected")
	}
}

func TestCompile_MaxIterationsWithoutLoop(t *testing.T) {
	wf := &definition.WorkflowFile{
		Name: "no-loop-test", Version: 1, InitialStep: "s",
		Steps: []definition.Step{{ID: "s", Kind: "agent", Agent: "go-engineer"}},
		Transitions: []definition.Transition{
			{From: "s", To: "s", Match: definition.MatchCriteria{Status: "failed"}, MaxIterations: 5},
			{From: "s", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	_, err := Compile(wf)
	if err == nil {
		t.Fatal("expected error: max_iterations without loop name")
	}
	if !strings.Contains(err.Error(), "requires a loop name") {
		t.Errorf("error %q should mention 'requires a loop name'", err.Error())
	}
}

func TestCompile_DigestIsStable(t *testing.T) {
	t.Run("identical workflow produces same digest", func(t *testing.T) {
		wf1 := newMinimalWorkflow("stable-digest-test")
		wf2 := newMinimalWorkflow("stable-digest-test")
		cw1, err := Compile(wf1)
		if err != nil {
			t.Fatalf("first compile: %v", err)
		}
		cw2, err := Compile(wf2)
		if err != nil {
			t.Fatalf("second compile: %v", err)
		}
		if cw1.Digest != cw2.Digest {
			t.Errorf("digest mismatch: %q != %q", cw1.Digest, cw2.Digest)
		}
	})

	t.Run("modified workflow produces different digest", func(t *testing.T) {
		wf1 := newMinimalWorkflow("stable-digest-test")
		wf2 := newMinimalWorkflow("stable-digest-test")
		wf2.Description = "a different description"
		cw1, err := Compile(wf1)
		if err != nil {
			t.Fatalf("first compile: %v", err)
		}
		cw2, err := Compile(wf2)
		if err != nil {
			t.Fatalf("second compile: %v", err)
		}
		if cw1.Digest == cw2.Digest {
			t.Errorf("digest should differ when description changes, got same: %q", cw1.Digest)
		}
	})
}
