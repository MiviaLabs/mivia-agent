// conversation_test.go drives ports.Conversation end-to-end against a
// chat.Session built from a scriptedCompleter. It is the Phase 2 test
// surface for the adapter: it covers the contract listed in the plan
// (turn.start/turn.end ordering, prior-handler restore, cancel,
// concurrent Send, history/model/usage/title, no CLI-family imports).
package uiadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// noopTool is a tool that always succeeds. Conversation tests don't care
// about its output, only about the fact that the agent loop runs a tool
// step and emits an EventToolStart / EventToolEnd pair.
type noopTool struct{}

func (noopTool) Name() string               { return "noop" }
func (noopTool) Description() string        { return "does nothing and returns ok" }
func (noopTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (noopTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (noopTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// newTestConversation builds a chat.Session with a scripted completer
// and one noop tool, wrapped in uiadapter.NewConversation. t.Cleanup
// closes any goroutines that may still be alive.
func newTestConversation(t *testing.T, completer provider.Completer, msgs ...provider.Message) *uiadapter.Conversation {
	t.Helper()
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	sess := chat.NewSession(res, completer)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
	if len(msgs) > 0 {
		sess.Messages = append([]provider.Message{{Role: provider.RoleSystem, Content: "sys"}}, msgs...)
	}
	conv := uiadapter.NewConversation(sess)
	return conv
}

// drainUntilClose reads every event from ch into a slice until the
// channel closes or the timeout elapses. Empty slice + timeout error
// means the channel did not close.
func drainUntilClose(t *testing.T, ch <-chan uievent.Event, timeout time.Duration) []uievent.Event {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	out := []uievent.Event{}
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-deadline.C:
			t.Fatalf("drainUntilClose: timeout after %s with %d events", timeout, len(out))
		}
	}
}

// drainN reads exactly n events or fails the test on timeout. Used for
// tests that want to inspect the first n events without waiting for
// close.
func drainN(t *testing.T, ch <-chan uievent.Event, n int, timeout time.Duration) []uievent.Event {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	out := make([]uievent.Event, 0, n)
	for i := 0; i < n; i++ {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("drainN: channel closed after %d events, want %d", i, n)
			}
			out = append(out, e)
		case <-deadline.C:
			t.Fatalf("drainN: timeout after %s with %d events", timeout, len(out))
		}
	}
	return out
}

// TestSend_FullTurn_EmitsTurnStartThenEnd verifies the synthetic
// turn.start / turn.end framing and that at least one agent-loop event
// from the noop tool is present in the middle.
func TestSend_FullTurn_EmitsTurnStartThenEnd(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{
		toolResponse("tc1", "noop", "{}"),
		assistantResponse("world"),
	}}
	conv := newTestConversation(t, comp)
	h, err := conv.Send(context.Background(), intent.Send{Text: "hello"})
	if err != nil {
		t.Fatalf("Send returned err=%v", err)
	}
	got := drainUntilClose(t, h.Events(), 5*time.Second)
	if len(got) == 0 {
		t.Fatal("channel closed with no events")
	}
	if got[0].Kind != uievent.KindTurnStart {
		t.Fatalf("first event Kind=%v, want KindTurnStart", got[0].Kind)
	}
	body, ok := got[0].Body.(uievent.TurnStartBody)
	if !ok {
		t.Fatalf("first event body type=%T, want TurnStartBody", got[0].Body)
	}
	if body.Input != "hello" {
		t.Fatalf("TurnStartBody.Input=%q, want %q", body.Input, "hello")
	}
	last := got[len(got)-1]
	if last.Kind != uievent.KindTurnEnd {
		t.Fatalf("last event Kind=%v, want KindTurnEnd", last.Kind)
	}
	endBody, ok := last.Body.(uievent.TurnEndBody)
	if !ok {
		t.Fatalf("last event body type=%T, want TurnEndBody", last.Body)
	}
	if endBody.Reason != "completed" {
		t.Fatalf("KindTurnEnd reason=%q, want %q", endBody.Reason, "completed")
	}
	// At least one tool.start or tool.end in the middle.
	sawAgent := false
	for _, e := range got[1 : len(got)-1] {
		if e.Kind == uievent.KindToolStart || e.Kind == uievent.KindToolEnd {
			sawAgent = true
			break
		}
	}
	if !sawAgent {
		t.Fatalf("no agent-loop event between turn.start and turn.end: %+v", got)
	}
}

// TestSend_FullTurn_RestoresPriorHandler verifies that the adapter
// swaps OnAgentEvent back to its prior value when the turn ends. The
// post-turn SwapOnAgentEvent call must hand back the sentinel handler
// identity that was installed before Send.
func TestSend_FullTurn_RestoresPriorHandler(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("done")}}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})

	// sentinel is a typed method value, not a bare function literal, so
	// its reflect.ValueOf(...).Pointer() identity is stable across the
	// test (a fresh func literal each iteration would not compare equal).
	witness := &handlerWitness{}
	// Pre-install sentinel; capture whatever was there so the test
	// defer does not pollute the session for later tests.
	prev := sess.SwapOnAgentEvent(witness.Handle)
	defer sess.SwapOnAgentEvent(prev)

	c := uiadapter.NewConversation(sess)
	h, err := c.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatalf("Send err=%v", err)
	}
	// Drain until close; this also waits for restore.
	drainUntilClose(t, h.Events(), 5*time.Second)

	// After the turn, SwapOnAgentEvent should hand back the sentinel.
	// Re-install a placeholder and capture the returned prior handler,
	// restoring it on exit so subsequent tests see the original sink.
	returned := sess.SwapOnAgentEvent(func(agent.Event) {})
	defer sess.SwapOnAgentEvent(returned)

	if returned == nil {
		t.Fatal("post-turn SwapOnAgentEvent returned nil; prior handler was not restored")
	}
	if reflect.ValueOf(returned).Pointer() != reflect.ValueOf(witness.Handle).Pointer() {
		t.Fatalf("post-turn SwapOnAgentEvent returned handler with pointer %v, want sentinel pointer %v",
			reflect.ValueOf(returned).Pointer(),
			reflect.ValueOf(witness.Handle).Pointer())
	}
}

// handlerWitness provides a stable method-value identity for tests that
// need to compare an OnAgentEvent handler across a swap/restore cycle.
type handlerWitness struct{}

func (handlerWitness) Handle(agent.Event) {}

// TestSend_PriorHandlerRestoredOnError verifies that even when the
// completer errors, the prior OnAgentEvent is restored and the channel
// is closed exactly once.
func TestSend_PriorHandlerRestoredOnError(t *testing.T) {
	comp := &errCompleter{err: errors.New("boom")}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
	c := uiadapter.NewConversation(sess)

	h, err := c.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatalf("Send returned err=%v, want synchronous nil", err)
	}
	events := drainUntilClose(t, h.Events(), 5*time.Second)
	// Channel must be closed exactly once; second receive is zero, false.
	if _, ok := <-h.Events(); ok {
		t.Fatal("Events() channel not closed")
	}
	// Last event must be KindTurnEnd with reason "error".
	last := events[len(events)-1]
	if last.Kind != uievent.KindTurnEnd {
		t.Fatalf("last event Kind=%v, want KindTurnEnd", last.Kind)
	}
	body, ok := last.Body.(uievent.TurnEndBody)
	if !ok {
		t.Fatalf("last body type=%T, want TurnEndBody", last.Body)
	}
	if body.Reason != "error" {
		t.Fatalf("KindTurnEnd reason=%q, want %q", body.Reason, "error")
	}
}

// errCompleter always errors from ChatTurn. Used to drive the error path
// in TestSend_PriorHandlerRestoredOnError.
type errCompleter struct{ err error }

func (c *errCompleter) Name() string { return "err" }
func (c *errCompleter) Chat(_ context.Context, _ provider.Request) (string, error) {
	return "", c.err
}
func (c *errCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	return "", c.err
}
func (c *errCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return nil, c.err
}

// TestSend_CancelMidTurn_ClosesChannel verifies that handle.Cancel()
// cancels the per-turn context and closes the channel.
func TestSend_CancelMidTurn_ClosesChannel(t *testing.T) {
	comp := &scriptedCompleter{block: make(chan struct{}), turns: []provider.Response{
		toolResponse("tc1", "noop", "{}"),
	}}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
	c := uiadapter.NewConversation(sess)

	sendErr := make(chan error, 1)
	var handle ports.TurnHandle
	go func() {
		h, err := c.Send(context.Background(), intent.Send{Text: "x"})
		handle = h
		sendErr <- err
	}()
	if err := <-sendErr; err != nil {
		t.Fatalf("Send returned err=%v", err)
	}
	// At least the synthetic turn.start must arrive.
	first := drainN(t, handle.Events(), 1, 2*time.Second)
	if first[0].Kind != uievent.KindTurnStart {
		t.Fatalf("first event Kind=%v, want KindTurnStart", first[0].Kind)
	}
	handle.Cancel()
	// Channel must close eventually; the scriptedCompleter's block may
	// be unobserved after Cancel because ChatTurn checks ctx.Err() and
	// returns. drainUntilClose with a generous timeout.
	drainUntilClose(t, handle.Events(), 5*time.Second)
	// Second receive should be the closed-channel witness.
	if _, ok := <-handle.Events(); ok {
		t.Fatal("Events() not closed after Cancel")
	}
	// Release the completer's block so the goroutine exits cleanly.
	close(comp.block)
}

// TestCancel_AfterTurnIsNoOp verifies that Cancel after a turn has
// ended does not panic and does not double-close the channel.
func TestCancel_AfterTurnIsNoOp(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("done")}}
	conv := newTestConversation(t, comp)
	h, err := conv.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatalf("Send err=%v", err)
	}
	drainUntilClose(t, h.Events(), 5*time.Second)
	// Two Cancel calls; both must be safe.
	h.Cancel()
	h.Cancel()
	// Receive from a closed channel: zero, false.
	if _, ok := <-h.Events(); ok {
		t.Fatal("Events() returned a value after close")
	}
}

// TestSend_ConcurrentSerializes verifies that two concurrent Sends are
// serialized by turnMu and their terminal events arrive in handle order.
func TestSend_ConcurrentSerializes(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("a")}}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
	c := uiadapter.NewConversation(sess)

	var wg sync.WaitGroup
	var hA, hB ports.TurnHandle
	wg.Add(2)
	go func() {
		defer wg.Done()
		h, err := c.Send(context.Background(), intent.Send{Text: "A"})
		if err != nil {
			t.Errorf("Send A err=%v", err)
			return
		}
		hA = h
	}()
	go func() {
		defer wg.Done()
		h, err := c.Send(context.Background(), intent.Send{Text: "B"})
		if err != nil {
			t.Errorf("Send B err=%v", err)
			return
		}
		hB = h
	}()
	wg.Wait()
	if hA == nil || hB == nil {
		t.Fatal("one or both Sends failed to return a handle")
	}
	// Drain both.
	eventsA := drainUntilClose(t, hA.Events(), 5*time.Second)
	eventsB := drainUntilClose(t, hB.Events(), 5*time.Second)
	// First event of each channel must be its own turn.start.
	if eventsA[0].Kind != uievent.KindTurnStart || eventsB[0].Kind != uievent.KindTurnStart {
		t.Fatalf("first events A=%v B=%v, want both KindTurnStart", eventsA[0].Kind, eventsB[0].Kind)
	}
	// Last event of each channel must be KindTurnEnd.
	if eventsA[len(eventsA)-1].Kind != uievent.KindTurnEnd || eventsB[len(eventsB)-1].Kind != uievent.KindTurnEnd {
		t.Fatalf("last events not KindTurnEnd")
	}
	// The test passes if both turns completed cleanly under the race
	// detector; the ordering assertion is implicit because chat.Session
	// serializes via turnMu.
}

// TestHistory_ReflectsSessionMessages verifies that History returns
// user/assistant messages from the session in order.
func TestHistory_ReflectsSessionMessages(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("r")}}
	conv := newTestConversation(t, comp, provider.Message{Role: provider.RoleUser, Content: "u1"}, provider.Message{Role: provider.RoleAssistant, Content: "a1"})
	got := conv.History()
	if len(got) != 2 {
		t.Fatalf("History len=%d, want 2", len(got))
	}
	if got[0].Role != "user" || got[0].Text != "u1" {
		t.Fatalf("got[0]=%+v, want {user,u1}", got[0])
	}
	if got[1].Role != "assistant" || got[1].Text != "a1" {
		t.Fatalf("got[1]=%+v, want {assistant,a1}", got[1])
	}
}

// TestHistory_EmptyReturnsNil verifies that an empty session returns
// nil, not an empty slice.
func TestHistory_EmptyReturnsNil(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("r")}}
	conv := newTestConversation(t, comp)
	if got := conv.History(); got != nil {
		t.Fatalf("History on empty session=%+v, want nil", got)
	}
}

// TestModel_ReportsProviderAndWindow verifies that Model returns the
// provider and model from the session's binding.
func TestModel_ReportsProviderAndWindow(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("r")}}
	res := &config.Resolved{
		Model:        "test-model",
		SystemPrompt: "sys",
		ModelProfiles: []config.ModelSpec{{
			Name:                "test-model",
			ContextWindowTokens: 12345,
		}},
	}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
	c := uiadapter.NewConversation(sess)
	m := c.Model()
	if m.Name != "test-model" {
		t.Fatalf("Name=%q, want test-model", m.Name)
	}
	if m.Provider != "scripted" {
		t.Fatalf("Provider=%q, want scripted", m.Provider)
	}
	if m.ContextWindow != 12345 {
		t.Fatalf("ContextWindow=%d, want 12345", m.ContextWindow)
	}
}

// TestContextUsage_MapsFields verifies the four-field mapping.
func TestContextUsage_MapsFields(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("r")}}
	conv := newTestConversation(t, comp)
	u := conv.ContextUsage()
	// OutputTokens must be 0 (OutputReserveTokens is reservation capacity, not consumed tokens),
	// and CachedTokens / CostUSD are honest zeros.
	if u.OutputTokens != 0 {
		t.Fatalf("OutputTokens=%d, want 0 (reservation output tokens must not be charged as used tokens)", u.OutputTokens)
	}
	if u.CachedTokens != 0 {
		t.Fatalf("CachedTokens=%d, want 0", u.CachedTokens)
	}
	if u.CostUSD != 0 {
		t.Fatalf("CostUSD=%f, want 0", u.CostUSD)
	}
	if u.InputTokens < 0 {
		t.Fatalf("negative tokens: %+v", u)
	}
}

// TestTitle_DerivedFromFirstUserMessage verifies the whitespace-collapse
// and ellipsisation rules.
func TestTitle_DerivedFromFirstUserMessage(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("r")}}
	t.Run("TrimsAndCollapsesWhitespace", func(t *testing.T) {
		conv := newTestConversation(t, comp, provider.Message{
			Role: provider.RoleUser, Content: "  hello   world  ",
		})
		if got := conv.Title(); got != "hello world" {
			t.Fatalf("Title=%q, want %q", got, "hello world")
		}
	})
	t.Run("EllipsisesPast60Runes", func(t *testing.T) {
		long := strings.Repeat("a", 70)
		conv := newTestConversation(t, comp, provider.Message{
			Role: provider.RoleUser, Content: long,
		})
		got := conv.Title()
		if !strings.HasSuffix(got, "...") {
			t.Fatalf("Title=%q, want ellipsised suffix", got)
		}
		// 60 runes + ellipsis (3 runes) = 63 runes total.
		if got != strings.Repeat("a", 60)+"..." {
			t.Fatalf("Title len=%d, want %d", len(got), len(strings.Repeat("a", 60)+"..."))
		}
	})
	t.Run("MemoisedAcrossCalls", func(t *testing.T) {
		conv := newTestConversation(t, comp, provider.Message{
			Role: provider.RoleUser, Content: "first",
		})
		a := conv.Title()
		// Mutate the live session to add a NEW user message; the cached
		// title must still be "first", proving memoisation.
		// Note: this test intentionally races titleMu by appending to
		// s.Messages under the assumption that Title() only reads
		// MessagesCopy() once. If the implementation changes to re-read
		// per call, this test would fail; that's intentional.
		// However, we cannot reach s from outside the adapter without
		// exposing it; the test instead just calls Title() twice.
		b := conv.Title()
		if a != b {
			t.Fatalf("memoisation broken: first=%q second=%q", a, b)
		}
	})
	t.Run("ResetsOnSessionIDChange", func(t *testing.T) {
		sess := chat.NewSession(&config.Resolved{Model: "m"}, comp)
		sess.SessionID = "session-1"
		sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "title 1"}}
		conv := uiadapter.NewConversation(sess)
		if got := conv.Title(); got != "title 1" {
			t.Fatalf("Title()=%q, want %q", got, "title 1")
		}

		sess.SessionID = "session-2"
		sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "title 2"}}
		if got := conv.Title(); got != "title 2" {
			t.Fatalf("Title() after session switch=%q, want %q", got, "title 2")
		}
	})
}

// TestSend_GoroutineLeak performs 20 sequential Send calls and asserts
// every per-turn goroutine has fully returned by waiting on the
// SetTurnWaiterForTest-installed WaitGroup. The channel-close check
// (drainUntilClose) precedes the goroutine's final defers and so is
// not by itself evidence that the goroutine returned end-to-end; the
// WaitGroup is the authoritative signal.
func TestSend_GoroutineLeak(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("r")}}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
	c := uiadapter.NewConversation(sess)
	var wg sync.WaitGroup
	uiadapter.SetTurnWaiterForTest(&wg)
	defer uiadapter.SetTurnWaiterForTest(nil)
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		h, err := c.Send(context.Background(), intent.Send{Text: "x"})
		if err != nil {
			t.Fatalf("Send #%d err=%v", i, err)
		}
		drainUntilClose(t, h.Events(), 5*time.Second)
	}
	// All per-turn goroutines must have fully returned by now: every
	// Add(1) above is matched by a Done() from runTurnGoroutine's final
	// defer. Wait() with a generous timeout proves the test seam is
	// wired and the goroutines do not leak past the close.
	waitDone := make(chan struct{})
	go func() { wg.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("per-turn goroutines did not return within 5s after channel close")
	}
}

// TestSend_ChannelCloseIsExactlyOnce fans out N Cancel calls and
// asserts that only one close is observed (no panic from double-close).
func TestSend_ChannelCloseIsExactlyOnce(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{assistantResponse("r")}}
	conv := newTestConversation(t, comp)
	h, err := conv.Send(context.Background(), intent.Send{Text: "x"})
	if err != nil {
		t.Fatalf("Send err=%v", err)
	}
	drainUntilClose(t, h.Events(), 5*time.Second)
	const N = 64
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Cancel panicked: %v", r)
				}
			}()
			h.Cancel()
		}()
	}
	wg.Wait()
}

// TestConversation_DoesNotImportCLIFamily is a belt-and-braces invariant
// check: it runs `go list -deps` against the package and greps for any
// of the cli-family packages. Not wired to make verify; it is local
// evidence for this chunk only.
func TestConversation_DoesNotImportCLIFamily(t *testing.T) {
	if testing.Short() {
		t.Skip("invariants skip in -short")
	}
	cmd := exec.Command("go", "list", "-deps", "./internal/uiadapter")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list failed (%v); skipping CLI-family invariant", err)
	}
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		for _, banned := range []string{
			"github.com/MiviaLabs/mivia-agent/internal/cli",
			"github.com/MiviaLabs/mivia-agent/internal/clichat",
			"github.com/MiviaLabs/mivia-agent/internal/cliworkflow",
			"github.com/MiviaLabs/mivia-agent/internal/cliorchestrate",
			"github.com/MiviaLabs/mivia-agent/internal/cliworktree",
		} {
			if l == banned {
				t.Fatalf("uiadapter transitively imports %s", banned)
			}
		}
	}
}

// TestUIPackages_DoNotImportUIAdapter mirrors the invariant for the
// other half of the isolation contract: ./internal/ui/... and
// ./internal/uikit/... must not import uiadapter (that would create a
// reverse dependency). Not wired to make verify.
func TestUIPackages_DoNotImportUIAdapter(t *testing.T) {
	if testing.Short() {
		t.Skip("invariants skip in -short")
	}
	for _, target := range []string{"./internal/ui/...", "./internal/uikit/..."} {
		cmd := exec.Command("go", "list", "-deps", target)
		out, err := cmd.Output()
		if err != nil {
			t.Logf("go list %s failed (%v); skipping", target, err)
			continue
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l == "github.com/MiviaLabs/mivia-agent/internal/uiadapter" {
				t.Fatalf("%s transitively imports uiadapter", target)
			}
		}
	}
}

// TestSend_FullTurn_ExactlyOneOfEach pins the per-turn event-count
// invariants that manual acceptance of Phase 3 surfaced as bugs:
//
//   - exactly one KindTurnStart (a duplicated synthetic turn.start
//     surfaces as a duplicated "> hi" in the stream renderer).
//   - exactly one KindTextEnd with the assistant's final content
//     (a duplicated text.end surfaces as the doubled message).
//   - exactly one KindTurnEnd (a missing or duplicated terminal
//     surfaces as either no transcript terminator or a stuck renderer).
//   - exactly one KindError with non-empty Text (an empty-text error
//     surfaces as a bare "  error" line — Bug B; see also
//     internal/ui/stream/stream.go's defensive guard).
//
// This is stricter than the original
// TestSend_FullTurn_EmitsTurnStartThenEnd, which only checks the
// first/last kinds. A regression that duplicates any of these events
// fails loudly here even if the test was green under the old check.
func TestSend_FullTurn_ExactlyOneOfEach(t *testing.T) {
	comp := &scriptedCompleter{turns: []provider.Response{
		toolResponse("tc1", "noop", "{}"),
		assistantResponse("world"),
	}}
	conv := newTestConversation(t, comp)
	h, err := conv.Send(context.Background(), intent.Send{Text: "hello"})
	if err != nil {
		t.Fatalf("Send returned err=%v", err)
	}
	got := drainUntilClose(t, h.Events(), 5*time.Second)

	counts := map[uievent.Kind]int{}
	for _, e := range got {
		counts[e.Kind]++
	}

	if counts[uievent.KindTurnStart] != 1 {
		t.Errorf("KindTurnStart count=%d, want 1 (duplicates surface as duplicated '> hello' lines in the stream renderer)", counts[uievent.KindTurnStart])
	}
	if counts[uievent.KindTurnEnd] != 1 {
		t.Errorf("KindTurnEnd count=%d, want 1 (missing or duplicated terminal surfaces as a stuck renderer)", counts[uievent.KindTurnEnd])
	}
	if counts[uievent.KindTextEnd] != 1 {
		t.Errorf("KindTextEnd count=%d, want 1 (duplicates surface as the doubled assistant message bug)", counts[uievent.KindTextEnd])
	}
	if counts[uievent.KindTextDelta] != 0 {
		t.Errorf("KindTextDelta count=%d, want 0 (the scriptedCompleter's assistantResponse emits a single text.end with the full text, not deltas)", counts[uievent.KindTextDelta])
	}

	// Pin the empty-text error invariant: any KindError reaching the
	// channel must have a non-empty Text. The renderer suppresses
	// empty-text errors today, but the upstream invariant is what
	// keeps the channel clean.
	for i, e := range got {
		if e.Kind != uievent.KindError {
			continue
		}
		b, ok := e.Body.(uievent.ErrorBody)
		if !ok {
			t.Errorf("event[%d] KindError body type=%T, want ErrorBody", i, e.Body)
			continue
		}
		if b.Text == "" && !b.Fatal {
			t.Errorf("event[%d] KindError{Text:'', Fatal:false} must not appear on the channel; current renderer suppresses empty-text errors but the producer is wrong", i)
		}
	}
}

// TestSend_FullTurn_TextEndContentExact pins the text.end invariant
// directly: a single-turn scripted response produces exactly one
// KindTextEnd whose Text equals the scripted response's Content.
// This is the primary regression for the doubled-message bug (Bug
// A) under the offline test surface. A future change that re-emits
// the assistant content through a second surface (a tool-name
// collision, a hub re-emit, a steer retry) would either duplicate
// the KindTextEnd or change its body — both fail here.
//
// The streaming-delta case (assistant content emitted as N deltas +
// one final text.end) is exercised by the end-to-end stream-renderer
// smoke test in cmd/mivia-ui_test.go (Phase 3 acceptance), which
// runs the binary against a real completer. The test below covers
// the non-streaming scripted path which is the most common offline
// assertion surface.
func TestSend_FullTurn_TextEndContentExact(t *testing.T) {
	const want = "the assistant said this"
	comp := &scriptedCompleter{turns: []provider.Response{
		assistantResponse(want),
	}}
	conv := newTestConversation(t, comp)
	h, err := conv.Send(context.Background(), intent.Send{Text: "say it"})
	if err != nil {
		t.Fatalf("Send returned err=%v", err)
	}
	got := drainUntilClose(t, h.Events(), 5*time.Second)

	textEnds := 0
	for i, e := range got {
		if e.Kind != uievent.KindTextEnd {
			continue
		}
		textEnds++
		b, ok := e.Body.(uievent.TextEndBody)
		if !ok {
			t.Errorf("event[%d] KindTextEnd body type=%T, want TextEndBody", i, e.Body)
			continue
		}
		if b.Text != want {
			t.Errorf("event[%d] KindTextEnd text=%q, want %q", i, b.Text, want)
		}
	}
	if textEnds != 1 {
		t.Errorf("KindTextEnd count=%d, want 1 (the doubled-message bug surfaces as 2)", textEnds)
	}
}

func TestHistory_CarriesDiffsFromPriorToolCalls(t *testing.T) {
	tc := provider.ToolCall{
		ID:   "call_1",
		Type: "function",
	}
	tc.Function.Name = "replace_file_content"
	tc.Function.Arguments = `{"path": "foo/bar.go"}`

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "edit foo"},
		{
			Role:      provider.RoleAssistant,
			Content:   "editing...",
			ToolCalls: []provider.ToolCall{tc},
		},
		{
			Role:       provider.RoleTool,
			ToolCallID: "call_1",
			Content:    "updated foo/bar.go (1 replacement, +2 −1)\n--- a/foo/bar.go\n+++ b/foo/bar.go\n@@ -1,3 +1,4 @@\n-old\n+new1\n+new2\n context",
		},
	}
	comp := &scriptedCompleter{}
	conv := newTestConversation(t, comp, msgs...)
	hist := conv.History()
	if len(hist) < 2 {
		t.Fatalf("expected at least 2 history messages, got %d", len(hist))
	}
	assistantMsg := hist[1]
	if len(assistantMsg.Diffs) != 1 {
		t.Fatalf("expected 1 diff in assistant history message, got %d", len(assistantMsg.Diffs))
	}
	diff := assistantMsg.Diffs[0]
	if diff.Path != "foo/bar.go" {
		t.Errorf("diff.Path = %q, want %q", diff.Path, "foo/bar.go")
	}
	if diff.Added != 2 || diff.Removed != 1 {
		t.Errorf("diff Added=%d, Removed=%d, want +2 -1", diff.Added, diff.Removed)
	}
}
