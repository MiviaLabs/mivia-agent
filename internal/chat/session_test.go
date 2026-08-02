package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestPreparedTurnDiscardedAfterClear(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, nil)
	sess.Messages = []provider.Message{{Role: provider.RoleSystem, Content: "sys"}, {Role: provider.RoleUser, Content: "before"}}
	token := sess.captureOperationToken("turn:stale")
	_ = sess.Clear()
	prepared := []provider.Message{{Role: provider.RoleSystem, Content: "sys"}, {Role: provider.RoleUser, Content: "secret"}, {Role: provider.RoleAssistant, Content: "answer"}}
	if err := sess.commitPreparedTurn(prepared, token, nil); !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("stale prepared turn error = %v, want ErrStaleOperation", err)
	}
	if strings.Contains(historyBlob(sess), "secret") || strings.Contains(historyBlob(sess), "before") {
		t.Fatalf("clear retained stale prepared history: %s", historyBlob(sess))
	}
}

type fakeCompleter struct {
	err error
	out string
}

func (f *fakeCompleter) Name() string { return "fake" }

func (f *fakeCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return f.ChatStream(ctx, req, io.Discard)
}

func (f *fakeCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if w != nil {
		_, _ = io.WriteString(w, f.out)
	}
	return f.out, nil
}

func (f *fakeCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &provider.Response{Content: f.out, FinishReason: "stop"}, nil
}

// sessionToolCompleter drives one tool call then a final reply (agent mode).
type sessionToolCompleter struct {
	calls    int
	tool     string
	args     string
	requests []provider.Request
}

func (c *sessionToolCompleter) Name() string { return "session-tool" }
func (c *sessionToolCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *sessionToolCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *sessionToolCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	c.requests = append(c.requests, req)
	if c.calls == 1 {
		var call provider.ToolCall
		call.ID = "tc1"
		call.Type = "function"
		call.Function.Name = c.tool
		call.Function.Arguments = c.args
		return &provider.Response{
			ToolCalls:    []provider.ToolCall{call},
			FinishReason: "tool_calls",
		}, nil
	}
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func TestSessionScrubsEphemeralToolResultsAfterTheActiveTurn(t *testing.T) {
	const secret = "resource body must not persist"
	reg := tools.NewRegistry()
	reg.Register(tools.NewSkillResourceTool(func(context.Context, string) (string, string, error) {
		return secret, "skill resource loaded: template", nil
	}, "test-activation", 4096))
	comp := &sessionToolCompleter{tool: tools.SkillResourceToolName, args: `{"id":"template"}`}
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, comp)
	s.UseTools = true
	s.Tools = reg
	s.MaxSteps = 3
	var events []agent.Event
	s.OnAgentEvent = func(event agent.Event) { events = append(events, event) }
	if _, err := s.SendUser(context.Background(), "read the template", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(comp.requests) < 2 || !strings.Contains(messagesText(comp.requests[1].Messages), secret) {
		t.Fatalf("resource body did not reach the active turn's next provider request: %#v", comp.requests)
	}
	for _, message := range s.MessagesCopy() {
		if strings.Contains(message.Content, secret) {
			t.Fatalf("persistable history leaked resource body: %#v", s.MessagesCopy())
		}
	}
	if !strings.Contains(messagesText(s.MessagesCopy()), "skill resource loaded: template") {
		t.Fatalf("history did not retain safe resource marker: %#v", s.MessagesCopy())
	}
	for _, event := range events {
		if strings.Contains(event.Output, secret) {
			t.Fatalf("event leaked resource body: %#v", event)
		}
	}
}

func messagesText(messages []provider.Message) string {
	var values []string
	for _, message := range messages {
		values = append(values, message.Content)
	}
	return strings.Join(values, "\n")
}

// timedCapTool sleeps under ctx; Capability.Timeout advertises budget.
type timedCapTool struct {
	name    string
	timeout time.Duration
	work    time.Duration
	ran     atomic.Int32
}

func (t *timedCapTool) Name() string               { return t.name }
func (t *timedCapTool) Description() string        { return "timed cap tool" }
func (t *timedCapTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *timedCapTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, Timeout: t.timeout, ResourceKey: "path:" + t.name}
}
func (t *timedCapTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.ran.Add(1)
	select {
	case <-time.After(t.work):
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestSessionAgentCapabilityExtendsShortToolTimeout(t *testing.T) {
	// Session default tool timeout is deliberately short (40ms). Long tool
	// advertises 250ms Capability.Timeout and does 80ms of work - must complete.
	long := &timedCapTool{name: "long_tool", timeout: 250 * time.Millisecond, work: 80 * time.Millisecond}
	reg := tools.NewRegistry()
	reg.Register(long)

	comp := &sessionToolCompleter{tool: "long_tool", args: `{}`}
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, comp)
	s.UseTools = true
	s.Tools = reg
	s.ToolTimeout = 40 * time.Millisecond
	s.MaxSteps = 3

	start := time.Now()
	reply, err := s.SendUser(context.Background(), "run long", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("session hang: %s", elapsed)
	}
	if reply != "done" {
		t.Fatalf("reply=%q", reply)
	}
	if long.ran.Load() != 1 {
		t.Fatalf("long tool ran %d times", long.ran.Load())
	}
	// Tool result should be success in history, not deadline.
	foundOK := false
	for _, m := range s.MessagesCopy() {
		if m.Role == provider.RoleTool && strings.Contains(m.Content, "ok") {
			foundOK = true
		}
		if m.Role == provider.RoleTool && strings.Contains(m.Content, "deadline") {
			t.Fatalf("long tool should not be killed by session ToolTimeout: %q", m.Content)
		}
	}
	if !foundOK {
		t.Fatalf("expected tool ok in history, msgs=%+v", s.MessagesCopy())
	}
}

func TestSessionAgentDefaultToolTimeoutStillBoundsPlainTools(t *testing.T) {
	// No Capability.Timeout → session ToolTimeout (40ms) kills 200ms work.
	plain := &timedCapTool{name: "plain_tool", timeout: 0, work: 200 * time.Millisecond}
	reg := tools.NewRegistry()
	reg.Register(plain)
	comp := &sessionToolCompleter{tool: "plain_tool", args: `{}`}
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, comp)
	s.UseTools = true
	s.Tools = reg
	s.ToolTimeout = 40 * time.Millisecond
	s.MaxSteps = 3

	start := time.Now()
	_, err := s.SendUser(context.Background(), "run plain", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("expected bounded wait, got %s", elapsed)
	}
	// The tool message must carry the timeout. "timed_out" is the bounded status
	// the dispatcher synthesizes; the other two cover the paths that surface the
	// raw context error instead. This previously matched "error:" only by
	// accident, via the "ref:error:" prefix of a content reference that the
	// failure payload no longer carries (INV-AG-10: nothing stores those bytes,
	// so no reference is minted at that layer).
	sawDeadline := false
	for _, m := range s.MessagesCopy() {
		if m.Role != provider.RoleTool {
			continue
		}
		if strings.Contains(m.Content, "timed_out") ||
			strings.Contains(m.Content, "deadline") ||
			strings.Contains(m.Content, "error:") {
			sawDeadline = true
		}
	}
	if !sawDeadline {
		t.Fatalf("plain tool should hit session ToolTimeout, msgs=%+v", s.MessagesCopy())
	}
}

// loopingToolCompleter never stops asking for a tool call - the shape of a
// model stuck in a tool loop.
type loopingToolCompleter struct {
	calls atomic.Int32
}

func (c *loopingToolCompleter) Name() string { return "looping-tool" }
func (c *loopingToolCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *loopingToolCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *loopingToolCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := c.calls.Add(1)
	var call provider.ToolCall
	call.ID = "tc" + strconv.Itoa(int(n))
	call.Type = "function"
	call.Function.Name = "plain_tool"
	call.Function.Arguments = `{}`
	return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
}

// TestNewSessionBoundsAgentSteps pins the default step ceiling: the default
// is 0 (unlimited), so /steps is the only way to cap a turn.
func TestNewSessionBoundsAgentSteps(t *testing.T) {
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, &fakeCompleter{out: "ok"})
	if s.MaxSteps != DefaultMaxSteps {
		t.Fatalf("MaxSteps=%d, want DefaultMaxSteps=%d", s.MaxSteps, DefaultMaxSteps)
	}
}

func TestSessionAgentLoopStopsAtConfiguredMaxSteps(t *testing.T) {
	plain := &timedCapTool{name: "plain_tool"}
	reg := tools.NewRegistry()
	reg.Register(plain)
	comp := &loopingToolCompleter{}
	maxSteps := 3
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys", MaxSteps: &maxSteps}, comp)
	s.UseTools = true
	s.Tools = reg

	done := make(chan error, 1)
	go func() {
		_, err := s.SendUser(context.Background(), "loop forever", io.Discard)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "max_steps") {
			t.Fatalf("expected a max_steps error, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("agent loop never terminated: configured max_steps not enforced")
	}
	if got := comp.calls.Load(); int(got) > maxSteps+1 {
		t.Fatalf("model called %d times, want at most maxSteps+1=%d", got, maxSteps+1)
	}
}

func TestClearAndUserTurns(t *testing.T) {
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{out: "ok"})
	if len(s.Messages) != 1 {
		t.Fatalf("want system message")
	}
	_, err := s.SendUser(context.Background(), "hi", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.UserTurns() != 1 {
		t.Fatalf("turns=%d", s.UserTurns())
	}
	_ = s.Clear()
	if s.UserTurns() != 0 || len(s.Messages) != 1 {
		t.Fatalf("clear failed: turns=%d msgs=%d", s.UserTurns(), len(s.Messages))
	}
}

func TestFailedSendDropsUserTurn(t *testing.T) {
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{err: context.Canceled})
	_, err := s.SendUser(context.Background(), "hi", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if s.UserTurns() != 0 {
		t.Fatalf("user turn should be dropped, turns=%d", s.UserTurns())
	}
}

// gatedCompleter blocks the first ChatStream until release is closed (or ctx done).
// Subsequent calls return immediately with outFast.
type gatedCompleter struct {
	firstEntered chan struct{}
	releaseFirst chan struct{}
	outSlow      string
	outFast      string
	calls        int
	mu           sync.Mutex
}

func (g *gatedCompleter) Name() string { return "gated" }

func (g *gatedCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return g.ChatStream(ctx, req, io.Discard)
}

func (g *gatedCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()
	if n == 1 {
		close(g.firstEntered)
		select {
		case <-g.releaseFirst:
			if w != nil {
				_, _ = io.WriteString(w, g.outSlow)
			}
			return g.outSlow, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if w != nil {
		_, _ = io.WriteString(w, g.outFast)
	}
	return g.outFast, nil
}

func (g *gatedCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	out, err := g.ChatStream(ctx, req, io.Discard)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: out, FinishReason: "stop"}, nil
}

// TestSendUserStaleTurnDoesNotOverwrite ensures a slow first SendUser cannot
// overwrite Messages after a newer turn has completed (force-send race).
func TestSendUserStaleTurnDoesNotOverwrite(t *testing.T) {
	g := &gatedCompleter{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		outSlow:      "REPLY_FIRST",
		outFast:      "REPLY_SECOND",
	}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, g)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	done1 := make(chan error, 1)
	go func() {
		_, err := s.SendUser(ctx1, "first", io.Discard)
		done1 <- err
	}()

	select {
	case <-g.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first SendUser did not enter ChatStream")
	}

	// Newer turn while first is still in-flight (simulates force-send overlap).
	cancel1()
	_, err := s.SendUser(context.Background(), "second", io.Discard)
	if err != nil {
		t.Fatalf("second SendUser: %v", err)
	}

	// Unblock first if it is still waiting (cancelled path may already have returned).
	close(g.releaseFirst)
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("first SendUser did not finish")
	}

	// Latest turn wins: history must reflect "second", not a late write of "first".
	s.mu.Lock()
	defer s.mu.Unlock()
	var users []string
	var lastAssistant string
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			users = append(users, m.Content)
		}
		if m.Role == provider.RoleAssistant {
			lastAssistant = m.Content
		}
	}
	if len(users) != 1 || users[0] != "second" {
		t.Fatalf("user messages = %v, want [second]", users)
	}
	if lastAssistant != "REPLY_SECOND" {
		t.Fatalf("assistant = %q, want REPLY_SECOND", lastAssistant)
	}
}

// TestSendUserSlowFirstCannotOverwriteFasterSecond covers the non-cancel race:
// first turn completes after second and must not clobber Messages.
func TestSendUserSlowFirstCannotOverwriteFasterSecond(t *testing.T) {
	g := &gatedCompleter{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		outSlow:      "REPLY_FIRST",
		outFast:      "REPLY_SECOND",
	}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, g)

	done1 := make(chan error, 1)
	go func() {
		_, err := s.SendUser(context.Background(), "first", io.Discard)
		done1 <- err
	}()

	select {
	case <-g.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first SendUser did not enter ChatStream")
	}

	_, err := s.SendUser(context.Background(), "second", io.Discard)
	if err != nil {
		t.Fatalf("second SendUser: %v", err)
	}

	close(g.releaseFirst)
	if err := <-done1; err != nil {
		t.Fatalf("first SendUser: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var users []string
	var lastAssistant string
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			users = append(users, m.Content)
		}
		if m.Role == provider.RoleAssistant {
			lastAssistant = m.Content
		}
	}
	if len(users) != 1 || users[0] != "second" {
		t.Fatalf("user messages = %v, want [second] (stale first must not write)", users)
	}
	if lastAssistant != "REPLY_SECOND" {
		t.Fatalf("assistant = %q, want REPLY_SECOND", lastAssistant)
	}
}

func TestSessionAgentPublishesToEventBus(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&timedCapTool{name: "echo", timeout: time.Second, work: 0})
	comp := &sessionToolCompleter{tool: "echo", args: `{}`}
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, comp)
	s.UseTools = true
	s.Tools = reg
	s.MaxSteps = 3

	bus := events.New()
	s.EventBus = bus
	var mu sync.Mutex
	var kinds []events.Kind
	bus.SubscribeMany([]events.Kind{
		events.KindAssistant, events.KindToolStart, events.KindToolEnd,
	}, events.HandlerFunc(func(ctx context.Context, ev events.Event) {
		mu.Lock()
		kinds = append(kinds, ev.Kind)
		mu.Unlock()
	}))

	reply, err := s.SendUser(context.Background(), "go", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Fatalf("reply=%q", reply)
	}
	bus.Flush()
	mu.Lock()
	defer mu.Unlock()
	hasAsst, hasStart, hasEnd := false, false, false
	for _, k := range kinds {
		switch k {
		case events.KindAssistant:
			hasAsst = true
		case events.KindToolStart:
			hasStart = true
		case events.KindToolEnd:
			hasEnd = true
		}
	}
	if !hasAsst || !hasStart || !hasEnd {
		t.Fatalf("expected assistant+tool_start+tool_end on bus, got %v", kinds)
	}
}

// [chat] max_steps must be honoured, including an explicit 0 (unlimited) -
// which is why the config field is a pointer. Treating 0 as "unset" would
// silently re-impose the default on a user who asked for no ceiling.
func TestSessionMaxStepsFromConfig(t *testing.T) {
	zero, custom := 0, 7
	cases := map[string]struct {
		configured *int
		want       int
	}{
		"unset uses default":         {nil, DefaultMaxSteps},
		"explicit zero is unlimited": {&zero, 0},
		"explicit value honoured":    {&custom, 7},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := NewSession(&config.Resolved{Model: "m", MaxSteps: tc.configured}, &fakeCompleter{})
			if s.MaxSteps != tc.want {
				t.Fatalf("MaxSteps=%d want %d", s.MaxSteps, tc.want)
			}
		})
	}
}

func TestSetAgentSettingsUpdatesProviderSystemMessage(t *testing.T) {
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "BASE", MaxSteps: intpSession(7)}, &fakeCompleter{})
	s.SetAgentSettings("AGENT", 3)
	messages := s.MessagesCopy()
	if len(messages) == 0 || messages[0].Role != provider.RoleSystem || messages[0].Content != "AGENT" {
		t.Fatalf("agent system message = %+v", messages)
	}
	s.SetAgentSettings("BASE", 7)
	messages = s.MessagesCopy()
	if messages[0].Content != "BASE" || s.MaxStepsValue() != 7 {
		t.Fatalf("restored settings = %+v / %d", messages, s.MaxStepsValue())
	}
}

func intpSession(v int) *int { return &v }
