package agent

import (
	"strings"
	"testing"

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
