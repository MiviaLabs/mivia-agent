package clichat

// Regression tests for DC-9 (silent failure / fail-open guard) in the shared
// entrypoint flag helpers flagValue/flagVar (internal/cli/root.go).
//
// Before the fix, the space form took the token after a flag as its value
// unconditionally: `mivia chat -p --plain` sent the literal prompt "--plain"
// to the model and dropped the documented --plain UI flag, and
// `mivia workflow run <wf> --input --allow-publish` swallowed --allow-publish
// as an input token. The helpers now refuse a missing or dash-prefixed space
// value with "%s requires a value", matching the sibling parsers in this
// package (parseWorkflowStringFlag, worktreeFlagValue, parseSetupArgs,
// parseDoctorArgs).

import (
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"strings"
	"testing"
)

// TestFlagValueRejectsFlagLikeValue is a RED regression test: a flag token in
// value position must be refused, not swallowed as the value.
func TestFlagValueRejectsFlagLikeValue(t *testing.T) {
	_, _, _, err := flagValueLocal([]string{"-p", "--plain"}, "-p", "--prompt")
	if err == nil {
		t.Fatal("flagValueLocal([-p --plain], -p, --prompt) returned nil error; --plain was swallowed as the prompt")
	}
	if !strings.Contains(err.Error(), "requires a value") {
		t.Fatalf("error = %v, want a 'requires a value' message", err)
	}
}

// TestFlagValueRejectsBareTrailingFlag is a RED regression test: a bare
// trailing flag must be refused and named, not silently left as a positional.
func TestFlagValueRejectsBareTrailingFlag(t *testing.T) {
	_, _, _, err := flagValueLocal([]string{"-p"}, "-p", "--prompt")
	if err == nil {
		t.Fatal("flagValueLocal([-p], -p, --prompt) returned nil error; bare trailing flag was left in rest")
	}
	if !strings.Contains(err.Error(), "-p requires a value") {
		t.Fatalf("error = %v, want a message naming -p", err)
	}
}

// TestFlagVarRejectsFlagLikeValue is the flagVar RED case: a following flag
// must not be collected as a repeatable value.
func TestFlagVarRejectsFlagLikeValue(t *testing.T) {
	_, _, _, err := flagVarLocal([]string{"--input", "--allow-publish"}, "--input")
	if err == nil {
		t.Fatal("flagVarLocal([--input --allow-publish], --input) returned nil error; --allow-publish was swallowed as an input")
	}
	if !strings.Contains(err.Error(), "--input requires a value") {
		t.Fatalf("error = %v, want a message naming --input", err)
	}
}

// TestFlagVarRejectsBareTrailingFlag is the flagVar RED case for a bare
// trailing flag.
func TestFlagVarRejectsBareTrailingFlag(t *testing.T) {
	_, _, _, err := flagVarLocal([]string{"--input"}, "--input")
	if err == nil {
		t.Fatal("flagVarLocal([--input], --input) returned nil error; bare trailing flag was left in rest")
	}
	if !strings.Contains(err.Error(), "--input requires a value") {
		t.Fatalf("error = %v, want a message naming --input", err)
	}
}

// TestFlagValueSpaceFormLeavesRest is a positive control: the space form
// returns the value and leaves later flags in rest.
func TestFlagValueSpaceFormLeavesRest(t *testing.T) {
	val, rest, found, err := flagValueLocal([]string{"--prompt", "hello", "--plain"}, "--prompt")
	if err != nil {
		t.Fatalf("flagValueLocal([--prompt hello --plain], --prompt) error = %v", err)
	}
	if !found || val != "hello" {
		t.Fatalf("val = %q, found = %v, want 'hello', true", val, found)
	}
	if len(rest) != 1 || rest[0] != "--plain" {
		t.Fatalf("rest = %v, want [--plain]", rest)
	}
}

// TestFlagValueEqualsFormAcceptsDashValue is a positive control: the "=" form
// stays permissive so values that legitimately start with "-" remain
// expressible as --prompt=--plain.
func TestFlagValueEqualsFormAcceptsDashValue(t *testing.T) {
	val, rest, found, err := flagValueLocal([]string{"--prompt=--plain"}, "--prompt")
	if err != nil {
		t.Fatalf("flagValueLocal([--prompt=--plain], --prompt) error = %v", err)
	}
	if !found || val != "--plain" {
		t.Fatalf("val = %q, found = %v, want '--plain', true", val, found)
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %v, want []", rest)
	}
}

// TestFlagVarRepeatable is a positive control: repeatable flags collect every
// well-formed occurrence.
func TestFlagVarRepeatable(t *testing.T) {
	vals, rest, found, err := flagVarLocal([]string{"--input", "a=1", "--input", "b=2"}, "--input")
	if err != nil {
		t.Fatalf("flagVarLocal([--input a=1 --input b=2], --input) error = %v", err)
	}
	if !found || len(vals) != 2 || vals[0] != "a=1" || vals[1] != "b=2" {
		t.Fatalf("vals = %v, found = %v, want [a=1 b=2], true", vals, found)
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %v, want []", rest)
	}
}

// TestFlagValueEmptyInput is a positive control: empty input means an absent
// flag, never an error.
func TestFlagValueEmptyInput(t *testing.T) {
	val, rest, found, err := flagValueLocal(nil, "-p")
	if err != nil {
		t.Fatalf("flagValueLocal(nil, -p) error = %v", err)
	}
	if found || val != "" || len(rest) != 0 {
		t.Fatalf("val = %q, rest = %v, found = %v, want '', [], false", val, rest, found)
	}
}

// TestParseChatInvocationKeepsPlainFlag is an entry-level positive control:
// a well-formed chat invocation keeps the documented --plain UI flag.
func TestParseChatInvocationKeepsPlainFlag(t *testing.T) {
	inv, err := parseChatInvocation([]string{"-p", "hi", "--plain"})
	if err != nil {
		t.Fatalf("parseChatInvocation([-p hi --plain]) error = %v", err)
	}
	if inv.prompt != "hi" {
		t.Fatalf("prompt = %q, want 'hi'", inv.prompt)
	}
	if !inv.plainUI {
		t.Fatal("plainUI = false, want true; --plain was dropped")
	}
}

// TestParseChatInvocationKeepsFullDiskFlag verifies the --full-disk flag
// is parsed and stored on the invocation.
func TestParseChatInvocationKeepsFullDiskFlag(t *testing.T) {
	inv, err := parseChatInvocation([]string{"--full-disk", "--json"})
	if err != nil {
		t.Fatalf("parseChatInvocation([--full-disk --json]) error = %v", err)
	}
	if !inv.fullDisk {
		t.Fatal("fullDisk = false, want true; --full-disk was dropped")
	}
	if !inv.jsonMode {
		t.Fatal("jsonMode = false, want true; --json was dropped")
	}
}

// TestParseChatInvocationFullDiskDefault verifies fullDisk defaults to false.
func TestParseChatInvocationFullDiskDefault(t *testing.T) {
	inv, err := parseChatInvocation([]string{"--json"})
	if err != nil {
		t.Fatalf("parseChatInvocation([--json]) error = %v", err)
	}
	if inv.fullDisk {
		t.Fatal("fullDisk = true without --full-disk flag")
	}
}

// TestParseChatInvocationRejectsSwallowedPlain is the entry-level RED case:
// `mivia chat -p --plain` must refuse instead of sending "--plain" to the
// model as the prompt while silently dropping the --plain flag.
func TestParseChatInvocationRejectsSwallowedPlain(t *testing.T) {
	_, err := parseChatInvocation([]string{"-p", "--plain"})
	if err == nil {
		t.Fatal("parseChatInvocation([-p --plain]) returned nil error; --plain was swallowed as the prompt")
	}
	if !strings.Contains(err.Error(), "requires a value") {
		t.Fatalf("error = %v, want a 'requires a value' message", err)
	}
}

// TestRunWorkflowCommandRejectsFlagLikeInput is the entry-level RED case:
// `mivia workflow run <wf> --input --allow-publish` must refuse with a value
// error instead of swallowing --allow-publish as an input token.
func TestRunWorkflowCommandRejectsFlagLikeInput(t *testing.T) {
	err := cliworkflow.RunWorkflowCommandRun([]string{"wf", "--input", "--allow-publish"}, "", "", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("cliworkflow.RunWorkflowCommandRun([wf --input --allow-publish]) returned nil error")
	}
	if !strings.Contains(err.Error(), "--input requires a value") {
		t.Fatalf("error = %v, want '--input requires a value'", err)
	}
}
