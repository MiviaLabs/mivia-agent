package uiadapter_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

type approverTestTool struct {
	name  string
	class tools.ExecutionClass
}

func (t *approverTestTool) Name() string               { return t.name }
func (t *approverTestTool) Description() string        { return "test tool" }
func (t *approverTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *approverTestTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: t.class, ResourceKey: "path:" + t.name}
}
func (t *approverTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

type decisionCase struct {
	name                 string
	decision             ports.Decision
	wantApproved         bool
	wantApprovedForClass bool
	wantDenialErr        bool
}

func testOneDecision(t *testing.T, tc decisionCase) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	appr := uiadapter.NewApprover(sess)

	if sess.ApprovalGate == nil {
		t.Fatal("NewApprover must set ApprovalGate on session")
	}
	if sess.ApprovalStanding == nil {
		t.Fatal("NewApprover must initialize ApprovalStanding if nil")
	}

	var (
		res  sdkadapter.ApprovalResult
		wg   sync.WaitGroup
		done = make(chan struct{})
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		res = sess.ApprovalGate(context.Background(), "write_file", json.RawMessage(`{"path":"/tmp/a"}`))
		close(done)
	}()

	select {
	case req := <-appr.Pending():
		if req.ToolName != "write_file" {
			t.Fatalf("req.ToolName = %q, want write_file", req.ToolName)
		}
		if req.ID == "" {
			t.Fatal("req.ID must not be empty")
		}
		if req.Args["path"] != "/tmp/a" {
			t.Fatalf("req.Args[path] = %v, want /tmp/a", req.Args["path"])
		}
		appr.Resolve(req.ID, tc.decision)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Pending request")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ApprovalGate to unblock")
	}

	wg.Wait()

	if res.Approved != tc.wantApproved {
		t.Errorf("res.Approved = %v, want %v", res.Approved, tc.wantApproved)
	}
	if res.ApprovedForClass != tc.wantApprovedForClass {
		t.Errorf("res.ApprovedForClass = %v, want %v", res.ApprovedForClass, tc.wantApprovedForClass)
	}
	if tc.wantDenialErr && res.Err == "" {
		t.Error("expected non-empty Err on denial")
	}
}

func TestApprover_DecisionMappings(t *testing.T) {
	cases := []decisionCase{
		{
			name:                 "DecisionOnce",
			decision:             ports.DecisionOnce,
			wantApproved:         true,
			wantApprovedForClass: false,
		},
		{
			name:                 "DecisionAlways",
			decision:             ports.DecisionAlways,
			wantApproved:         true,
			wantApprovedForClass: true,
		},
		{
			name:                 "DecisionDeny",
			decision:             ports.DecisionDeny,
			wantApproved:         false,
			wantApprovedForClass: false,
			wantDenialErr:        true,
		},
		{
			name:                 "DecisionDenyAlways",
			decision:             ports.DecisionDenyAlways,
			wantApproved:         false,
			wantApprovedForClass: true,
			wantDenialErr:        true,
		},
		{
			name:                 "DecisionUnknown",
			decision:             ports.Decision(999),
			wantApproved:         false,
			wantApprovedForClass: false,
			wantDenialErr:        true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			testOneDecision(t, tc)
		})
	}
}

func TestApprover_ResolveUnknownIDIsNoOp(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	appr := uiadapter.NewApprover(sess)

	// Resolving an unknown ID must not panic or block.
	appr.Resolve("unknown-id-123", ports.DecisionOnce)
	appr.Resolve("", ports.DecisionDeny)
}

func TestApprover_ContextCancellationBeforePending(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	_ = uiadapter.NewApprover(sess)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already canceled before gate called

	res := sess.ApprovalGate(ctx, "delete_file", json.RawMessage(`{}`))
	if res.Approved {
		t.Error("pre-canceled context must return Approved=false")
	}
	if res.Err != "canceled" {
		t.Errorf("got res.Err = %q, want %q", res.Err, "canceled")
	}
}

func TestApprover_ContextCancellationWhilePending(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	appr := uiadapter.NewApprover(sess)

	ctx, cancel := context.WithCancel(context.Background())

	var (
		res  sdkadapter.ApprovalResult
		done = make(chan struct{})
	)

	go func() {
		res = sess.ApprovalGate(ctx, "delete_file", json.RawMessage(`{}`))
		close(done)
	}()

	select {
	case req := <-appr.Pending():
		if req.ToolName != "delete_file" {
			t.Fatalf("req.ToolName = %q, want delete_file", req.ToolName)
		}
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending request")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ApprovalGate did not unblock on context cancellation")
	}

	if res.Approved {
		t.Error("canceled gate must return Approved=false")
	}
	if res.Err != "canceled" {
		t.Errorf("got res.Err = %q, want %q", res.Err, "canceled")
	}
}

func TestApprover_EndToEndTurnExecution(t *testing.T) {
	tool := &approverTestTool{name: "dangerous_tool", class: tools.ExecutionWrite}
	reg := tools.NewRegistry()
	reg.Register(tool)

	comp := &scriptedCompleter{
		turns: []provider.Response{
			toolResponse("tc-1", "dangerous_tool", `{"action":"drop"}`),
			assistantResponse("all done"),
		},
	}

	sess := chat.NewSession(&config.Resolved{Model: "test-m"}, comp)
	sess.UseTools = true
	sess.Tools = reg

	appr := uiadapter.NewApprover(sess)
	conv := uiadapter.NewConversation(sess)

	handle, err := conv.Send(context.Background(), intent.Send{Text: "run tool"})
	if err != nil {
		t.Fatalf("conv.Send failed: %v", err)
	}

	var (
		sawPending bool
		sawStart   bool
		sawEnd     bool
		mu         sync.Mutex
	)

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range handle.Events() {
			mu.Lock()
			switch ev.Kind {
			case uievent.KindToolPending:
				sawPending = true
			case uievent.KindToolStart:
				sawStart = true
			case uievent.KindToolEnd:
				sawEnd = true
			}
			mu.Unlock()
		}
	}()

	select {
	case req := <-appr.Pending():
		if req.ToolName != "dangerous_tool" {
			t.Fatalf("req.ToolName = %q, want dangerous_tool", req.ToolName)
		}
		appr.Resolve(req.ID, ports.DecisionOnce)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for approval request")
	}

	select {
	case <-eventsDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for turn events to finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawPending {
		t.Error("expected to see tool.pending event")
	}
	if !sawStart {
		t.Error("expected to see tool.start event")
	}
	if !sawEnd {
		t.Error("expected to see tool.end event")
	}
}

func TestApproverAutoModeReturnsImmediately(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.ApprovalPolicy = "auto"
	_ = uiadapter.NewApprover(sess)

	res := sess.ApprovalGate(context.Background(), "some_tool", json.RawMessage(`{}`))
	if !res.Approved {
		t.Fatalf("expected Approved=true in auto mode, got %+v", res)
	}
}

// TestConcurrentIDLessCallsDoNotShareAnApprovalKey is the guarantee that
// decided the shape of this fix.
//
// An ID-less call (a provider stream sending the tool-call NAME delta before,
// or without, the ID delta) cannot raise an answerable prompt: the published
// key would have to be one this side can register, and the only thing left in
// the ctx is the tool NAME. A name is not a per-call identity. Parallel
// subagents all share one Approver, so two overlapping calls to one tool would
// collide on it: the second registration overwrites the first's channel, the
// operator is shown one prompt, and the single decision they make authorizes
// the OTHER call - a command they were never shown.
//
// So each such call gets its own generated id and blocks until its context
// dies. That is a worse outcome for one call and a much better one for the
// operator, and this test exists so the trade is not quietly undone.
func TestConcurrentIDLessCallsDoNotShareAnApprovalKey(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	appr := uiadapter.NewApprover(sess)

	// Cancellable, and cancelled on the way out: neither gate is ever
	// resolved here (that is the point - an ID-less call cannot be answered),
	// so on context.Background() both goroutines would block for the life of
	// the test binary.
	// Defer order matters: wg.Wait registered FIRST so it runs LAST. The
	// gates only return once the context is cancelled, so waiting before
	// cancelling deadlocks the test.
	var wg sync.WaitGroup
	defer wg.Wait()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := sdkagentloop.WithToolCall(base, sdkshape.ToolCall{
		Name: "run_command", Arguments: []byte(`{}`),
	})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sess.ApprovalGate(ctx, "run_command", json.RawMessage(`{}`))
		}()
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case req := <-appr.Pending():
			if seen[req.ID] {
				t.Fatalf("two in-flight calls were both keyed %q; one operator decision "+
					"would authorize the call they were not shown", req.ID)
			}
			seen[req.ID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for both prompts")
		}
	}
}
