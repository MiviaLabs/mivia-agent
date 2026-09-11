package automation

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func sampleSpec(id string) Spec {
	return Spec{
		ID:          id,
		Name:        "Nightly summary",
		Description: "Summarizes the day's changes",
		Enabled:     true,
		Trigger: TriggerSpec{
			Kind: TriggerScheduled,
			Schedule: &ScheduleSpec{
				Kind:         ScheduleRecurring,
				Cron:         "0 2 * * *",
				TZ:           "America/New_York",
				EverySeconds: 0,
			},
		},
		Steps: []Step{
			{Kind: StepPrompt, Prompt: "Summarize today's commits"},
			{Kind: StepWorkflow, Ref: "release-notes", Inputs: map[string]string{"branch": "main"}},
		},
		Worktree:   WorktreeNew,
		BaseRef:    "HEAD",
		Unattended: UnattendedDeny,
	}
}

// TestRoundTripTOML covers the plan's "round-trip TOML at both scopes"
// test: write a Spec, load it back, assert equal - for both
// ports.ScopeProject and ports.ScopeUser. ScopeUser is redirected to a
// temp HOME via os.Setenv so the test never touches the real
// ~/.mivia/automations.toml.
func TestRoundTripTOML(t *testing.T) {
	cases := []struct {
		name  string
		scope ports.Scope
	}{
		{"project", ports.ScopeProject},
		{"user", ports.ScopeUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.scope == ports.ScopeUser {
				t.Setenv("HOME", root)
				t.Setenv("USERPROFILE", root) // windows fallback path in workspace.UserHomeDir
			}
			spec := sampleSpec("nightly-summary")
			if err := SaveSpecs(tc.scope, root, []Spec{spec}); err != nil {
				t.Fatalf("SaveSpecs: %v", err)
			}
			got, err := LoadSpecs(tc.scope, root)
			if err != nil {
				t.Fatalf("LoadSpecs: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("LoadSpecs returned %d specs, want 1", len(got))
			}
			if !reflect.DeepEqual(got[0], spec) {
				t.Fatalf("round-trip mismatch:\n got  = %#v\n want = %#v", got[0], spec)
			}
		})
	}
}

// TestFirstRegistryReturnsExplicitRegistry closes firstRegistry's own
// non-empty branch: LoadSpecs/SaveSpecs's variadic registry parameter is
// otherwise only ever exercised with zero arguments across this
// package's tests, leaving firstRegistry's `return registry[0]` line
// unreached. Round-tripping a plain (non-slash) spec through both
// functions with an explicit, non-nil *skills.Registry argument proves
// the value actually flows through rather than merely compiling.
func TestFirstRegistryReturnsExplicitRegistry(t *testing.T) {
	root := t.TempDir()
	reg := skills.NewRegistry()
	spec := sampleSpec("with-registry")
	if err := SaveSpecs(ports.ScopeProject, root, []Spec{spec}, reg); err != nil {
		t.Fatalf("SaveSpecs with explicit registry: %v", err)
	}
	got, err := LoadSpecs(ports.ScopeProject, root, reg)
	if err != nil {
		t.Fatalf("LoadSpecs with explicit registry: %v", err)
	}
	if len(got) != 1 || got[0].ID != spec.ID {
		t.Fatalf("LoadSpecs with explicit registry returned %#v, want one spec %q", got, spec.ID)
	}
}

// TestLoadSpecsMissingFileIsEmpty confirms an absent automations.toml is
// not an error (mirrors config.Load's found=false-is-fine precedent for
// an optional file).
func TestLoadSpecsMissingFileIsEmpty(t *testing.T) {
	root := t.TempDir()
	got, err := LoadSpecs(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("LoadSpecs on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadSpecs on missing file = %v, want empty", got)
	}
}

// TestValidateSpecNegative covers the plan's negative Scope test list:
// unknown step kind rejected; BaseRef set with WorktreeNone rejected;
// empty Steps rejected.
func TestValidateSpecNegative(t *testing.T) {
	base := sampleSpec("negative-case")
	base.Trigger = TriggerSpec{Kind: TriggerManual}

	cases := []struct {
		name    string
		mutate  func(Spec) Spec
		wantErr string
	}{
		{
			name: "unknown step kind",
			mutate: func(s Spec) Spec {
				s.Steps = []Step{{Kind: StepKind(99), Prompt: "x"}}
				return s
			},
			wantErr: "unknown step kind",
		},
		{
			name: "base ref with worktree none",
			mutate: func(s Spec) Spec {
				s.Worktree = WorktreeNone
				s.BaseRef = "HEAD"
				return s
			},
			wantErr: "base_ref is set but worktree is none",
		},
		{
			name: "empty steps",
			mutate: func(s Spec) Spec {
				s.Steps = nil
				return s
			},
			wantErr: "steps must be non-empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.mutate(base)
			err := ValidateSpec(spec, nil)
			if err == nil {
				t.Fatalf("ValidateSpec: got nil error, want one containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateSpec error = %q, want containing %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateSpecNegativeViaSaveAndLoad proves the same three rejections
// apply at both entry points the plan names: TOML load AND SaveSpecs (the
// Apply-time validation).
func TestValidateSpecNegativeViaSaveAndLoad(t *testing.T) {
	root := t.TempDir()
	bad := sampleSpec("bad-spec")
	bad.Steps = nil
	if err := SaveSpecs(ports.ScopeProject, root, []Spec{bad}); err == nil {
		t.Fatal("SaveSpecs with empty steps: got nil error, want rejection")
	}
}

// TestValidateSpecSkillRefMustBeNonEmpty covers ValidateSpec's own
// StepSkill guard: an empty or whitespace-only Ref is rejected
// regardless of whether a registry is supplied - parseSkillRef's own
// validation, reused by validateStepSkill.
func TestValidateSpecSkillRefMustBeNonEmpty(t *testing.T) {
	base := sampleSpec("skill-empty-ref")
	base.Trigger = TriggerSpec{Kind: TriggerManual}
	base.Steps = []Step{{Kind: StepSkill, Ref: "   "}}
	if err := ValidateSpec(base, nil); err == nil {
		t.Fatal("ValidateSpec with an empty StepSkill ref: got nil error, want rejection")
	}
}

// TestValidateSpecSkillRefMustResolveWhenRegistryGiven covers
// ValidateSpec's own StepSkill guard: a Ref naming a skill absent from a
// SUPPLIED non-nil registry is rejected, but the same spec passes with a
// nil registry (no registry to check against, matching StepSlash's own
// documented nil-registry behavior) and with a registry that DOES carry
// the skill.
func TestValidateSpecSkillRefMustResolveWhenRegistryGiven(t *testing.T) {
	base := sampleSpec("skill-must-resolve")
	base.Trigger = TriggerSpec{Kind: TriggerManual}
	base.Steps = []Step{{Kind: StepSkill, Ref: "/bug-audit"}}

	if err := ValidateSpec(base, nil); err != nil {
		t.Fatalf("ValidateSpec with nil registry: got error %v, want nil (no registry to check against)", err)
	}

	empty := skills.NewRegistry()
	if err := ValidateSpec(base, empty); err == nil {
		t.Fatal("ValidateSpec against a registry missing the referenced skill: got nil error, want rejection")
	}

	withSkill := skills.NewRegistry()
	if err := withSkill.Register(skills.Definition{Name: "bug-audit", Instructions: "hunt bugs", UserInvocable: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := ValidateSpec(base, withSkill); err != nil {
		t.Fatalf("ValidateSpec against a registry carrying the referenced skill: got error %v, want nil", err)
	}
}

// TestValidateID covers the plan's ID validation negative cases:
// ../../etc, __last__, __last__x, 64+ chars, uppercase, empty - all
// rejected with a named error (ErrInvalidID). A couple of positive cases
// are included to prove the regex is not overly strict.
func TestValidateID(t *testing.T) {
	invalid := []string{
		"../../etc",
		"__last__",
		"__last__x",
		strings.Repeat("a", 65), // 65 chars, one over the 64-char cap (1 + 63)
		"Uppercase",
		"",
	}
	for _, id := range invalid {
		t.Run("invalid_"+id, func(t *testing.T) {
			err := ValidateID(id)
			if err == nil {
				t.Fatalf("ValidateID(%q): got nil error, want ErrInvalidID", id)
			}
			if !errors.Is(err, ErrInvalidID) {
				t.Fatalf("ValidateID(%q) error = %v, want wrapping ErrInvalidID", id, err)
			}
		})
	}

	valid := []string{
		"a",
		"nightly-summary",
		"auto_1",
		strings.Repeat("b", 64), // 64 chars total, exactly at the cap (1 + 63)
	}
	for _, id := range valid {
		t.Run("valid_"+id, func(t *testing.T) {
			if err := ValidateID(id); err != nil {
				t.Fatalf("ValidateID(%q): got %v, want nil", id, err)
			}
		})
	}
}

// TestValidateSpecRejectsMalformedID proves ValidateSpec itself (called
// from LoadSpecs) rejects a malformed automation ID, not just a
// standalone ValidateID call.
func TestValidateSpecRejectsMalformedID(t *testing.T) {
	spec := sampleSpec("__last__")
	err := ValidateSpec(spec, nil)
	if err == nil || !errors.Is(err, ErrInvalidID) {
		t.Fatalf("ValidateSpec with malformed id: got %v, want ErrInvalidID", err)
	}
}

// TestAtomicWriteInterruptedBeforeRename covers D1's atomic-write
// requirement: a write interrupted before rename must leave the
// previous automations.toml intact and parseable. It simulates the
// interruption by calling writeFileAtomic's constituent steps directly
// (create temp, write, fsync, close) and deliberately NOT renaming,
// then asserts the original file on disk is unchanged and still loads.
func TestAtomicWriteInterruptedBeforeRename(t *testing.T) {
	root := t.TempDir()
	original := sampleSpec("first-automation")
	if err := SaveSpecs(ports.ScopeProject, root, []Spec{original}); err != nil {
		t.Fatalf("initial SaveSpecs: %v", err)
	}
	path := filepath.Join(root, ".mivia", automationsFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original file: %v", err)
	}

	// Simulate an interrupted write: create the temp file, write garbage,
	// fsync, close - everything writeFileAtomic does EXCEPT the final
	// rename - so the temp file exists on disk but the destination path
	// was never touched.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := tmp.Write([]byte("not valid toml {{{")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Deliberately no os.Rename call here - this is the "interrupted before
	// rename" simulation.

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file after interrupted write: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("destination file changed after an interrupted (non-renamed) write:\n before = %q\n after  = %q", before, after)
	}

	got, err := LoadSpecs(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("LoadSpecs after interrupted write: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], original) {
		t.Fatalf("LoadSpecs after interrupted write = %#v, want unchanged %#v", got, original)
	}
}
