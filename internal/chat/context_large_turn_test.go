package chat

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// largeResultTool stands in for read_file/grep/run_command, whose results are
// uncapped by default and routinely run to hundreds of kilobytes.
type largeResultTool struct{ size int }

func (largeResultTool) Name() string               { return "large_result_tool" }
func (largeResultTool) Description() string        { return "returns a large result" }
func (largeResultTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (largeResultTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (t largeResultTool) Execute(context.Context, json.RawMessage) (string, error) {
	return strings.Repeat("x", t.size), nil
}

// toolOnThirdTurnCompleter answers with prose until the third user message,
// which it serves with one tool call - the shape of a chat that opens with
// small talk and then asks the agent to actually read something.
type toolOnThirdTurnCompleter struct{}

func (toolOnThirdTurnCompleter) Name() string { return "large-turn" }
func (c toolOnThirdTurnCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	response, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}
func (c toolOnThirdTurnCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (toolOnThirdTurnCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	users := 0
	for _, message := range req.Messages {
		if message.Role == provider.RoleUser {
			users++
		}
	}
	if users >= 3 && req.Messages[len(req.Messages)-1].Role == provider.RoleUser {
		var call provider.ToolCall
		call.ID = "tc-large"
		call.Type = "function"
		call.Function.Name = "large_result_tool"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "answer", FinishReason: "stop"}, nil
}

func newLargeTurnSession(t *testing.T, store contextstate.Store, resultSize int) (*Session, contextstate.Principal) {
	t.Helper()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, toolOnThirdTurnCompleter{})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(largeResultTool{size: resultSize})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	return session, principal
}

// TestIntegrationLargeToolTurnStillCommits is the durable regression for the
// live wedge: `mivia chat` stopped persisting history the moment the agent did
// real work. A tool result is uncapped by default, so the turn's active context
// crossed a 32 KiB checkpoint bound and a 64 KiB per-payload bound that nothing
// upstream enforces, and the ENTIRE commit was refused - no source events, no
// checkpoint, no operation row. Because an active context only grows, every
// later turn failed the same way and the session never recovered.
//
// The sizes bracket both old bounds deliberately: 40 KiB tripped the checkpoint
// limit, 90 KiB tripped the payload limit, and 300 KiB is an ordinary
// `read_file` of a mid-sized source file.
func TestIntegrationLargeToolTurnStillCommits(t *testing.T) {
	for _, resultSize := range []int{40 * 1024, 90 * 1024, 300 * 1024} {
		store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "large.db"))
		if err != nil {
			t.Fatal(err)
		}
		session, principal := newLargeTurnSession(t, store, resultSize)
		for turn, question := range []string{"hello", "how are you", "read the file"} {
			if _, err := session.SendUser(context.Background(), question, io.Discard); err != nil {
				_ = store.Close()
				t.Fatalf("result=%dB turn %d (%q): %v", resultSize, turn+1, question, err)
			}
		}
		snapshot, err := store.Load(context.Background(), principal, session.SessionID)
		if err != nil {
			_ = store.Close()
			t.Fatalf("result=%dB load: %v", resultSize, err)
		}
		// Three turns: two prose pairs plus user + tool_call + tool_result +
		// answer for the tool turn.
		if snapshot.Revision.Durable != 3 || snapshot.Revision.Source != 8 {
			_ = store.Close()
			t.Fatalf("result=%dB durable revision = %+v, want Durable 3 / Source 8", resultSize, snapshot.Revision)
		}
		if got := len(session.MessagesCopy()); got != 9 {
			_ = store.Close()
			t.Fatalf("result=%dB in-memory history = %d messages, want 9", resultSize, got)
		}
		_ = store.Close()
	}
}

// TestDurableLimitsAreUncappedByDefault pins the policy this repo ships: a
// durable size bound is opt-in configuration, never a compiled-in ceiling that
// silently destroys a turn the agent already completed.
func TestDurableLimitsAreUncappedByDefault(t *testing.T) {
	limits := contextstate.DefaultLimits()
	if limits != (contextstate.Limits{}) {
		t.Fatalf("default durable limits = %+v, want every bound uncapped", limits)
	}
}
