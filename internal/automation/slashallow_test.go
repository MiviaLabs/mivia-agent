package automation

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// slashSpec builds a minimal valid Spec with one StepSlash step whose
// Ref is ref, so each D15 test case can drive ValidateSpec directly.
func slashSpec(ref string, registry *skills.Registry) (Spec, error) {
	spec := Spec{
		ID:      "slash-case",
		Name:    "slash case",
		Enabled: true,
		Steps:   []Step{{Kind: StepSlash, Ref: ref}},
	}
	return spec, ValidateSpec(spec, registry)
}

// TestSlashAllowlistExhaustive is D15's exhaustiveness test obligation:
// every entry of clichat.builtInSlashCommands() must have a
// classification in builtinSlashClass, so a builtin added later without
// classifying it fails the build rather than silently defaulting to
// allowed or rejected.
func TestSlashAllowlistExhaustive(t *testing.T) {
	for _, cmd := range clichat.BuiltInSlashCommands() {
		if _, ok := builtinSlashClass[cmd.Name]; !ok {
			t.Errorf("builtin %q has no entry in builtinSlashClass: D15 requires every builtin be classified", cmd.Name)
		}
	}
}

// TestSlashAllowlistNegativeNamedClass covers D15's negative list:
// /sessions (interactive surface), /delete (lifecycle), /search
// (outbound side effect), /help (informational no-op), and a bare
// /model (needs-arg) - each rejected with the class named in the error.
func TestSlashAllowlistNegativeNamedClass(t *testing.T) {
	cases := []struct {
		ref       string
		wantClass string
	}{
		{"/sessions", "needs an interactive surface"},
		{"/delete", "session-lifecycle mutation"},
		{"/search", "outbound side effect, unattended"},
		{"/help", "informational no-op"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			_, err := slashSpec(tc.ref, nil)
			if err == nil {
				t.Fatalf("slashSpec(%q): got nil error, want rejection naming %q", tc.ref, tc.wantClass)
			}
			if !strings.Contains(err.Error(), tc.wantClass) {
				t.Fatalf("slashSpec(%q) error = %q, want it naming class %q", tc.ref, err.Error(), tc.wantClass)
			}
		})
	}
}

// TestSlashAllowlistBareModelRejected covers a bare /model (no
// argument): it opens a picker that cannot render headless, so
// validation requires a non-empty argument.
func TestSlashAllowlistBareModelRejected(t *testing.T) {
	_, err := slashSpec("/model", nil)
	if err == nil {
		t.Fatal("slashSpec(/model) bare: got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "requires a non-empty argument") {
		t.Fatalf("error = %q, want it naming the missing-argument requirement", err.Error())
	}
}

// TestSlashAllowlistModelWithArgAccepted covers /model gpt-x: with a
// non-empty argument, /model is accepted.
func TestSlashAllowlistModelWithArgAccepted(t *testing.T) {
	if _, err := slashSpec("/model gpt-x", nil); err != nil {
		t.Fatalf("slashSpec(/model gpt-x): got %v, want nil", err)
	}
}

// TestSlashAllowlistCompactBareAccepted covers /compact bare: it is
// AutoExecute and safe with no argument.
func TestSlashAllowlistCompactBareAccepted(t *testing.T) {
	if _, err := slashSpec("/compact", nil); err != nil {
		t.Fatalf("slashSpec(/compact): got %v, want nil", err)
	}
}

// TestSlashAllowlistSkillCommandAccepted proves an arbitrary
// SlashKindSkill command is allowed: every user-invocable skill is a
// legal StepSlash Ref regardless of the closed builtin taxonomy.
func TestSlashAllowlistSkillCommandAccepted(t *testing.T) {
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{
		Name:          "release-checklist",
		UserInvocable: true,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := slashSpec("/release-checklist", reg); err != nil {
		t.Fatalf("slashSpec(/release-checklist) with a registered skill: got %v, want nil", err)
	}
}

// TestSlashAllowlistUnresolvedRejected proves a Ref that resolves to
// nothing (no builtin, no skill) is rejected.
func TestSlashAllowlistUnresolvedRejected(t *testing.T) {
	_, err := slashSpec("/not-a-real-command", nil)
	if err == nil {
		t.Fatal("slashSpec(/not-a-real-command): got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("error = %q, want it naming an unresolved command", err.Error())
	}
}

// TestSlashAllowlistEmptyRefRejected proves validateStepSlash's own
// empty-ref guard: a StepSlash step whose Ref is blank (or all
// whitespace) is rejected by name rather than falling through to
// FindSlashCommand with an empty token.
func TestSlashAllowlistEmptyRefRejected(t *testing.T) {
	_, err := slashSpec("   ", nil)
	if err == nil {
		t.Fatal("slashSpec(blank ref): got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %q, want it naming the empty ref", err.Error())
	}
}

// TestSlashAllowlistAliasInheritsCanonicalClass proves alias resolution
// (D15: "Resolution is by FindSlashCommand..., not raw-name table
// match"): /h is /help's alias, so it inherits /help's rejected
// informational-no-op classification exactly as /help itself would.
func TestSlashAllowlistAliasInheritsCanonicalClass(t *testing.T) {
	_, err := slashSpec("/h", nil)
	if err == nil {
		t.Fatal("slashSpec(/h): got nil error, want rejection (alias of /help)")
	}
	if !strings.Contains(err.Error(), "informational no-op") {
		t.Fatalf("error = %q, want it naming /help's class via alias resolution", err.Error())
	}
}

// TestSlashAllowlistSurfaceIsTUI proves the executor's D15-mandated
// choice of clichat.SlashSurfaceTUI is what makes a skill-backed
// StepSlash resolvable at all: a registry with a registered
// user-invocable skill only produces a SlashKindSkill entry for
// SlashSurfaceTUI (slash_catalog.go's own guard), so this indirectly
// pins that validateStepSlash uses that surface by proving the skill
// command above resolves. This test additionally proves FindSlashCommand
// itself, called with the plain surface, would NOT resolve the same
// skill command - the exact hazard D15 documents.
func TestSlashAllowlistSurfaceIsTUI(t *testing.T) {
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{
		Name:          "release-checklist",
		UserInvocable: true,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := clichat.FindSlashCommand("/release-checklist", clichat.SlashSurfaceTUI, reg); !ok {
		t.Fatal("FindSlashCommand with SlashSurfaceTUI did not resolve a registered skill command")
	}
}

// TestValidateSpecRejectsInvalidUnattended covers store.go's new
// ErrInvalidUnattendedPolicy branch: an Unattended value other than
// ""/UnattendedDeny/UnattendedAuto (e.g. "yolo") is rejected at
// validation.
func TestValidateSpecRejectsInvalidUnattended(t *testing.T) {
	spec := sampleSpec("bad-unattended")
	spec.Unattended = UnattendedPolicy("yolo")
	err := ValidateSpec(spec, nil)
	if err == nil || !errors.Is(err, ErrInvalidUnattendedPolicy) {
		t.Fatalf("ValidateSpec with unattended=yolo: got %v, want ErrInvalidUnattendedPolicy", err)
	}
}
