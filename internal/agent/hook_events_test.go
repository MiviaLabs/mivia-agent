package agent

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func collectHookEvents(runs []runtime.HookRun, call string) []Event {
	var got []Event
	opts := Options{OnEvent: func(e Event) {
		if e.Kind == EventHook {
			got = append(got, e)
		}
	}}
	emitHookRuns(opts, call, runs)
	return got
}

// A hook is a program running on the operator's machine on every matching tool
// call. Running one invisibly is the part that is hard to defend, so a run that
// happened produces a row - including the silent run of a formatter that had
// nothing to say, which is precisely the case "did my hook fire?" is asked
// about.
func TestEveryHookRunIsEmittedIncludingSilentOnes(t *testing.T) {
	got := collectHookEvents([]runtime.HookRun{
		{Event: "PreToolUse", Program: "guard.sh"},
		{Event: "PostToolUse", Program: "fmt.sh", Output: "gofmt rewrote 2 files"},
	}, "call-1")

	if len(got) != 2 {
		t.Fatalf("emitted %d hook events, want one per run", len(got))
	}
	if got[0].Name != "PreToolUse" || !strings.Contains(got[0].Detail, "guard.sh") {
		t.Fatalf("the silent run must name its event and program, got %+v", got[0])
	}
	if got[1].Output != "gofmt rewrote 2 files" {
		t.Fatalf("hook output was lost, got %q", got[1].Output)
	}
	for _, e := range got {
		if e.ToolCallID != "call-1" {
			t.Fatalf("a hook row must correlate to the call it ran for, got %q", e.ToolCallID)
		}
	}
}

// A block is the one outcome where the tool did not happen. The row has to say
// so, or it reads as ordinary advisory chatter next to a tool that failed for
// unrelated reasons.
func TestABlockingHookRunSaysItBlocked(t *testing.T) {
	got := collectHookEvents([]runtime.HookRun{
		{Event: "PreToolUse", Program: "guard.sh", Denied: true, Output: "policy forbids this argv"},
	}, "call-1")

	if len(got) != 1 {
		t.Fatalf("emitted %d hook events, want 1", len(got))
	}
	if !strings.Contains(strings.ToLower(got[0].Detail), "blocked") {
		t.Fatalf("a denial must be labelled as one, got %q", got[0].Detail)
	}
	if got[0].Output != "policy forbids this argv" {
		t.Fatalf("the block reason must be shown, got %q", got[0].Output)
	}
}

// A misbehaving hook - one that timed out, crashed, or could not start - is the
// case the operator most needs on screen, and its diagnostic never reaches the
// model. The row is where it becomes visible.
func TestAWarningRunSurfacesTheDiagnostic(t *testing.T) {
	got := collectHookEvents([]runtime.HookRun{
		{Event: "PostToolUse", Program: "fmt.sh", Warning: "hook /home/u/.mivia/fmt.sh timed out after 10s"},
	}, "call-1")

	if len(got) != 1 {
		t.Fatalf("emitted %d hook events, want 1", len(got))
	}
	if !strings.Contains(got[0].Output, "timed out") {
		t.Fatalf("the operator diagnostic was dropped, got %q", got[0].Output)
	}
}

func TestNoHookRunsEmitNothing(t *testing.T) {
	if got := collectHookEvents(nil, "call-1"); len(got) != 0 {
		t.Fatalf("emitted %d hook events with no runs", len(got))
	}
}

// The row must say which tool the hook fired for, not just which script ran -
// a session with several hooks and several tools in flight is unreadable
// without it.
func TestHookRunDetailNamesTheTool(t *testing.T) {
	got := collectHookEvents([]runtime.HookRun{
		{Event: "PreToolUse", Program: "guard.sh", Tool: "run_command"},
	}, "call-1")
	if !strings.Contains(got[0].Detail, "run_command") {
		t.Fatalf("the row must name the tool the hook fired for, got %q", got[0].Detail)
	}
}

// The tool input a hook saw must reach the row, so an operator can see what
// triggered it - and it must go through the same redaction path tool-input
// previews use everywhere else in the transcript.
func TestHookRunInputReachesTheEventRedacted(t *testing.T) {
	got := collectHookEvents([]runtime.HookRun{
		{Event: "PreToolUse", Program: "guard.sh", Tool: "run_command", Input: `{"argv":["git","status"]}`},
	}, "call-1")
	if !strings.Contains(got[0].Input, "git") {
		t.Fatalf("the hook's tool input must reach the row, got %q", got[0].Input)
	}
}

// Hook stdout can echo back environment values or a command's own output
// verbatim. It is a transcript row every viewer of the session sees, so it
// must go through the workspace's redaction policy exactly like a
// tool-output preview, not bypass it.
func TestHookRunOutputIsRedacted(t *testing.T) {
	old := redact.Current()
	policy, err := redact.Compile([]string{`secret-shaped-[A-Za-z0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(old) })

	got := collectHookEvents([]runtime.HookRun{
		{Event: "PostToolUse", Program: "fmt.sh", Output: "token: secret-shaped-ABC123value"},
	}, "call-1")
	if strings.Contains(got[0].Output, "secret-shaped-ABC123value") {
		t.Fatalf("a secret-shaped hook output must be redacted per the active policy, got %q", got[0].Output)
	}
}
