package agent

import (
	"errors"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// errBoom is any run failure: a failed run is never continued.
var errBoom = errors.New("boom")

// TestAnnouncesUnactedWork states the detector's rule table. It is the one
// inexact part of the mechanism, so the cases it must NOT fire on matter as
// much as the ones it must.
func TestAnnouncesUnactedWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"promised dispatch", "I'll spawn 4 agents to review the diff.", true},
		{"promised dispatch, long form", "I am going to dispatch four agents now.", true},
		{"let me read", "Let me read the config file first.", true},
		{"next step", "My next step is to run the test suite.", true},
		{"promise after prose", "Here is the plan. Now I will search the codebase for callers.", true},

		{"plain answer", "The module name is github.com/MiviaLabs/mivia-agent.", false},
		{"prose-only promise", "I'll explain how the loop works.", false},
		{"past tense", "I ran the tests and they pass.", false},
		{"advice to the user", "You should run make verify before pushing.", false},
		{"question", "Do you want me to dispatch four agents?", false},
		{"question after a promise", "I'll dispatch agents - should I proceed?", false},

		// Deferral collides head-on with the intent lexicon, and acting on
		// it runs a tool the model deliberately did not run. These are the
		// false positives that cost more than a wasted provider call.
		{"let me know", "Let me know if you'd like me to run the full test suite.", false},
		{"check with you", "I need to check with you before proceeding.", false},
		{"verify with you", "I should verify this with you first.", false},
		{"want me to", "Tell me if you want me to dispatch agents for this.", false},
		{"awaiting approval", "I will run the migration once I have your approval.", false},
		{"empty", "   ", false},
		{"verb without intent", "The build runs the linter.", false},
		{"substring is not a word", "I'll realistically additionally clarify.", false},
		{"verb too far from the intent", "Let me " + spaces(120) + "run it.", false},
	} {
		if got := announcesUnactedWork(tc.text); got != tc.want {
			t.Errorf("%s: announcesUnactedWork(%q) = %v, want %v", tc.name, tc.text, got, tc.want)
		}
	}
}

func spaces(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'x'
		if i%5 == 0 {
			out[i] = ' '
		}
	}
	return string(out)
}

// unactedRun builds the exact result shape the mechanism targets: a turn
// that answered with an announcement and called no tool.
func unactedRun(text string) sdkagentloop.Result {
	return sdkagentloop.Result{
		Stop: sdkagentloop.StopNoToolCalls,
		History: []sdkshape.Message{
			{Role: sdkshape.RoleUser, Content: "review the diff"},
			{Role: sdkshape.RoleAssistant, Content: text},
		},
		Final: sdkshape.Message{Role: sdkshape.RoleAssistant, Content: text},
	}
}

func toolsAdvertised() Options {
	return Options{
		MaxUnactedContinuations: 1,
		AdvertisedToolSpecs: []provider.ToolSpec{
			{"type": "function", "function": map[string]any{"name": "dispatch_tasks"}},
		},
	}
}

// TestTurnLeftWorkUnacted pins every precondition. Each case turns exactly
// one of them off and must stop the continuation.
func TestTurnLeftWorkUnacted(t *testing.T) {
	announced := "I'll spawn 4 agents to review the diff."
	base := unactedRun(announced)

	if !turnLeftWorkUnacted(sdkagentloop.Options{}, toolsAdvertised(), "review the diff", base, nil) {
		t.Fatal("the target shape must be detected")
	}

	t.Run("an error is never continued", func(t *testing.T) {
		if turnLeftWorkUnacted(sdkagentloop.Options{}, toolsAdvertised(), "review the diff", base, errBoom) {
			t.Fatal("continued a failed run")
		}
	})

	t.Run("only StopNoToolCalls", func(t *testing.T) {
		for _, stop := range []sdkagentloop.StopReason{
			sdkagentloop.StopMaxIterations, sdkagentloop.StopSteered,
			sdkagentloop.StopHookVeto, sdkagentloop.StopConcluded,
			sdkagentloop.StopEmptyResponse, sdkagentloop.StopRepeatedToolFailures,
		} {
			res := unactedRun(announced)
			res.Stop = stop
			if turnLeftWorkUnacted(sdkagentloop.Options{}, toolsAdvertised(), "review the diff", res, nil) {
				t.Fatalf("continued a %s stop", stop)
			}
		}
	})

	t.Run("no tools advertised", func(t *testing.T) {
		opts := toolsAdvertised()
		opts.AdvertisedToolSpecs = []provider.ToolSpec{}
		if turnLeftWorkUnacted(sdkagentloop.Options{}, opts, "review the diff", base, nil) {
			t.Fatal("continued a turn that had no tool surface to act with")
		}
	})

	t.Run("a turn that called a tool is never continued", func(t *testing.T) {
		res := unactedRun(announced)
		res.History = append(res.History, sdkshape.Message{
			Role:      sdkshape.RoleAssistant,
			ToolCalls: []sdkshape.ToolCall{{ID: "call_1", Name: "read_file"}},
		})
		if turnLeftWorkUnacted(sdkagentloop.Options{}, toolsAdvertised(), "review the diff", res, nil) {
			t.Fatal("continued a turn that already ran a tool - it could duplicate work")
		}
	})

	t.Run("a plain answer is never continued", func(t *testing.T) {
		res := unactedRun("The module name is github.com/MiviaLabs/mivia-agent.")
		if turnLeftWorkUnacted(sdkagentloop.Options{}, toolsAdvertised(), "review the diff", res, nil) {
			t.Fatal("continued an ordinary answer")
		}
	})
}

// TestContinueUnactedTurnOffByDefault pins the opt-in: an operator who
// configured nothing gets today's behaviour, with no extra provider call.
func TestContinueUnactedTurnOffByDefault(t *testing.T) {
	res := unactedRun("I'll spawn 4 agents to review the diff.")
	opts := toolsAdvertised()
	opts.MaxUnactedContinuations = 0
	if turnLeftWorkUnacted(sdkagentloop.Options{}, opts, "review the diff", res, nil) != true {
		t.Fatal("the predicate itself should still match; the gate is on the count")
	}
	got, err := continueUnactedTurn(t.Context(), nil, sdkagentloop.Options{}, opts, nil, "review the diff", res, nil, nil)
	if err != nil || got.Stop != sdkagentloop.StopNoToolCalls {
		t.Fatalf("a zero bound must return the run untouched, got %+v / %v", got.Stop, err)
	}
}

// TestContinueUnactedTurnRespectsDisableProviderReplay pins that a caller
// which forbade provider replays gets none: a continuation is a replay.
func TestContinueUnactedTurnRespectsDisableProviderReplay(t *testing.T) {
	res := unactedRun("I'll spawn 4 agents to review the diff.")
	opts := toolsAdvertised()
	opts.DisableProviderReplay = true
	got, err := continueUnactedTurn(t.Context(), nil, sdkagentloop.Options{}, opts, nil, "review the diff", res, nil, nil)
	if err != nil || got.Stop != sdkagentloop.StopNoToolCalls {
		t.Fatalf("DisableProviderReplay must suppress the continuation, got %+v / %v", got.Stop, err)
	}
}

// TestContinuedHistoryAppendsTheNotice pins that the model keeps what it
// said (so it continues its own plan rather than restarting) and that the
// caller's slice is not aliased.
func TestContinuedHistoryAppendsTheNotice(t *testing.T) {
	original := unactedRun("I'll spawn 4 agents.").History
	got := continuedHistory(original)
	if len(got) != len(original)+1 {
		t.Fatalf("want one appended message, got %d from %d", len(got), len(original))
	}
	if got[len(got)-2].Content != "I'll spawn 4 agents." {
		t.Fatal("the assistant's own message must survive into the continuation")
	}
	last := got[len(got)-1]
	if last.Role != sdkshape.RoleUser || last.Content != unactedContinuationNotice {
		t.Fatalf("notice not appended as a user turn: %+v", last)
	}
	if len(original) != 2 {
		t.Fatal("the caller's history must not be mutated")
	}
}

// TestRunAdvertisedTools pins both sources of the tool-surface answer: the
// host-pinned snapshot when there is one, the SDK registry otherwise.
func TestRunAdvertisedTools(t *testing.T) {
	if runAdvertisedTools(sdkagentloop.Options{}, Options{}) {
		t.Fatal("no pinned snapshot and no registry means no tools")
	}
	registry := sdktools.New()
	if runAdvertisedTools(sdkagentloop.Options{Tools: registry}, Options{}) {
		t.Fatal("an empty registry means no tools")
	}
	if !runAdvertisedTools(sdkagentloop.Options{}, toolsAdvertised()) {
		t.Fatal("a non-empty pinned snapshot means tools were advertised")
	}
}
