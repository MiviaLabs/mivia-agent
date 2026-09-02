package chat

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
)

// The deferred-tool path invokes the runtime dispatcher DIRECTLY, underneath
// the SDK registry where the approval wrapper lives. It therefore carried no
// approval at all: a threat model drove a write tool through it and watched
// the file appear under a "deny" policy with a live approver attached.

// writingTool is Write-class, so any policy but "auto" must decide about it.
type writingTool struct{ ran bool }

func (*writingTool) Name() string               { return "write_file" }
func (*writingTool) Description() string        { return "writes" }
func (*writingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (*writingTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "write_file"}
}
func (t *writingTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.ran = true
	return "WROTE THE FILE", nil
}

// TestTheDeferredPathRefusesUnderADenyPolicy is the reproduction.
func TestTheDeferredPathRefusesUnderADenyPolicy(t *testing.T) {
	var gateCalls int
	s := &Session{
		ApprovalPolicy: "deny",
		ApprovalGate: func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
			gateCalls++
			return sdkadapter.ApprovalResult{Approved: true}
		},
	}

	got := s.decideDeferredApproval(promptableCtx(), &writingTool{}, "write_file", json.RawMessage(`{}`), noopPending)

	if got.Approved {
		t.Fatal("a write tool was approved on the deferred path under a \"deny\" " +
			"policy; the operator's most restrictive setting was bypassed by the " +
			"one route that does not go through the approval wrapper")
	}
	if gateCalls != 0 {
		t.Errorf("the gate was consulted %d times under a deny policy", gateCalls)
	}
	if !strings.Contains(got.Reason, "deny") {
		t.Errorf("reason = %q, want it to name the policy", got.Reason)
	}
}

// TestTheDeferredPathDeniesWithNoApprover holds the fail-closed direction on
// this path too: a policy that needs a decision, and nobody to ask.
func TestTheDeferredPathDeniesWithNoApprover(t *testing.T) {
	s := &Session{ApprovalPolicy: "write-only"}

	got := s.decideDeferredApproval(promptableCtx(), &writingTool{}, "write_file", json.RawMessage(`{}`), noopPending)

	if got.Approved {
		t.Error("a write tool ran on the deferred path with no approver attached; " +
			"the absence of an approver must never read as approval")
	}
}

// TestTheDeferredPathAsksTheGate proves it does not simply refuse everything -
// an operator who approves must get their tool.
func TestTheDeferredPathAsksTheGate(t *testing.T) {
	var asked string
	s := &Session{
		ApprovalPolicy: "write-only",
		ApprovalGate: func(_ context.Context, name string, _ json.RawMessage) sdkadapter.ApprovalResult {
			asked = name
			return sdkadapter.ApprovalResult{Approved: true}
		},
	}

	got := s.decideDeferredApproval(promptableCtx(), &writingTool{}, "write_file", json.RawMessage(`{}`), noopPending)

	if !got.Approved {
		t.Errorf("an approved call was refused: %q", got.Reason)
	}
	if asked != "write_file" {
		t.Errorf("the gate was asked about %q, want write_file", asked)
	}
}

// TestTheDeferredPathRunsUnderAutoWithoutAsking keeps the shipped default
// unchanged: auto means run, and adding a prompt there would be a regression
// for every user who never configured approvals.
func TestTheDeferredPathRunsUnderAutoWithoutAsking(t *testing.T) {
	var gateCalls int
	s := &Session{
		ApprovalPolicy: "auto",
		ApprovalGate: func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
			gateCalls++
			return sdkadapter.ApprovalResult{Approved: false}
		},
	}

	got := s.decideDeferredApproval(promptableCtx(), &writingTool{}, "write_file", json.RawMessage(`{}`), noopPending)

	if !got.Approved {
		t.Error("the auto policy refused a call; the shipped default must be unchanged")
	}
	if gateCalls != 0 {
		t.Errorf("the gate was consulted %d times under auto", gateCalls)
	}
}

// TestAnUnsetPolicyOnTheDeferredPathStillDecides covers the session that
// carries no policy at all - which a threat model found /new and /resume
// produce. On this path an unset policy must not mean "run".
func TestAnUnsetPolicyOnTheDeferredPathStillDecides(t *testing.T) {
	s := &Session{}

	got := s.decideDeferredApproval(promptableCtx(), &writingTool{}, "write_file", json.RawMessage(`{}`), noopPending)

	if got.Approved {
		t.Error("a session with no approval policy ran a write tool on the deferred " +
			"path; an unset policy is not a licence to execute")
	}
}

// TestRunDeferredToolNowItselfRefuses drives the REAL deferred path, not the
// decision helper.
//
// Deleting the guard from runDeferredToolNow leaves every test above green,
// because they all call decideDeferredApproval directly. That is the shape
// that has shipped several things dead in this repo, so the call site gets its
// own test that executes it.
func TestRunDeferredToolNowItselfRefuses(t *testing.T) {
	tool := &writingTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	s := &Session{ApprovalPolicy: "deny"}
	content, _, _, failed, ok := s.runDeferredToolNow(
		context.Background(), d, func() *tools.Registry { return reg },
		"sess-1", 1, "write_file", json.RawMessage(`{}`), noopPending,
	)

	if tool.ran {
		t.Fatal("the deferred path EXECUTED a write tool under a \"deny\" policy; " +
			"this is the route that bypasses the approval wrapper entirely")
	}
	if !ok {
		t.Fatal("the refusal was not reported back to the loop, so the model gets " +
			"no result for a call it made")
	}
	if !strings.Contains(content, "denied") {
		t.Errorf("the model was told %q, want a refusal", content)
	}
	if !failed {
		t.Error("the refusal is reported as a completed call, so every viewer " +
			"renders a denied write as a successful one")
	}
}

// TestRunDeferredToolNowRunsWhenApproved is the other direction on the real
// path: an approved call must still execute and return its output.
func TestRunDeferredToolNowRunsWhenApproved(t *testing.T) {
	tool := &writingTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	s := &Session{ApprovalPolicy: "auto"}
	content, _, _, failed, ok := s.runDeferredToolNow(
		context.Background(), d, func() *tools.Registry { return reg },
		"sess-1", 1, "write_file", json.RawMessage(`{}`), noopPending,
	)

	if !ok {
		t.Fatal("the deferred path reported no result for an approved call")
	}
	if !tool.ran {
		t.Error("an approved call did not run")
	}
	if !strings.Contains(content, "WROTE") {
		t.Errorf("content = %q, want the tool's own output", content)
	}
	if failed {
		t.Error("a call that ran and succeeded is marked failed")
	}
}

// unclassifiedTool declares no Capability at all. Several real tools have this
// shape - post_message and run_messages in internal/clichat, every workflow_*
// tool in internal/workflows/ledger - so the "unclassified is ExecutionExternal"
// default is production-reachable, not a defensive branch.
type unclassifiedTool struct{ ran bool }

func (*unclassifiedTool) Name() string               { return "workflow_deliver" }
func (*unclassifiedTool) Description() string        { return "publishes a workflow run" }
func (*unclassifiedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *unclassifiedTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.ran = true
	return "published", nil
}

// TestAnUnclassifiedToolIsGatedAtTheMostRestrictiveClass pins the default.
//
// It probes the class through the STANDING store rather than through "was the
// gate asked". The gate signature carries no class, so asking it proves only
// that the class is at or above Write - a mutation from External to Write
// would survive that. A standing decision is recorded per class, so a denial
// recorded at External is consulted only if the call really is classified
// External.
func TestAnUnclassifiedToolIsGatedAtTheMostRestrictiveClass(t *testing.T) {
	standing := sdkadapter.NewApprovalStanding()
	standing.Deny(sdkadapter.StandingKey{
		Name:  "workflow_deliver",
		Class: tools.ExecutionExternal,
		Args:  json.RawMessage(`{}`),
	})

	var gateCalls int
	s := &Session{
		ApprovalPolicy:   "write-only",
		ApprovalStanding: standing,
		ApprovalGate: func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
			gateCalls++
			return sdkadapter.ApprovalResult{Approved: true}
		},
	}

	got := s.decideDeferredApproval(promptableCtx(), &unclassifiedTool{},
		"workflow_deliver", json.RawMessage(`{}`), noopPending)

	if got.Approved {
		t.Error("an unclassified tool was approved past a standing denial recorded " +
			"at ExecutionExternal, so it is not being classified there - a tool " +
			"that declares nothing must be gated at the most restrictive class, " +
			"not waved through")
	}
	if gateCalls != 0 {
		t.Errorf("the gate was consulted %d times despite a matching standing "+
			"denial, so the class the decision used is not the one recorded",
			gateCalls)
	}
}

// TestTheDeferredPathStampsThePolicyOnTheContext is the effect the empty-policy
// default actually has.
//
// "" and "write-only" take identical branches inside DecideApproval, so a test
// that only checks the outcome cannot see the default at all. What it changes
// is the context stamp: WithApprovalPolicy skips an empty policy, and the gate
// then falls back to ITS OWN session's policy - which is the cross-session
// /yolo leak that stamp exists to close.
func TestTheDeferredPathStampsThePolicyOnTheContext(t *testing.T) {
	var stamped string
	var found bool
	s := &Session{
		// Deliberately unset, the shape a session built by /new or /resume had.
		ApprovalPolicy: "",
		ApprovalGate: func(ctx context.Context, _ string, _ json.RawMessage) sdkadapter.ApprovalResult {
			stamped, found = sdkadapter.ApprovalPolicyFromContext(ctx)
			return sdkadapter.ApprovalResult{Approved: true}
		},
	}

	s.decideDeferredApproval(promptableCtx(), &writingTool{}, "write_file", json.RawMessage(`{}`), noopPending)

	if !found {
		t.Fatal("the deciding policy was not stamped on the context, so a gate " +
			"shared across sessions answers from a different session's policy")
	}
	if stamped != config.ApprovalPolicyWriteOnly {
		t.Errorf("stamped %q, want write-only: an unset policy must resolve to a "+
			"real one before it travels", stamped)
	}
}

// Under an INTERACTIVE policy the deferred path must raise a prompt the
// operator can actually answer.
//
// It did not. decideDeferredApproval passed no EmitPending, and the shipped
// TUI arms its approval prompt exclusively from the tool.pending event that
// EmitPending produces (nothing drains Approver.Pending() in live mode - its
// own comment says so). So DecideApproval called the gate, uiadapter's gate
// registered a waiter and blocked on its channel, and no prompt was ever
// drawn: the turn hung until the operator cancelled it, with nothing on
// screen explaining why. The doc comment on decideDeferredApproval asserted
// the opposite - "makes an interactive policy deny rather than hang".
//
// Its stated premise, "this path has no in-flight SDK call id", was also
// wrong: the SDK stamps one into the ctx it hands this handler, and
// uiadapter's gate already keys its waiter off that same id.
func TestAnInteractiveDeferredCallRaisesAPromptTheOperatorCanAnswer(t *testing.T) {
	tool := &writingTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	approve := make(chan struct{})
	var pending []agent.Event
	s := &Session{
		ApprovalPolicy: config.ApprovalPolicyWriteOnly,
		// The gate blocks exactly as uiadapter's does: it answers only when
		// something resolves the prompt. A synchronous fake gate is what let
		// every existing test on this path miss the hang.
		ApprovalGate: func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
			select {
			case <-approve:
				return sdkadapter.ApprovalResult{Approved: true}
			case <-ctx.Done():
				return sdkadapter.ApprovalResult{Err: "canceled"}
			}
		},
		OnAgentEvent: func(e agent.Event) {
			if e.Kind != agent.EventToolPending {
				return
			}
			pending = append(pending, e)
			// The operator answering. Resolving is keyed by ToolCallID, so a
			// prompt carrying the wrong id could never unblock the gate.
			if e.ToolCallID == "call-42" {
				close(approve)
			}
		},
	}
	s.PublishAgentSurface("p", 0, reg, nil, nil, "", reg.OpenAITools())
	s.SetDispatcher(d)
	s.ToolBaseResolver = func() *tools.Registry { return reg }

	var opts agent.Options
	opts.OnEvent = s.OnAgentEvent
	s.wireStepBoundaryAdmission(&opts, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = toolcallctx.WithToolCall(ctx, sdkshape.ToolCall{ID: "call-42", Name: "write_file"})

	done := make(chan agent.UnadmittedToolResult, 1)
	go func() { done <- opts.UnadmittedToolHandler(ctx, "write_file", json.RawMessage(`{}`)) }()

	select {
	case result := <-done:
		if !tool.ran {
			t.Fatalf("the operator approved and the tool still did not run: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the deferred call blocked with no prompt the operator could " +
			"answer - the turn hangs until it is cancelled, showing nothing")
	}

	if len(pending) != 1 {
		t.Fatalf("want exactly one pending prompt, got %d", len(pending))
	}
	if pending[0].ToolCallID != "call-42" {
		t.Errorf("the prompt carries ToolCallID %q, not the in-flight call id; "+
			"the UI resolves by that id, so every answer would be a silent no-op",
			pending[0].ToolCallID)
	}
	if pending[0].Name != "write_file" {
		t.Errorf("the prompt does not name the tool: %+v", pending[0])
	}
}

// When there is genuinely no call id to key a prompt to - a direct caller, a
// legacy backend - the call must be REFUSED with a reason, never left blocked
// on a prompt that cannot be raised or answered.
func TestADeferredCallWithNoCallIDRefusesInsteadOfBlocking(t *testing.T) {
	tool := &writingTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	s := &Session{
		ApprovalPolicy: config.ApprovalPolicyWriteOnly,
		ApprovalGate: func(ctx context.Context, _ string, _ json.RawMessage) sdkadapter.ApprovalResult {
			<-ctx.Done() // never answered, exactly like a prompt nobody sees
			return sdkadapter.ApprovalResult{Err: "canceled"}
		},
	}
	s.PublishAgentSurface("p", 0, reg, nil, nil, "", reg.OpenAITools())
	s.SetDispatcher(d)
	s.ToolBaseResolver = func() *tools.Registry { return reg }

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	done := make(chan agent.UnadmittedToolResult, 1)
	go func() {
		// No toolcallctx on this ctx.
		done <- opts.UnadmittedToolHandler(context.Background(), "write_file", json.RawMessage(`{}`))
	}()

	select {
	case result := <-done:
		if tool.ran {
			t.Fatal("the tool ran without an approval anybody gave")
		}
		if !strings.Contains(result.Content, "cannot be raised") {
			t.Errorf("the refusal does not say why it could not ask: %q", result.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call blocked on a prompt that can never be raised or answered")
	}
}

// promptableCtx is the ctx the SDK really hands the unadmitted-tool handler:
// one carrying the in-flight tool call. decideDeferredApproval refuses rather
// than consults a gate without it, because a prompt keyed to nothing can never
// be answered - so a test that means to exercise the GATE must supply one.
func promptableCtx() context.Context {
	return toolcallctx.WithToolCall(context.Background(),
		sdkshape.ToolCall{ID: "call-1", Name: "write_file"})
}

// noopPending stands in for the UI that would draw the prompt. It must be
// non-nil for the same reason: a decision with nowhere to publish the prompt
// is refused, not blocked on.
func noopPending(toolCallID, name, detail, input string) {}

// slowTool blocks until its ctx is cancelled, and declares its own timeout.
type slowTool struct {
	timeout time.Duration
	started chan struct{}
	once    sync.Once
}

func (*slowTool) Name() string               { return "run_command" }
func (*slowTool) Description() string        { return "blocks" }
func (*slowTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *slowTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionExternal, Timeout: t.timeout}
}
func (t *slowTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.once.Do(func() { close(t.started) })
	<-ctx.Done()
	return "", ctx.Err()
}

// A deferred call must be bounded by the tool's declared timeout.
//
// The admitted path arms one (armDispatcherTimeout, then Timeout on the
// request). The deferred path set none and never narrowed the ctx, so the
// FIRST call to a deferred run_command ran unbounded while the identical call
// one step later - once the tool was natively admitted - was bounded. A
// timeout the tool declared for itself was silently dropped.
func TestADeferredCallIsBoundedByTheToolsDeclaredTimeout(t *testing.T) {
	tool := &slowTool{timeout: 150 * time.Millisecond, started: make(chan struct{})}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	s := &Session{ApprovalPolicy: config.ApprovalPolicyAuto}
	s.PublishAgentSurface("p", 0, reg, nil, nil, "", reg.OpenAITools())
	s.SetDispatcher(d)
	s.ToolBaseResolver = func() *tools.Registry { return reg }

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)

	done := make(chan agent.UnadmittedToolResult, 1)
	go func() {
		done <- opts.UnadmittedToolHandler(promptableCtx(), "run_command", json.RawMessage(`{}`))
	}()

	select {
	case result := <-done:
		if !result.Failed {
			t.Errorf("a call that timed out is not reported as failed: %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the deferred call ran unbounded: the tool declared a 150ms " +
			"timeout and nothing armed it, so a hanging tool hangs the turn")
	}
}

// ...and that timeout must NOT bound the operator's approval wait.
//
// This is the trap in fixing the above. On the admitted path the approval
// wrapper sits OUTSIDE the dispatcher shim, so the clock starts only after the
// operator has answered. Arming it around the inline approval here would put a
// 60s default deadline around a human reading a prompt: uiadapter's gate
// selects on ctx.Done() and answers "canceled", so the prompt would silently
// auto-deny mid-read and report a refusal the operator never made. That is a
// worse bug than the unbounded call.
func TestTheDeferredTimeoutDoesNotBoundTheOperatorsApprovalWait(t *testing.T) {
	tool := &writingTool{}
	reg := tools.NewRegistry()
	reg.Register(tool)
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	s := &Session{
		ApprovalPolicy: config.ApprovalPolicyWriteOnly,
		// The operator "reads" for longer than any per-call tool timeout.
		ApprovalGate: func(ctx context.Context, _ string, _ json.RawMessage) sdkadapter.ApprovalResult {
			select {
			case <-time.After(300 * time.Millisecond):
				return sdkadapter.ApprovalResult{Approved: true}
			case <-ctx.Done():
				return sdkadapter.ApprovalResult{Err: "canceled"}
			}
		},
		// Shorter than the operator's wait: if this bounds the approval, the
		// gate is cancelled before it can answer.
		ToolTimeout: 50 * time.Millisecond,
	}
	s.PublishAgentSurface("p", 0, reg, nil, nil, "", reg.OpenAITools())
	s.SetDispatcher(d)
	s.ToolBaseResolver = func() *tools.Registry { return reg }

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	result := opts.UnadmittedToolHandler(promptableCtx(), "write_file", json.RawMessage(`{}`))

	if !tool.ran {
		t.Fatalf("the operator approved and the call did not run - the tool "+
			"timeout was armed around the human, and the prompt auto-denied "+
			"while they were still reading it: %+v", result)
	}
}
