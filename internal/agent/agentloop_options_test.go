package agent

// Tests for the B.2 #8 part 2 commit 3 surface: the tool-registry
// converter, the full options mapping, the request translator, and
// the RunAgentLoopOnce helper.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/usage"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestBuildAgentLoopOptionsPassesValidate asserts the full mapping
// produces Options that satisfy the SDK's Validate: completer
// wrapped, registry converted, MaxIterations positive.
func TestBuildAgentLoopOptionsPassesValidate(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, _, err := buildAgentLoopOptions(l, Options{MaxSteps: 5})
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("converted Options failed Validate: %v", err)
	}
}

// TestBuildAgentLoopOptionsFailClosed runs one subtest per CLI
// Options field the SDK path cannot carry. Each subtest asserts the
// error names the field so an opt-in caller learns the boundary.
func TestBuildAgentLoopOptionsFailClosed(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		// Surface moved to the carried table: bridgeSDKBridgeSurface maps it onto the SDK's own per-iteration Options.Surface (see docs/development/sdk-backend-field-mapping.md §1).
		// BeforeStep moved to the carried table: RunAgentLoopOnce
		// installs it as the SDK Steer's pull injector (see
		// docs/development/sdk-backend-field-mapping.md §1).
		// PreserveWorkLimits and the three token-reservation fields
		// (MaxPromptTokens, MaxOutputTokens, MaxOutputPerCall) moved
		// to the carried table too (Item 8): they ride the WorkBudget
		// bridge over the same workLimitMeter the legacy path uses.
		// MaxToolCalls stays fail-closed: the SDK path runs tool calls
		// through the converted registry, so the legacy
		// reserveToolBatch has no call point.
		{"WL.MaxToolCalls", Options{WorkLimits: runtime.WorkLimits{MaxToolCalls: 1}}, "WorkLimits.MaxToolCalls"},
	}
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := buildAgentLoopOptions(l, tt.opts)
			if err == nil {
				t.Fatalf("Options with %s set passed; want fail-closed error", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to name %q", err, tt.want)
			}
		})
	}
}

// TestTranslateRequestMapsFields asserts the SDK-to-CLI request
// translator: pass-through scalars, message conversion with tool
// calls, and the effort-to-level inverse.
func TestTranslateRequestMapsFields(t *testing.T) {
	req := sdkshape.Request{
		Model:     "m1",
		SessionID: "s1",
		Messages: []sdkshape.Message{
			{
				Role:    sdkshape.RoleAssistant,
				Content: "calling",
				ToolCalls: []sdkshape.ToolCall{
					{ID: "c1", Name: "read_file", Arguments: []byte(`{"path":"/x"}`)},
				},
			},
			{Role: sdkshape.RoleTool, Content: "result", ToolCallID: "c1"},
		},
		ReasoningEffort: sdkshape.ReasoningEffortHigh,
	}
	got := translateAgentLoopRequest(req)
	if got.Model != "m1" || got.SessionID != "s1" {
		t.Fatalf("Model/SessionID = %q/%q, want m1/s1", got.Model, got.SessionID)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(got.Messages))
	}
	first := got.Messages[0]
	if len(first.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(first.ToolCalls))
	}
	tc := first.ToolCalls[0]
	if tc.ID != "c1" || tc.Type != "function" || tc.Function.Name != "read_file" {
		t.Fatalf("ToolCall = %+v, want id c1 function read_file", tc)
	}
	if tc.Function.Arguments != `{"path":"/x"}` {
		t.Fatalf("Arguments = %q, want the raw JSON string", tc.Function.Arguments)
	}
	if got.ReasoningLevel != "high" {
		t.Fatalf("ReasoningLevel = %q, want high", got.ReasoningLevel)
	}
}

// TestRunAgentLoopOnceCompletesOneTurn is the end-to-end smoke: a
// fake completer whose ChatTurn returns plain content, an empty
// registry, MaxSteps 1. RunAgentLoopOnce must return the content as
// the final message and stop with the SDK's no-tool-calls reason.
func TestRunAgentLoopOnceCompletesOneTurn(t *testing.T) {
	l := &Loop{
		Completer: &fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "done", FinishReason: "stop"}},
		Tools:     tools.NewRegistry(),
	}
	res, err := RunAgentLoopOnce(context.Background(), l, Options{Model: "m", MaxSteps: 1}, nil)
	if err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}
	if res.Final.Content != "done" {
		t.Fatalf("Final.Content = %q, want %q", res.Final.Content, "done")
	}
	if res.Stop != sdkagentloop.StopNoToolCalls {
		t.Fatalf("Stop = %q, want %q", res.Stop, sdkagentloop.StopNoToolCalls)
	}
}

// TestRunAgentLoopOnceSteerTriggers asserts the steer bridge: a
// blocking completer plus a fired InterruptCh must let
// RunAgentLoopOnce return rather than hang. The test wraps the call
// in a 2-second guard; the assertion is no-hang, not a specific stop
// reason, because the steer may land before or during the first
// completion depending on scheduling.
func TestRunAgentLoopOnceSteerTriggers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	fired := make(chan struct{})
	close(fired)
	l := &Loop{
		Completer: &fakeCompleter{name: "fake", blocksChat: release, chatTurnOut: &provider.Response{Content: "x"}},
		Tools:     tools.NewRegistry(),
	}
	opts := Options{
		MaxSteps:    1,
		InterruptCh: func() <-chan struct{} { return fired },
	}
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = RunAgentLoopOnce(context.Background(), l, opts, nil)
	}()
	select {
	case <-done:
		if err != nil && !strings.Contains(err.Error(), "context") && err != context.Canceled {
			// A steer-abort error is acceptable; the contract is return-without-hang.
			t.Logf("RunAgentLoopOnce returned err (acceptable for steer test): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAgentLoopOnce hung for 2s; steer bridge did not unblock the call")
	}
}

// TestRunAgentLoopOnceMailboxPendingInterruptTriggers asserts the
// strict signal branch: a MailboxPendingInterrupt predicate that flips
// to true during a blocking chat call must let RunAgentLoopOnce return
// rather than hang. The guard is 2 seconds; the default poll interval
// is 250ms, plus a round of scheduling, so the steer should land well
// inside the budget.
func TestRunAgentLoopOnceMailboxPendingInterruptTriggers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var pendingFlag atomic.Bool
	pendingFlag.Store(false)
	l := &Loop{
		Completer: &fakeCompleter{
			name:        "fake",
			blocksChat:  release,
			chatTurnOut: &provider.Response{Content: "x"},
			onChatTurn:  func() { pendingFlag.Store(true) },
		},
		Tools: tools.NewRegistry(),
	}
	opts := Options{
		MaxSteps:                1,
		MailboxPendingInterrupt: func() bool { return pendingFlag.Load() },
	}
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = RunAgentLoopOnce(context.Background(), l, opts, nil)
	}()
	select {
	case <-done:
		if err != nil && !strings.Contains(err.Error(), "context") && err != context.Canceled {
			t.Logf("RunAgentLoopOnce returned err (acceptable for steer test): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAgentLoopOnce hung for 2s; MailboxPendingInterrupt steer did not unblock the call")
	}
}

// echoTool is the minimal CLI tool the event-translation test
// registers: its Execute returns a fixed string.
type echoTool struct{}

func (echoTool) Name() string               { return "echo" }
func (echoTool) Description() string        { return "echo" }
func (echoTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (echoTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "tool-result", nil
}

// TestRunAgentLoopOnceTranslatesEvents asserts the event bridge: a
// two-step SDK turn (one tool call, then a final answer) emits the
// CLI event sequence step, tool_start, tool_end through the caller's
// OnEvent - the same surface the legacy path publishes. The SDK's
// label payloads ride on Detail.
func TestRunAgentLoopOnceTranslatesEvents(t *testing.T) {
	call := provider.ToolCall{}
	call.Type = "function"
	call.Function.Name = "echo"
	call.Function.Arguments = "{}"
	comp := &scriptCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"},
		{Content: "final answer", FinishReason: "stop"},
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	l := &Loop{Completer: comp, Tools: reg}

	var mu sync.Mutex
	var got []Event
	opts := Options{
		Model:    "m",
		MaxSteps: 3,
		Backend:  "sdk",
		OnEvent: func(e Event) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	}
	out, err := l.Run(context.Background(), "hi", opts)
	if err != nil {
		t.Fatalf("Run(sdk): %v", err)
	}
	if out != "final answer" {
		t.Fatalf("Run output = %q, want %q", out, "final answer")
	}
	mu.Lock()
	defer mu.Unlock()
	var kinds []EventKind
	for _, e := range got {
		kinds = append(kinds, e.Kind)
	}
	for _, want := range []EventKind{EventStep, EventToolStart, EventToolEnd} {
		found := false
		for _, k := range kinds {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("event kinds %v missing %q", kinds, want)
		}
	}
	// The tool events carry the legacy queued/running Detail contract:
	// every tool_start names the tool on Name (Detail is "queued" or
	// "running", never the tool name), synthesized by the PointPreTool
	// hook and the dispatcher shim (sdk_tool_events.go).
	for _, e := range got {
		if e.Kind == EventToolStart && e.Name != "echo" {
			t.Fatalf("tool_start Name = %q (Detail %q), want echo", e.Name, e.Detail)
		}
		if e.Kind == EventToolStart && e.Detail != "queued" && e.Detail != "running" {
			t.Fatalf("tool_start Detail = %q, want queued or running", e.Detail)
		}
	}
}

// recordingUsageWriter records every UsageRecord it receives.
type recordingUsageWriter struct {
	records []usage.UsageRecord
}

func (f *recordingUsageWriter) Record(_ context.Context, r usage.UsageRecord) error {
	f.records = append(f.records, r)
	return nil
}

// TestRunAgentLoopOnceRecordsTokenUsage asserts the audit bridge: a
// completer turn with reported token usage yields one token_usage
// row carrying the provider-reported actuals, the same shape the
// legacy path's EmitTokenUsage writes.
func TestRunAgentLoopOnceRecordsTokenUsage(t *testing.T) {
	comp := &scriptCompleter{steps: []provider.Response{
		{Content: "done", FinishReason: "stop", TokenUsage: provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 40}},
	}}
	w := &recordingUsageWriter{}
	l := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	_, err := l.Run(context.Background(), "hi", Options{
		Model: "m", MaxSteps: 1, Backend: "sdk",
		SessionID: "s1", UsageWriter: w,
	})
	if err != nil {
		t.Fatalf("Run(sdk): %v", err)
	}
	if len(w.records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(w.records))
	}
	r := w.records[0]
	if r.Kind != "token_usage" || r.InputTokens != 100 || r.OutputTokens != 40 {
		t.Fatalf("record = %+v, want token_usage 100/40", r)
	}
	if r.SessionID != "s1" {
		t.Fatalf("SessionID = %q, want s1 (recordUsage stamps it)", r.SessionID)
	}
}

// TestRunAgentLoopOnceWritesFinalText asserts FinalWriter receives
// the final assistant text after a graceful stop.
func TestRunAgentLoopOnceWritesFinalText(t *testing.T) {
	comp := &scriptCompleter{steps: []provider.Response{
		{Content: "final answer", FinishReason: "stop"},
	}}
	var buf bytes.Buffer
	l := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	_, err := l.Run(context.Background(), "hi", Options{
		Model: "m", MaxSteps: 1, Backend: "sdk", FinalWriter: &buf,
	})
	if err != nil {
		t.Fatalf("Run(sdk): %v", err)
	}
	if buf.String() != "final answer" {
		t.Fatalf("FinalWriter got %q, want %q", buf.String(), "final answer")
	}
}

// TestRunAgentLoopOnceRequireFinalTextFailsEmpty asserts the
// empty-turn refusal: RequireFinalText with a turn that produced no
// assistant text anywhere returns an error, matching the legacy
// surface's loud-failure contract.
func TestRunAgentLoopOnceRequireFinalTextFailsEmpty(t *testing.T) {
	comp := &scriptCompleter{steps: []provider.Response{
		{Content: "", FinishReason: "stop"},
	}}
	l := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	_, err := l.Run(context.Background(), "hi", Options{
		Model: "m", MaxSteps: 1, Backend: "sdk", RequireFinalText: true,
	})
	if err == nil {
		t.Fatal("RequireFinalText with empty turn returned nil error; want refusal")
	}
	if !strings.Contains(err.Error(), "no assistant text") {
		t.Fatalf("err = %v, want it to name the empty turn", err)
	}
}

// TestBuildAgentLoopOptionsWorkLimitsTurnsClamp asserts the MaxTurns
// clamp mirrors the legacy rule exactly: the test reads MaxSteps
// pre-default, so an unset MaxSteps takes ANY positive turn limit as
// the bound (even above the default 25), and a set MaxSteps takes
// the tighter of the two.
func TestBuildAgentLoopOptionsWorkLimitsTurnsClamp(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "f"}, Tools: tools.NewRegistry()}
	cases := []struct {
		name     string
		maxSteps int
		maxTurns int
		want     int
	}{
		{"unset steps take any limit", 0, 30, 30},
		{"unset steps take small limit", 0, 3, 3},
		{"tighter limit wins", 5, 3, 3},
		{"configured steps win when tighter", 3, 10, 3},
		{"zero limit leaves unbounded", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := buildAgentLoopOptions(l, Options{MaxSteps: tc.maxSteps, WorkLimits: runtime.WorkLimits{MaxTurns: tc.maxTurns}})
			if err != nil {
				t.Fatalf("buildAgentLoopOptions: %v", err)
			}
			if got.MaxIterations != tc.want {
				t.Fatalf("MaxIterations = %d, want %d", got.MaxIterations, tc.want)
			}
		})
	}
}

// TestRunAgentLoopOnceDeadlineExpiry asserts a past WorkLimits
// deadline fails the turn promptly instead of reaching the completer:
// the deadline narrowing wraps the context before the SDK loop runs.
func TestRunAgentLoopOnceDeadlineExpiry(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	l := &Loop{Completer: &fakeCompleter{name: "f", blocksChat: release}, Tools: tools.NewRegistry()}
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = RunAgentLoopOnce(context.Background(), l, Options{
			MaxSteps: 1,
			WorkLimits: runtime.WorkLimits{
				DeadlineAt: time.Now().Add(-1 * time.Second),
			},
		}, nil)
	}()
	select {
	case <-done:
		if err == nil {
			t.Fatal("past deadline returned nil error; want deadline-exceeded failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunAgentLoopOnce hung for 2s with a past deadline")
	}
}

// stubPreparationManager satisfies contextmgr.PreparationManager with
// a fixed compaction outcome: it drops everything past a configurable
// keep-first-N count. The point of the test is that the SDK path
// pre-compacts through it before the SDK loop runs.
type stubPreparationManager struct {
	keep int
}

func (s *stubPreparationManager) Prepare(_ context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	kept := in.Messages
	if len(kept) > s.keep {
		kept = kept[:s.keep]
	}
	return contextmgr.Preparation{Messages: append([]provider.Message(nil), kept...), Compacted: true}, nil
}
func (s *stubPreparationManager) Discard(_ contextmgr.Preparation) {}

// TestPrepareSDKHistoryCompactsThroughManager asserts that when a
// PreparationManager is wired and MaxContextTokens is set, the SDK
// path pre-compacts the loop's history through the manager before
// handing the messages to the SDK. The test reads the messages the
// SDK saw by checking the request the wrapped completer received.
func TestPrepareSDKHistoryCompactsThroughManager(t *testing.T) {
	var seen int
	cap := &captureCompleter{
		fakeCompleter: fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "ok", FinishReason: "stop"}},
		onTurn: func(req provider.Request) {
			seen = len(req.Messages)
		},
	}
	l := &Loop{Completer: cap, Tools: tools.NewRegistry()}
	in := []provider.Message{
		{Role: provider.RoleUser, Content: "m1"},
		{Role: provider.RoleAssistant, Content: "a1"},
		{Role: provider.RoleUser, Content: "m2"},
		{Role: provider.RoleAssistant, Content: "a2"},
		{Role: provider.RoleUser, Content: "m3"},
	}
	opts := Options{
		Model:              "m",
		MaxSteps:           1,
		MaxContextTokens:   100,
		PreparationManager: &stubPreparationManager{keep: 3},
	}
	if _, err := RunAgentLoopOnce(context.Background(), l, opts, in); err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}
	if seen != 3 {
		t.Fatalf("completer saw %d messages; want 3 (the manager's keep count)", seen)
	}
}

// TestPrepareSDKHistoryNoManagerPassesThrough asserts that a nil
// PreparationManager leaves the messages unchanged (the SDK's
// per-call Budget still bounds one call).
func TestPrepareSDKHistoryNoManagerPassesThrough(t *testing.T) {
	var seen int
	cap := &captureCompleter{
		fakeCompleter: fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "ok", FinishReason: "stop"}},
		onTurn: func(req provider.Request) {
			seen = len(req.Messages)
		},
	}
	l := &Loop{Completer: cap, Tools: tools.NewRegistry()}
	in := []provider.Message{
		{Role: provider.RoleUser, Content: "m1"},
		{Role: provider.RoleUser, Content: "m2"},
	}
	if _, err := RunAgentLoopOnce(context.Background(), l, Options{Model: "m", MaxSteps: 1}, in); err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}
	if seen != 2 {
		t.Fatalf("completer saw %d messages; want 2 (unchanged)", seen)
	}
}

// captureCompleter wraps fakeCompleter with an onTurn observer that
// captures the request's message count. It exists to assert the
// pre-compaction path without exposing internal state.
type captureCompleter struct {
	fakeCompleter
	onTurn func(provider.Request)
}

func (c *captureCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if c.onTurn != nil {
		c.onTurn(req)
	}
	return c.chatTurnOut, c.chatTurnErr
}

// compactAndSummarizeManager marks every Prepare outcome as
// Compacted so injectSummary runs on the SDK path. The default
// stubPreparationManager only sets Compacted when the message slice
// shrinks; this one always sets it, simulating a structural-pruning
// pass that always has something to summarize.
type compactAndSummarizeManager struct{}

func (c *compactAndSummarizeManager) Prepare(_ context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: in.Principal.SessionID, Sequence: in.Revision.Source},
		End:   contextstate.SourceID{SessionID: in.Principal.SessionID, Sequence: in.Revision.Source},
	}
	return contextmgr.CapturePreparation(in, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, in.Messages, true, "sdk-summary-test")
}

func (c *compactAndSummarizeManager) Discard(_ contextmgr.Preparation) {}

// stubSummaryProvider returns a fixed valid Summary so the CLI's
// NewSummarizer can be built without a real provider. The test
// inspects Loop.InjectedSummary() rather than the wire result.
type stubSummaryProvider struct{}

func (*stubSummaryProvider) Summarize(_ context.Context, request contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	return contextmgr.Summary{
		Version:     request.Input.Version,
		Objective:   "obj",
		State:       request.Input.State,
		SourceRange: request.SourceRange,
	}, nil
}

// TestPrepareSDKHistoryInjectsSummary asserts that when both
// PreparationManager and SummaryConfig.Summarizer are wired, the
// SDK path produces a final history whose tail message carries the
// SummaryMessageName (the summary injection frame). The completer
// records the last message it received so the test can read the
// injection directly.
func TestPrepareSDKHistoryInjectsSummary(t *testing.T) {
	var lastName string
	cap := &captureCompleter{
		fakeCompleter: fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "ok", FinishReason: "stop"}},
		onTurn: func(req provider.Request) {
			if len(req.Messages) > 0 {
				lastName = req.Messages[len(req.Messages)-1].Name
			}
		},
	}
	l := &Loop{Completer: cap, Tools: tools.NewRegistry()}
	summ := summaryInjectSummarizer(t, &stubSummaryProvider{})
	in := []provider.Message{
		{Role: provider.RoleUser, Content: "m1"},
		{Role: provider.RoleAssistant, Content: "a1"},
		{Role: provider.RoleUser, Content: "m2"},
		{Role: provider.RoleAssistant, Content: "a2"},
		{Role: provider.RoleUser, Content: "m3"},
	}
	principal, perr := contextstate.NewPrincipal("workspace", "session", "subject")
	if perr != nil {
		t.Fatalf("NewPrincipal: %v", perr)
	}
	binding := mustBinding(t)
	opts := Options{
		Model: "model", MaxSteps: 1, MaxContextTokens: 100,
		PreparationManager: &compactAndSummarizeManager{},
		PreparationInput: contextmgr.PrepareInput{
			Budget: 100, Principal: principal, Binding: binding,
			Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
		SummaryConfig: SummaryConfig{
			Summarizer: &summ,
			Redaction:  contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}},
		},
	}
	_, err := RunAgentLoopOnce(context.Background(), l, opts, in)
	if err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}
	if lastName != SummaryMessageName {
		t.Fatalf("last message name = %q, want %q", lastName, SummaryMessageName)
	}
}

// TestPrepareSDKHistoryNoSummarizerCompactsOnly asserts that a
// non-nil PreparationManager without a Summarizer compacts the
// messages but does NOT append a summary frame at the tail.
func TestPrepareSDKHistoryNoSummarizerCompactsOnly(t *testing.T) {
	var lastName string
	cap := &captureCompleter{
		fakeCompleter: fakeCompleter{name: "fake", chatTurnOut: &provider.Response{Content: "ok", FinishReason: "stop"}},
		onTurn: func(req provider.Request) {
			if len(req.Messages) > 0 {
				lastName = req.Messages[len(req.Messages)-1].Name
			}
		},
	}
	l := &Loop{Completer: cap, Tools: tools.NewRegistry()}
	principal, perr := contextstate.NewPrincipal("workspace", "session", "subject")
	if perr != nil {
		t.Fatalf("NewPrincipal: %v", perr)
	}
	binding := mustBinding(t)
	in := []provider.Message{
		{Role: provider.RoleUser, Content: "m1"},
		{Role: provider.RoleAssistant, Content: "a1"},
		{Role: provider.RoleUser, Content: "m2"},
		{Role: provider.RoleAssistant, Content: "a2"},
		{Role: provider.RoleUser, Content: "m3"},
	}
	opts := Options{
		Model: "model", MaxSteps: 1, MaxContextTokens: 100,
		PreparationManager: &compactAndSummarizeManager{},
		PreparationInput: contextmgr.PrepareInput{
			Budget: 100, Principal: principal, Binding: binding,
			Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
		// SummaryConfig.Summarizer left nil.
	}
	if _, err := RunAgentLoopOnce(context.Background(), l, opts, in); err != nil {
		t.Fatalf("RunAgentLoopOnce: %v", err)
	}
	if lastName == SummaryMessageName {
		t.Fatalf("last message name = %q; expected no summary injection without a Summarizer", lastName)
	}
}

func mustBinding(t *testing.T) contextstate.BindingRevision {
	t.Helper()
	b, err := contextstate.NewBindingRevision("summary-test", "model", 1)
	if err != nil {
		t.Fatalf("NewBindingRevision: %v", err)
	}
	return b
}

// TestBuildAgentLoopOptionsCarriesMaxConcurrentTools asserts the host
// Options.MaxConcurrentTools is forwarded to SDK agentloop.Options.
func TestBuildAgentLoopOptionsCarriesMaxConcurrentTools(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: tools.NewRegistry()}
	got, _, err := buildAgentLoopOptions(l, Options{MaxSteps: 5, MaxConcurrentTools: 4})
	if err != nil {
		t.Fatalf("buildAgentLoopOptions: %v", err)
	}
	if got.MaxConcurrentTools != 4 {
		t.Fatalf("MaxConcurrentTools = %d, want 4", got.MaxConcurrentTools)
	}
}
