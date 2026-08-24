package chat

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type approvalDummyTool struct {
	name    string
	class   tools.ExecutionClass
	started *atomic.Int32
}

func (t *approvalDummyTool) Name() string        { return t.name }
func (t *approvalDummyTool) Description() string { return "test tool" }
func (t *approvalDummyTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *approvalDummyTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: t.class, ResourceKey: "path:" + t.name}
}
func (t *approvalDummyTool) Execute(context.Context, json.RawMessage) (string, error) {
	if t.started != nil {
		t.started.Add(1)
	}
	return "dummy result", nil
}

func TestSessionApprovalGatedToolPromptsAndExecutes(t *testing.T) {
	started := &atomic.Int32{}
	tool := &approvalDummyTool{name: "write_tool", class: tools.ExecutionWrite, started: started}

	srv := sessionHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{
		{
			toolCalls: []provider.ToolCall{mkTC("call_1", "write_tool", `{}`)},
		},
		{
			content: "tool completed",
		},
	})
	defer srv.Close()

	s, _ := newSessionIntegrationHelper(t, srv)
	s.Tools = tools.NewRegistry()
	s.Tools.Register(tool)

	var gateCalls int
	s.ApprovalGate = func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		gateCalls++
		return sdkadapter.ApprovalResult{Approved: true}
	}

	reply, err := s.SendUser(context.Background(), "run write_tool", io.Discard)
	if err != nil {
		t.Fatalf("SendUser failed: %v", err)
	}
	if reply != "tool completed" {
		t.Fatalf("got reply %q, want 'tool completed'", reply)
	}
	if gateCalls != 1 {
		t.Fatalf("gate called %d times, want 1", gateCalls)
	}
	if started.Load() != 1 {
		t.Fatalf("tool started %d times, want 1", started.Load())
	}
}

func TestSessionApprovalGatedToolDenialPreventsExecution(t *testing.T) {
	started := &atomic.Int32{}
	tool := &approvalDummyTool{name: "write_tool", class: tools.ExecutionWrite, started: started}

	srv := sessionHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{
		{
			toolCalls: []provider.ToolCall{mkTC("call_1", "write_tool", `{}`)},
		},
		{
			content: "understood denial",
		},
	})
	defer srv.Close()

	s, _ := newSessionIntegrationHelper(t, srv)
	s.Tools = tools.NewRegistry()
	s.Tools.Register(tool)

	s.ApprovalGate = func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: false, Err: "denied by policy"}
	}

	reply, err := s.SendUser(context.Background(), "run write_tool", io.Discard)
	if err != nil {
		t.Fatalf("SendUser failed: %v", err)
	}
	if reply != "understood denial" {
		t.Fatalf("got reply %q, want 'understood denial'", reply)
	}
	if started.Load() != 0 {
		t.Fatalf("tool ran %d times after denial, want 0", started.Load())
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	var foundDenial bool
	for _, m := range s.Messages {
		if m.Role == provider.RoleTool && strings.Contains(m.Content, "denied by policy") {
			foundDenial = true
		}
	}
	if !foundDenial {
		t.Fatal("denial message not found in session history")
	}
}

func TestSessionApprovalStandingPersistenceAcrossTurns(t *testing.T) {
	started := &atomic.Int32{}
	tool := &approvalDummyTool{name: "write_tool", class: tools.ExecutionWrite, started: started}

	srv := sessionHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{
		// Turn 1
		{
			toolCalls: []provider.ToolCall{mkTC("call_t1", "write_tool", `{}`)},
		},
		{
			content: "turn 1 done",
		},
		// Turn 2
		{
			toolCalls: []provider.ToolCall{mkTC("call_t2", "write_tool", `{}`)},
		},
		{
			content: "turn 2 done",
		},
	})
	defer srv.Close()

	s, _ := newSessionIntegrationHelper(t, srv)
	s.Tools = tools.NewRegistry()
	s.Tools.Register(tool)

	standing := sdkadapter.NewApprovalStanding()
	s.ApprovalStanding = standing

	var gateCalls int
	s.ApprovalGate = func(_ context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		gateCalls++
		return sdkadapter.ApprovalResult{Approved: true, ApprovedForClass: true}
	}

	// Turn 1: prompts and records standing allow
	if _, err := s.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	if gateCalls != 1 {
		t.Fatalf("gate calls after turn 1 = %d, want 1", gateCalls)
	}

	// Turn 2: standing decision short-circuits gate
	if _, err := s.SendUser(context.Background(), "second", io.Discard); err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}
	if gateCalls != 1 {
		t.Fatalf("gate calls after turn 2 = %d, want 1 (second call short-circuits)", gateCalls)
	}
	if started.Load() != 2 {
		t.Fatalf("tool started %d times across two turns, want 2", started.Load())
	}
}
