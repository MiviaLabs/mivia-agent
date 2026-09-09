package automation

import (
	"strings"
	"testing"
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
