package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// steerEchoCompleter forces two steps: first requests a no-op tool, second
// echoes whether a <parent-message> frame appears in history (proves BeforeStep
// inject from the mailbox path).
type steerEchoCompleter struct {
	mu           sync.Mutex
	calls        int
	lastMessages []provider.Message
	sawParent    bool
}

func (c *steerEchoCompleter) Name() string { return "steer-echo" }
func (c *steerEchoCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
func (c *steerEchoCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *steerEchoCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastMessages = append([]provider.Message(nil), req.Messages...)
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "<parent-message>") {
			c.sawParent = true
		}
	}
	if c.calls == 1 {
		var tc provider.ToolCall
		tc.ID = "ping-1"
		tc.Type = "function"
		tc.Function.Name = "ping"
		tc.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{tc}, FinishReason: "tool_calls"}, nil
	}
	if c.sawParent {
		return &provider.Response{Content: "SAW_PARENT_STEER", FinishReason: "stop"}, nil
	}
	return &provider.Response{Content: "NO_PARENT_STEER", FinishReason: "stop"}, nil
}

type pingTool struct {
	onExec func()
}

func (pingTool) Name() string               { return "ping" }
func (pingTool) Description() string        { return "no-op ping" }
func (pingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t pingTool) Execute(context.Context, json.RawMessage) (string, error) {
	if t.onExec != nil {
		t.onExec()
	}
	return "pong", nil
}

type steerSendTarget struct {
	runID, taskID string
	coord         coordinator.Coordinator
	handle        *coordinator.RunHandle
}

func registerSteerPing(t *testing.T, reg *tools.Registry, ready <-chan steerSendTarget) {
	t.Helper()
	var sendOnce sync.Once
	reg.Register(pingTool{onExec: func() {
		sendOnce.Do(func() {
			select {
			case tgt := <-ready:
				msg, err := agentmsg.NewMessage(tgt.runID, agentmsg.KindSteer,
					agentmsg.Party{Role: agentmsg.ParentSentinel},
					agentmsg.Party{TaskID: tgt.taskID},
					"stop expanding scope", nil,
					agentmsg.Options{ID: "steer-mid-1"})
				if err != nil {
					t.Errorf("NewMessage: %v", err)
					return
				}
				if _, err := tgt.coord.SendToTask(context.Background(), tgt.handle, tgt.taskID, msg); err != nil {
					t.Errorf("SendToTask: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Error("send target never ready")
			}
		})
	}})
}

func assertSteerLedgerParent(t *testing.T, coord coordinator.Coordinator, runID, taskID string) {
	t.Helper()
	list, err := coord.ListRunMessages(context.Background(), runID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.Kind != agentmsg.KindSteer {
			continue
		}
		full, err := coord.LoadMessageBody(context.Background(), m.ContentRef)
		if err != nil {
			t.Fatal(err)
		}
		if !full.From.IsParent() {
			t.Fatalf("steer From not parent: %+v", full.From)
		}
		return
	}
	t.Fatalf("steer missing from ledger: %+v", list)
}

// TestSteerMidRunVisibleToMultiStepChild is the plan 53.03 integration gate:
// SendToTask enqueues a steer during the child's tool call; the next step
// boundary injects a framed <parent-message> into history before the model call.
func TestSteerMidRunVisibleToMultiStepChild(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	comp := &steerEchoCompleter{}
	reg := tools.NewRegistry()
	ready := make(chan steerSendTarget, 1)
	registerSteerPing(t, reg, ready)

	h := &subagents.MultiStepHandler{
		Completer: comp, FullRegistry: reg, Dispatcher: d,
		Model: "test-model", MaxSteps: 5, SystemPrompt: "You are a test agent.",
	}
	if err := d.Register(runtime.Subagent, "steered", h); err != nil {
		t.Fatal(err)
	}
	coord := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	taskID := "steer-task-1"
	handle, err := coord.Spawn(context.Background(), []subagents.Task{
		{ID: taskID, Name: "steered", AgentName: "steered", Input: json.RawMessage(`"do work"`), Timeout: 10 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := coord.Inspect(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	ready <- steerSendTarget{runID: snap.RunID, taskID: taskID, coord: coord, handle: handle}

	result, err := coord.Join(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Err != nil {
		t.Fatalf("result=%+v", result.Results)
	}
	out := string(result.Results[0].Output)
	comp.mu.Lock()
	saw, calls := comp.sawParent, comp.calls
	comp.mu.Unlock()
	if !strings.Contains(out, "SAW_PARENT_STEER") && !saw {
		t.Fatalf("child did not see framed parent steer; output=%q sawParent=%v calls=%d", out, saw, calls)
	}
	assertSteerLedgerParent(t, coord, snap.RunID, taskID)
}
