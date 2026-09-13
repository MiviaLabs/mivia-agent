package automation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// TestParseSkillRef covers parseSkillRef's own table of cases: leading
// slashes (any number) are stripped before splitting the name from its
// trailing argument text, and an empty or whitespace-only ref is
// rejected outright.
func TestParseSkillRef(t *testing.T) {
	cases := []struct {
		ref      string
		wantName string
		wantArgs string
		wantErr  bool
	}{
		{ref: "/bug-audit a b", wantName: "bug-audit", wantArgs: "a b"},
		{ref: "bug-audit", wantName: "bug-audit", wantArgs: ""},
		{ref: "//bug-audit x", wantName: "bug-audit", wantArgs: "x"},
		{ref: "", wantErr: true},
		{ref: "   ", wantErr: true},
		{ref: "/bug-audit path/to/file", wantName: "bug-audit", wantArgs: "path/to/file"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			name, args, err := parseSkillRef(tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSkillRef(%q): got nil error, want rejection", tc.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSkillRef(%q): unexpected error %v", tc.ref, err)
			}
			if name != tc.wantName || args != tc.wantArgs {
				t.Fatalf("parseSkillRef(%q) = (%q, %q), want (%q, %q)", tc.ref, name, args, tc.wantName, tc.wantArgs)
			}
		})
	}
}

// TestResolveSkillDefinition covers resolveSkillDefinition's matching
// semantics (mirroring internal/uiadapter/runner.go's handleSkill): a
// slash-token or case-insensitive name match that IS UserInvocable
// resolves; a non-invocable match, an unknown name, and a nil registry
// are each rejected with a distinct named error.
func TestResolveSkillDefinition(t *testing.T) {
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{Name: "bug-audit", Instructions: "hunt bugs", UserInvocable: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(skills.Definition{Name: "internal-only", Instructions: "not for direct use", UserInvocable: false}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	t.Run("matches by slash token", func(t *testing.T) {
		def, err := resolveSkillDefinition(reg, "bug-audit")
		if err != nil {
			t.Fatalf("resolveSkillDefinition: unexpected error %v", err)
		}
		if def.Name != "bug-audit" {
			t.Fatalf("resolveSkillDefinition name = %q, want bug-audit", def.Name)
		}
	})

	t.Run("matches case-insensitively", func(t *testing.T) {
		def, err := resolveSkillDefinition(reg, "Bug-Audit")
		if err != nil {
			t.Fatalf("resolveSkillDefinition: unexpected error %v", err)
		}
		if def.Name != "bug-audit" {
			t.Fatalf("resolveSkillDefinition name = %q, want bug-audit", def.Name)
		}
	})

	t.Run("non-invocable match rejected", func(t *testing.T) {
		_, err := resolveSkillDefinition(reg, "internal-only")
		if err == nil {
			t.Fatal("resolveSkillDefinition(internal-only): got nil error, want rejection")
		}
		if !strings.Contains(err.Error(), "cannot be invoked directly") {
			t.Fatalf("resolveSkillDefinition error = %q, want it naming the non-invocable rejection", err.Error())
		}
	})

	t.Run("unknown name rejected", func(t *testing.T) {
		_, err := resolveSkillDefinition(reg, "no-such-skill")
		if err == nil {
			t.Fatal("resolveSkillDefinition(no-such-skill): got nil error, want rejection")
		}
	})

	t.Run("nil registry rejected", func(t *testing.T) {
		_, err := resolveSkillDefinition(nil, "bug-audit")
		if err == nil {
			t.Fatal("resolveSkillDefinition(nil registry): got nil error, want rejection")
		}
	})
}

// bugAuditSkillDefinition is the fixture skill TestRunStepSkill* registers:
// a real, user-invocable skill whose instructions are short but
// distinctive enough to assert on verbatim in the rendered prompt.
func bugAuditSkillDefinition() skills.Definition {
	return skills.Definition{
		Name:          "bug-audit",
		Instructions:  "Hunt for reachable bugs in the diff.",
		UserInvocable: true,
	}
}

// newSkillBoundSession builds a real *chat.Session carrying reg on its
// current binding, exactly as the interactive TUI's own agent-surface
// wiring does (chat.Session.SetBindingSkillRegistry) - the same
// resolution path automation.Service.skillRegistryFor reads from a
// spawned run's own boundSess.
func newSkillBoundSession(reg *skills.Registry) *chat.Session {
	sess := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nil)
	sess.SetBindingSkillRegistry(reg)
	return sess
}

// TestRunStepSkillRendersInstructionsAndPersistedText covers runStep's
// StepSkill case end to end: the ref "/bug-audit some args" resolves
// against the session's own bound registry, sends the rendered
// <skill-instructions> prompt (never the literal "/"+Ref text the
// executor used to send), and persists only the short slash command.
func TestRunStepSkillRendersInstructionsAndPersistedText(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reg := skills.NewRegistry()
	if err := reg.Register(bugAuditSkillDefinition()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := newSkillBoundSession(reg)
	conv := newRecordingConversation()
	step := Step{Kind: StepSkill, Ref: "/bug-audit some args"}

	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, sess, step, time.Second)
	if err != nil {
		t.Fatalf("runStep(StepSkill): unexpected error %v", err)
	}

	sent := conv.sentIntents()
	if len(sent) != 1 {
		t.Fatalf("sentIntents = %v, want exactly 1", sent)
	}
	got := sent[0]
	if !strings.Contains(got.Text, `<skill-instructions name="bug-audit">`) {
		t.Fatalf("sent text = %q, want it to contain the named skill-instructions tag", got.Text)
	}
	if !strings.Contains(got.Text, "Hunt for reachable bugs in the diff.") {
		t.Fatalf("sent text = %q, want it to contain the skill's rendered instructions", got.Text)
	}
	if got.PersistedText != "/bug-audit some args" {
		t.Fatalf("PersistedText = %q, want %q", got.PersistedText, "/bug-audit some args")
	}
}

// TestRunStepSkillUnknownFails covers sendSkillStep's own
// resolveSkillDefinition-error branch: an unresolvable skill name must
// fail runStep before ever reaching sendIntentHeadless.
func TestRunStepSkillUnknownFails(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := newSkillBoundSession(skills.NewRegistry())
	conv := newRecordingConversation()
	step := Step{Kind: StepSkill, Ref: "/no-such-skill"}

	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, sess, step, time.Second)
	if err == nil {
		t.Fatal("runStep(StepSkill) with an unknown skill: got nil error, want rejection")
	}
	if len(conv.sentIntents()) != 0 {
		t.Fatalf("sentIntents = %v, want none (an unresolvable skill must never reach sendIntentHeadless)", conv.sentIntents())
	}
}

// TestRunStepSkillNotInvocableFails covers sendSkillStep's rejection of
// a resolved-but-not-UserInvocable skill.
func TestRunStepSkillNotInvocableFails(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{Name: "internal-only", Instructions: "x", UserInvocable: false}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := newSkillBoundSession(reg)
	conv := newRecordingConversation()
	step := Step{Kind: StepSkill, Ref: "/internal-only"}

	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, sess, step, time.Second)
	if err == nil {
		t.Fatal("runStep(StepSkill) against a non-invocable skill: got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "cannot be invoked directly") {
		t.Fatalf("runStep(StepSkill) error = %q, want it naming the non-invocable rejection", err.Error())
	}
}

// TestRunStepSkillFallsBackToConfigRegistry covers skillRegistryFor's own
// fallback branch: when boundSess carries no registry on its current
// binding, Service.Config's SkillRegistry source is consulted instead.
func TestRunStepSkillFallsBackToConfigRegistry(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	reg := skills.NewRegistry()
	if err := reg.Register(bugAuditSkillDefinition()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{
		SkillRegistry: func() *skills.Registry { return reg },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// sess carries NO bound skill registry (SetBindingSkillRegistry never
	// called), so skillRegistryFor must fall through to svc.cfg.SkillRegistry.
	sess := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nil)
	conv := newRecordingConversation()
	step := Step{Kind: StepSkill, Ref: "/bug-audit"}

	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, sess, step, time.Second)
	if err != nil {
		t.Fatalf("runStep(StepSkill) via config-registry fallback: unexpected error %v", err)
	}
	sent := conv.sentIntents()
	if len(sent) != 1 || sent[0].PersistedText != "/bug-audit" {
		t.Fatalf("sentIntents = %v, want one intent persisted as /bug-audit", sent)
	}
}
