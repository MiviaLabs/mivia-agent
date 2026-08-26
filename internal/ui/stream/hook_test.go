package stream

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Before renderHook existed, KindHook fell through renderOne's default case
// and Render returned "unhandled body type" - the plain (--output json /
// non-TTY) surface would hard-fail an entire session transcript the moment
// any lifecycle hook fired. This pins the fix.
func TestRenderHookDoesNotError(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, []uievent.Event{{Kind: uievent.KindHook, Body: uievent.HookBody{
		Event: "PostToolUse", Program: "fmt.sh", Tool: "write_file", Output: "reformatted",
	}}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"fmt.sh", "PostToolUse", "write_file", "reformatted"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered hook line missing %q, got %q", want, got)
		}
	}
}

// A denied run must say so in plain text too, not just in the TUI.
func TestRenderHookDeniedIsMarked(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, []uievent.Event{{Kind: uievent.KindHook, Body: uievent.HookBody{
		Event: "PreToolUse", Program: "guard.sh", Tool: "run_command",
		Input: `{"argv":["rm"]}`, Output: "policy forbids this", Denied: true,
	}}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"blocked", "guard.sh", "rm", "policy forbids this"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered denied hook line missing %q, got %q", want, got)
		}
	}
}
