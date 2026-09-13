package automation

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestStepKindStringCoversEveryCase exercises StepKind.String()'s full
// switch, including the default "unknown" branch for an out-of-range
// value - a value that can legitimately arrive here from a TOML decode
// of a raw int outside the declared range (MarshalText/UnmarshalText
// only run through their own text form; String() is also called
// directly, e.g. by error messages, on a value nobody has validated
// yet).
func TestStepKindStringCoversEveryCase(t *testing.T) {
	cases := []struct {
		kind StepKind
		want string
	}{
		{StepPrompt, "prompt"},
		{StepSkill, "skill"},
		{StepAgent, "agent"},
		{StepSlash, "slash"},
		{StepWorkflow, "workflow"},
		{StepKind(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("StepKind(%d).String() = %q, want %q", int(tc.kind), got, tc.want)
		}
	}
}

// TestStepKindMarshalTextRejectsUnknownKind covers MarshalText's own
// validStepKind guard: an out-of-range StepKind (e.g. decoded from a raw
// TOML int by a hand-edited file, or constructed programmatically) must
// fail to marshal rather than silently emitting "unknown" as if it were
// a valid step-kind name.
func TestStepKindMarshalTextRejectsUnknownKind(t *testing.T) {
	if _, err := StepKind(99).MarshalText(); err == nil {
		t.Fatal("StepKind(99).MarshalText(): got nil error, want rejection of an out-of-range kind")
	} else if !strings.Contains(err.Error(), "unknown step kind") {
		t.Fatalf("error = %q, want it naming the unknown step kind", err.Error())
	}
	for _, k := range []StepKind{StepPrompt, StepSkill, StepAgent, StepSlash, StepWorkflow} {
		if _, err := k.MarshalText(); err != nil {
			t.Errorf("StepKind(%d).MarshalText(): got %v, want nil for a valid kind", int(k), err)
		}
	}
}

// TestStepKindUnmarshalTextCoversEveryCase exercises
// StepKind.UnmarshalText's full switch (including "skill", "agent",
// "slash", the cases TestRoundTripTOML's single sample spec never
// exercises since it only uses "prompt" and "workflow") plus the
// unknown-name error branch.
func TestStepKindUnmarshalTextCoversEveryCase(t *testing.T) {
	cases := []struct {
		text string
		want StepKind
	}{
		{"prompt", StepPrompt},
		{"skill", StepSkill},
		{"agent", StepAgent},
		{"slash", StepSlash},
		{"workflow", StepWorkflow},
	}
	for _, tc := range cases {
		var k StepKind
		if err := k.UnmarshalText([]byte(tc.text)); err != nil {
			t.Errorf("UnmarshalText(%q): got %v, want nil", tc.text, err)
		}
		if k != tc.want {
			t.Errorf("UnmarshalText(%q) = %d, want %d", tc.text, int(k), int(tc.want))
		}
	}
	var k StepKind
	if err := k.UnmarshalText([]byte("bogus")); err == nil {
		t.Fatal("UnmarshalText(\"bogus\"): got nil error, want rejection")
	} else if !strings.Contains(err.Error(), "unknown step kind") {
		t.Fatalf("error = %q, want it naming the unknown step kind", err.Error())
	}
}

// specWithAgentStep builds the minimal valid Spec
// TestValidateSpecRejectsNonRootAgentRef/TestValidateSpecAcceptsRootAgentRef
// need: one StepAgent step referencing ref, a manual trigger (no
// schedule to satisfy), and no worktree (so BaseRef stays empty and
// never trips the "base_ref is set but worktree is none" check).
func specWithAgentStep(id, ref string) Spec {
	return Spec{
		ID:      id,
		Name:    "Agent step spec",
		Enabled: true,
		Trigger: TriggerSpec{Kind: TriggerManual},
		Steps: []Step{
			{Kind: StepAgent, Ref: ref, Prompt: "do the thing"},
		},
	}
}

// TestValidateSpecRejectsNonRootAgentRef is the negative half of the
// interim StepAgent restriction: executor.go's runStep dispatches
// StepAgent through cliagents.ApplySessionAgent with a nil
// *config.Resolved and an always-empty AgentSessionState (its own
// documented KNOWN GAP), so only config.RootAgentName can ever resolve
// there today - any other agent name fails mid-run with "no agents
// loaded". ValidateSpec must catch this at load time instead, naming
// the automation id, the step index, and the offending ref, so an
// operator sees the failure before the automation ever fires.
func TestValidateSpecRejectsNonRootAgentRef(t *testing.T) {
	spec := specWithAgentStep("agent-ref-case", "some-other-agent")
	err := ValidateSpec(spec, nil)
	if err == nil {
		t.Fatal("ValidateSpec: got nil error, want rejection of a non-root StepAgent ref")
	}
	if !errors.Is(err, ErrNonRootAgentRef) {
		t.Fatalf("ValidateSpec error = %v, want errors.Is match on ErrNonRootAgentRef", err)
	}
	for _, want := range []string{"agent-ref-case", "step 0", "some-other-agent", "resolves today"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateSpec error %q does not mention %q", err.Error(), want)
		}
	}
}

// TestValidateSpecAcceptsRootAgentRef is the positive half: a StepAgent
// step whose Ref is exactly config.RootAgentName is the one case that
// resolves today (ApplySessionAgent's empty AgentSessionState still
// answers a lookup for the root agent), so ValidateSpec must not reject
// it.
func TestValidateSpecAcceptsRootAgentRef(t *testing.T) {
	spec := specWithAgentStep("agent-ref-root", config.RootAgentName)
	if err := ValidateSpec(spec, nil); err != nil {
		t.Fatalf("ValidateSpec with RootAgentName ref: got %v, want nil", err)
	}
}
