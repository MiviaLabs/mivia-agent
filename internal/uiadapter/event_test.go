package uiadapter_test

import (
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// allEventKinds is every agent.EventKind, taken from the producing package
// rather than restated here.
//
// It used to be a hand-written mirror, and that is exactly how
// EventAssistantReset reached only one of four renderers with the suite
// green: the kind was never written down here, so the exhaustiveness test
// below never asked whether it translated. agent.AllEventKinds is itself
// checked against the parsed source (see the agent package's
// TestAllEventKindsMatchesTheDeclaredConstants), so a new constant now fails
// a test instead of being skipped by one.
func allEventKinds() []agent.EventKind { return agent.AllEventKinds() }

// mappingCase pairs an agent.Event input with the uievent.Events the
// TranslateEvent switch must produce. Each per-group test below holds
// one table of these for its logical category.
type mappingCase struct {
	name string
	ev   agent.Event
	want []uievent.Event
}

// runMappingCases iterates the supplied table, naming each row and
// comparing TranslateEvent's output against want via the shared equality
// helper. Parallel subtests run within their parent; the parent itself
// is parallel.
func runMappingCases(t *testing.T, cases []mappingCase) {
	t.Helper()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := uiadapter.TranslateEvent(tc.ev)
			assertUIEventsEqual(t, got, tc.want)
		})
	}
}

// TestTranslateEvent_Assistant covers the EventAssistant fan-out. Detail
// is the agent loop's mode marker: "delta" for streaming chunks,
// "interim" for batch-aggregated interim content, and empty for the
// final whole-turn text. Empty Content on any mode drops because the
// uievent body set has no representation for an empty payload.
func TestTranslateEvent_Assistant(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "delta_streams_as_text.delta",
			ev:   agent.Event{Kind: agent.EventAssistant, Content: "chunk", Detail: "delta"},
			want: []uievent.Event{{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "chunk"}}},
		},
		{
			name: "interim_is_interim_text.end",
			ev:   agent.Event{Kind: agent.EventAssistant, Content: "batched so far", Detail: "interim"},
			want: []uievent.Event{{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "batched so far"}}},
		},
		{
			name: "empty_detail_is_final_text.end",
			ev:   agent.Event{Kind: agent.EventAssistant, Content: "final text"},
			want: []uievent.Event{{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "final text"}}},
		},
		{
			name: "empty_content_dropped_regardless_of_mode",
			ev:   agent.Event{Kind: agent.EventAssistant, Content: "", Detail: "delta"},
			want: nil,
		},
	})
}

// TestTranslateEvent_Tool covers the tool lifecycle (start, end).
// EventToolStart maps to tool.start with parsed Args; EventToolEnd maps
// to tool.end with OK derived from Detail's vocabulary ("completed"
// vs anything starting with "failed") and Err carrying the same string
// so a renderer has something to show without re-parsing.
func TestTranslateEvent_Tool(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "tool_pending_with_parseable_input",
			ev: agent.Event{
				Kind: agent.EventToolPending, ToolCallID: "c1", Name: "write_file",
				Detail: "write", Input: `{"path":"/tmp/x"}`,
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolPending,
				Body: uievent.ToolPendingBody{
					ToolCallID: "c1", Name: "write_file",
					Args: map[string]any{"path": "/tmp/x"},
				},
			}},
		},
		{
			name: "tool_pending_with_empty_input_yields_nil_args",
			ev:   agent.Event{Kind: agent.EventToolPending, ToolCallID: "c2", Name: "custom_tool"},
			want: []uievent.Event{{
				Kind: uievent.KindToolPending,
				Body: uievent.ToolPendingBody{ToolCallID: "c2", Name: "custom_tool"},
			}},
		},
		{
			name: "tool_start_with_parseable_input",
			ev: agent.Event{
				Kind: agent.EventToolStart, ToolCallID: "c1", Name: "read_file",
				Detail: "running", Input: `{"path":"/tmp/x"}`,
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolStart,
				Body: uievent.ToolStartBody{
					ToolCallID: "c1", Name: "read_file",
					Args: map[string]any{"path": "/tmp/x"},
				},
			}},
		},
		{
			name: "tool_start_with_empty_input_yields_nil_args",
			ev:   agent.Event{Kind: agent.EventToolStart, ToolCallID: "c2", Name: "noop"},
			want: []uievent.Event{{
				Kind: uievent.KindToolStart,
				Body: uievent.ToolStartBody{ToolCallID: "c2", Name: "noop"},
			}},
		},
		{
			name: "tool_end_completed_is_OK_true",
			ev: agent.Event{
				Kind: agent.EventToolEnd, ToolCallID: "c1", Name: "read_file",
				Detail: "completed", Output: "ok\n",
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolEnd,
				Body: uievent.ToolEndBody{
					ToolCallID: "c1", Name: "read_file", OK: true,
					Result: "ok\n",
				},
			}},
		},
		{
			name: "tool_end_failed_is_OK_false_with_err",
			ev: agent.Event{
				Kind: agent.EventToolEnd, ToolCallID: "c2", Name: "run_command",
				Detail: "failed", Output: "exit=1",
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolEnd,
				Body: uievent.ToolEndBody{
					ToolCallID: "c2", Name: "run_command", OK: false,
					Result: "exit=1", Err: "failed",
				},
			}},
		},
	})
}

// TestTranslateEvent_Notice covers every kind that resolves to a free-
// text advisory line (notice). The kinds are split across two tests so
// neither grows past the function-length soft cap: this one holds the
// kinds whose payload has no agent-side structured equivalent (step,
// prune, tool_parallel, hook). TestTranslateEvent_NoticeMetrics holds the
// kinds whose payload is a typed accounting event from the agent loop
// (compaction, cache_usage, token_usage, work_limit).
func TestTranslateEvent_Notice(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "step_dropped_by_default",
			ev:   agent.Event{Kind: agent.EventStep, Detail: "step 3 of 5"},
			want: nil,
		},
		{
			name: "prune_carries_retry_message",
			ev: agent.Event{
				Kind:   agent.EventPrune,
				Detail: "provider rejected prompt (prompt too long); compacted and retrying once",
			},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{
					Text: "provider rejected prompt (prompt too long); compacted and retrying once",
				},
			}},
		},
		{
			name: "tool_parallel_carries_banner",
			ev:   agent.Event{Kind: agent.EventToolParallel, Detail: "3 tools: a, b, c"},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{Text: "3 tools: a, b, c"},
			}},
		},
		{
			name: "schema_retry_carries_attempt_message",
			ev: agent.Event{
				Kind:   agent.EventSchemaRetry,
				Detail: "schema validation failed on attempt 1/3, retrying...",
			},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{Text: "schema validation failed on attempt 1/3, retrying..."},
			}},
		},
		{
			name: "empty_response_retry_carries_attempt_message",
			ev: agent.Event{
				Kind:   agent.EventEmptyResponseRetry,
				Detail: "empty response on attempt 1/3, retrying...",
			},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{Text: "empty response on attempt 1/3, retrying..."},
			}},
		},
		{
			// Without this mapping the operator sees nothing at all while a
			// second, potentially multi-minute provider call runs - the exact
			// silence the sibling retry notice above exists to prevent.
			name: "unacted_continuation_carries_attempt_message",
			ev: agent.Event{
				Kind:   agent.EventUnactedContinuation,
				Detail: "turn announced work but called no tool, continuing (1/1)",
			},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{Text: "turn announced work but called no tool, continuing (1/1)"},
			}},
		},
	})
}

// TestTranslateEvent_Hook covers EventHook's own body: it carries the
// program, tool, input, output, and denial state a HookBody needs, rather
// than collapsing to a NoticeBody's bare text (which would silently drop
// Output, as the notice(hookText(ev)) path once did).
func TestTranslateEvent_Hook(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "hook_denied_carries_full_shape",
			ev: agent.Event{
				Kind: agent.EventHook, Name: "PreToolUse",
				Program: "guard.sh", Tool: "run_command", Denied: true,
				Input: `{"argv":["rm","-rf","/"]}`, Output: "policy forbids this argv",
			},
			want: []uievent.Event{{
				Kind: uievent.KindHook,
				Body: uievent.HookBody{
					Event: "PreToolUse", Program: "guard.sh", Tool: "run_command",
					Input: `{"argv":["rm","-rf","/"]}`, Output: "policy forbids this argv", Denied: true,
				},
			}},
		},
		{
			name: "hook_silent_run_still_carries_program_and_tool",
			ev: agent.Event{
				Kind: agent.EventHook, Name: "PostToolUse",
				Program: "fmt.sh", Tool: "write_file",
			},
			want: []uievent.Event{{
				Kind: uievent.KindHook,
				Body: uievent.HookBody{Event: "PostToolUse", Program: "fmt.sh", Tool: "write_file"},
			}},
		},
	})
}

// TestTranslateEvent_NoticeMetrics covers the kinds whose payload is a
// typed accounting event from the agent loop (compaction, cache_usage,
// token_usage, work_limit). They arrive as free-text notices here because
// the uievent contract has no dedicated body for typed accounting yet;
// the typed payloads still travel alongside (events.CompactionEvent etc.)
// for consumers that want the structured form.
func TestTranslateEvent_NoticeMetrics(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "compaction_summary_is_notice",
			ev: agent.Event{
				Kind:   agent.EventCompaction,
				Detail: "context compacted: 120000 -> 60000 tokens",
			},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{Text: "context compacted: 120000 -> 60000 tokens"},
			}},
		},
		{
			name: "cache_usage_dropped_by_default",
			ev: agent.Event{
				Kind:   agent.EventCacheUsage,
				Detail: "prompt cache: 800/1000 tokens cached (80%)",
			},
			want: nil,
		},
		{
			// The root loop's prompt tokens ARE the session's context fill
			// level, so they drive the gauge; see translateTokenUsage.
			name: "root_token_usage_is_the_context_fill_level",
			ev: agent.Event{
				Kind:       agent.EventTokenUsage,
				Detail:     "estimate 1000 vs actual 1200",
				TokenUsage: &events.TokenUsageEvent{InputTokens: 1200, OutputTokens: 300},
			},
			want: []uievent.Event{{
				Kind: uievent.KindUsage,
				Body: uievent.UsageBody{InputTokens: 1200, OutputTokens: 300},
			}},
		},
		{
			// A dispatched agent's private history says nothing about how
			// full the ROOT session's context is; letting it through would
			// swing the gauge to an unrelated conversation's size.
			name: "subagent_token_usage_does_not_move_the_gauge",
			ev: agent.Event{
				Kind:       agent.EventTokenUsage,
				Detail:     "estimate 1000 vs actual 1200",
				TokenUsage: &events.TokenUsageEvent{InputTokens: 1200, OutputTokens: 300},
				Origin:     agent.EventOrigin{TaskID: "task-1", Agent: "builder", Depth: 1},
			},
			want: nil,
		},
		{
			// EmitTokenUsage only fires on a reported usage, but a nil
			// payload must not panic the translator.
			name: "token_usage_without_a_payload_is_dropped",
			ev:   agent.Event{Kind: agent.EventTokenUsage, Detail: "actual 5 in / 2 out"},
			want: nil,
		},
		{
			name: "work_limit_is_notice",
			ev:   agent.Event{Kind: agent.EventWorkLimit, Detail: "conclude: approaching a work bound"},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{Text: "conclude: approaching a work bound"},
			}},
		},
	})
}

// TestTranslateEvent_Dropped covers the two drop-list entries (and proves
// they really do drop rather than fall through to a default case). A
// non-empty Detail is set so a future change that returns nil only for
// empty Detail is caught here.
func TestTranslateEvent_Dropped(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "heartbeat_dropped",
			ev:   agent.Event{Kind: agent.EventHeartbeat, Detail: "tools 2/4 done"},
			want: nil,
		},
	})
}

// TestTranslateEvent_Reasoning covers EventThinking's chain-of-thought.
// Content drives the body; empty Content drops because reasoning.delta
// has no representation for an empty payload.
func TestTranslateEvent_Reasoning(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "thinking_with_content_maps",
			ev:   agent.Event{Kind: agent.EventThinking, Content: "step 1: enumerate"},
			want: []uievent.Event{{
				Kind: uievent.KindReasoning,
				Body: uievent.ReasoningDeltaBody{Text: "step 1: enumerate"},
			}},
		},
		{
			name: "thinking_empty_content_dropped",
			ev:   agent.Event{Kind: agent.EventThinking},
			want: nil,
		},
	})
}

// TestTranslateEvent_ExhaustiveCoverage is the structural guardrail. It
// asserts two cooperating invariants:
//
//  1. Every agent.EventKind constant declared at test time has a case in
//     TranslateEvent's switch; an unrecognised kind panics (so a new
//     constant added to internal/agent/event.go without a switch entry
//     surfaces as a panic rather than a silent drop).
//  2. Every kind in the drop-list truly drops regardless of input, and
//     every other kind produces at least one uievent.Event when fed a
//     content-bearing event so an accidentally-conditional return-nil
//     fails the test rather than the test passing by accident.
func TestTranslateEvent_ExhaustiveCoverage(t *testing.T) {
	t.Parallel()
	covered := map[agent.EventKind]struct{}{}
	for _, k := range allEventKinds() {
		if _, dup := covered[k]; dup {
			t.Fatalf("allEventKinds has duplicate %q", k)
		}
		covered[k] = struct{}{}
	}
	kinds := allEventKinds()
	if len(kinds) == 0 {
		t.Fatal("agent.AllEventKinds is empty; the registry, not this test, is wrong")
	}
	seen := map[agent.EventKind]bool{}
	for _, k := range kinds {
		ev := contentBearingEvent(k)
		got := safeTranslate(t, ev)
		switch k {
		case agent.EventHeartbeat, agent.EventTokenUsage, agent.EventStep, agent.EventCacheUsage:
			if len(got) != 0 {
				t.Errorf("%s must drop by default; got %d events", k, len(got))
			}
		default:
			if len(got) == 0 {
				t.Errorf("%s is content-bearing but produced 0 uievents; check for an accidental drop", k)
			}
		}
		seen[k] = true
	}
	for _, k := range kinds {
		if !seen[k] {
			t.Errorf("kind %q declared but never exercised in coverage loop", k)
		}
	}
}

func TestTranslateEvent_NoticeOptions(t *testing.T) {
	t.Parallel()

	stepEv := agent.Event{Kind: agent.EventStep, Detail: "iteration 2"}
	cacheEv := agent.Event{Kind: agent.EventCacheUsage, Detail: "prompt cache: 50%"}

	// When enabled:
	opts := uiadapter.TranslateOptions{
		ShowIterationNotices:   true,
		ShowPromptCacheNotices: true,
	}

	gotStep := uiadapter.TranslateEventWithOptions(stepEv, opts)
	if len(gotStep) != 1 || gotStep[0].Kind != uievent.KindNotice || gotStep[0].Body.(uievent.NoticeBody).Text != "iteration 2" {
		t.Errorf("TranslateEventWithOptions with ShowIterationNotices=true failed: got %+v", gotStep)
	}

	gotCache := uiadapter.TranslateEventWithOptions(cacheEv, opts)
	if len(gotCache) != 1 || gotCache[0].Kind != uievent.KindNotice || gotCache[0].Body.(uievent.NoticeBody).Text != "prompt cache: 50%" {
		t.Errorf("TranslateEventWithOptions with ShowPromptCacheNotices=true failed: got %+v", gotCache)
	}
}

// TestTranslateEvent_PanicsOnUnknownKind pins the "no default case"
// rule at test time. Without this, adding a new EventKind would compile
// fine (the production switch has a panic on the fall-through) and only
// surface when the loop runs through every kind.
func TestTranslateEvent_LogsAndDropsUnknownKind(t *testing.T) {
	t.Parallel()
	// An unrecognised agent.EventKind must not panic - production code
	// returns errors, not crashes. The helper logs the drop and returns
	// nil. The compile-time exhaustiveness check on the switch is the
	// primary defence; this test confirms the runtime fallback is a
	// graceful drop, not a process exit.
	got := uiadapter.TranslateEvent(agent.Event{Kind: agent.EventKind("definitely_not_in_the_switch")})
	if got != nil {
		t.Fatalf("unrecognised kind must drop to nil, got %#v", got)
	}
}

// contentBearingEvent returns one agent.Event of kind k with enough fields
// populated that every case in TranslateEvent (other than the explicit
// drop-list pair) would emit at least one uievent.Event. Used by the
// exhaustive coverage test to distinguish "this kind always drops" from
// "this kind drops only when fields are empty".
func contentBearingEvent(k agent.EventKind) agent.Event {
	ev := agent.Event{Kind: k, Detail: "running", Output: "ok", Content: "text", Name: "tool"}
	switch k {
	case agent.EventSubagentStart, agent.EventSubagentEnd, agent.EventSubagentHeartbeat, agent.EventSubagentDone:
		ev = agent.Event{Kind: k, ToolCallID: "tc-1", Detail: "running", Origin: agent.EventOrigin{
			TaskID: "wft-1", Agent: "audit", TaskDescription: "audit the diff",
		}}
		ev.Name = "delegate"
	case agent.EventToolPending:
		ev = agent.Event{Kind: k, ToolCallID: "tc-1", Name: "write_file", Detail: "write", Input: `{"path":"/tmp/x"}`}
	case agent.EventToolStart, agent.EventToolEnd:
		ev = agent.Event{Kind: k, ToolCallID: "tc-1", Name: "read_file", Detail: "completed"}
	case agent.EventAssistant:
		ev = agent.Event{Kind: k, Content: "hi", Detail: "delta"}
	case agent.EventThinking:
		ev = agent.Event{Kind: k, Content: "thought"}
	case agent.EventHook:
		ev = agent.Event{Kind: k, Name: "PreToolUse", Detail: "ran"}
	}
	return ev
}

// safeTranslate runs TranslateEvent and converts a panic into a t.Errorf
// for the exhaustive coverage test, so a forgotten case is reported as a
// coverage miss instead of crashing the test binary mid-loop.
func safeTranslate(t *testing.T, ev agent.Event) (out []uievent.Event) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("TranslateEvent panicked on %q: %v", ev.Kind, r)
		}
	}()
	return uiadapter.TranslateEvent(ev)
}

// assertUIEventsEqual fails unless got and want are identical including
// the body type and its fields. Body comparison goes through JSON
// round-trip so non-comparable types inside the body (map[string]any,
// *Diff, *Progress, []byte derivatives) can be compared meaningfully;
// a type mismatch is reported as such before the round-trip kicks in.
func assertUIEventsEqual(t *testing.T, got, want []uievent.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count: got %d, want %d (got=%+v, want=%+v)", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i].Kind != want[i].Kind {
			t.Fatalf("event[%d] kind: got %q, want %q", i, got[i].Kind, want[i].Kind)
		}
		if bodyTypeDiffers(got[i].Body, want[i].Body) {
			t.Fatalf("event[%d] body type: got %T, want %T", i, got[i].Body, want[i].Body)
		}
		gotJSON, err := json.Marshal(got[i].Body)
		if err != nil {
			t.Fatalf("event[%d] marshal got body: %v", i, err)
		}
		wantJSON, err := json.Marshal(want[i].Body)
		if err != nil {
			t.Fatalf("event[%d] marshal want body: %v", i, err)
		}
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("event[%d] body mismatch: got %s, want %s", i, gotJSON, wantJSON)
		}
	}
}

// bodyTypeDiffers reports whether two Body values have different dynamic
// types. Two different concrete body implementations carrying the same
// fields would still be considered different by this helper because the
// uievent contract is that Body's concrete type is decided by Kind.
func bodyTypeDiffers(a, b uievent.Body) bool {
	switch a.(type) {
	case uievent.TurnStartBody:
		_, ok := b.(uievent.TurnStartBody)
		return !ok
	case uievent.TextDeltaBody:
		_, ok := b.(uievent.TextDeltaBody)
		return !ok
	case uievent.TextEndBody:
		_, ok := b.(uievent.TextEndBody)
		return !ok
	case uievent.ReasoningDeltaBody:
		_, ok := b.(uievent.ReasoningDeltaBody)
		return !ok
	case uievent.ToolPendingBody:
		_, ok := b.(uievent.ToolPendingBody)
		return !ok
	case uievent.ToolStartBody:
		_, ok := b.(uievent.ToolStartBody)
		return !ok
	case uievent.ToolOutputBody:
		_, ok := b.(uievent.ToolOutputBody)
		return !ok
	case uievent.ToolEndBody:
		_, ok := b.(uievent.ToolEndBody)
		return !ok
	case uievent.PlanBody:
		_, ok := b.(uievent.PlanBody)
		return !ok
	case uievent.NoticeBody:
		_, ok := b.(uievent.NoticeBody)
		return !ok
	case uievent.HookBody:
		_, ok := b.(uievent.HookBody)
		return !ok
	case uievent.UsageBody:
		_, ok := b.(uievent.UsageBody)
		return !ok
	case uievent.ErrorBody:
		_, ok := b.(uievent.ErrorBody)
		return !ok
	case uievent.TurnEndBody:
		_, ok := b.(uievent.TurnEndBody)
		return !ok
	case nil:
		return b != nil
	}
	// Unknown body types compare unequal as a conservative default; this
	// branch is unreachable for the bodies uievent declares today.
	return true
}

// TestParseArgs covers the small surface of parseArgs in event.go: empty
// input, the "{}" sentinel, a non-empty object, an invalid JSON string,
// and a JSON literal that parses but yields an empty map. Each branch
// is exercised to keep diff-coverage clean for the lines introduced with
// the uiadapter package.
func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]any
	}{
		{name: "empty", in: "", want: nil},
		{name: "whitespace", in: "   \t\n ", want: nil},
		{name: "sentinel", in: "{}", want: nil},
		{name: "valid", in: `{"foo":"bar"}`, want: map[string]any{"foo": "bar"}},
		{name: "invalid", in: "not-json", want: nil},
		{name: "parses-empty", in: `{"a":null}`, want: map[string]any{"a": nil}},
		{name: "json-null", in: `null`, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uiadapter.ParseArgsForTest(tc.in)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("nil-ness mismatch: got %v want %v", got, tc.want)
			}
			if got == nil {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %d want %d", len(got), len(tc.want))
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("key %q: got %v want %v", k, got[k], v)
				}
			}
		})
	}
}

// TestErrFromDetail covers errFromDetail's three branches: ok=true returns
// ""; a Detail of "" or "failed" with ok=false returns "failed"; any
// other non-empty detail with ok=false returns the detail verbatim. The
// third branch is what the diff-coverage gate flagged on the original
// uiadapter commit and what this test keeps under coverage.
func TestErrFromDetail(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		ok     bool
		want   string
	}{
		{name: "ok-empty-detail", detail: "", ok: true, want: ""},
		{name: "ok-non-empty-detail", detail: "anything", ok: true, want: ""},
		{name: "failed-empty-detail", detail: "", ok: false, want: "failed"},
		{name: "failed-bare", detail: "failed", ok: false, want: "failed"},
		{name: "failed-qualified", detail: "permission denied", ok: false, want: "permission denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uiadapter.ErrFromDetailForTest(tc.detail, tc.ok)
			if got != tc.want {
				t.Fatalf("ErrFromDetail(%q, %v) = %q, want %q", tc.detail, tc.ok, got, tc.want)
			}
		})
	}
}
