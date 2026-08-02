package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// elisionMarker is a unique oversized tool body. Length is strictly above the
// planner elision threshold (2048) so a later compaction path can replace it.
func elisionMarker() string {
	return "ELISION_MARK_" + strings.Repeat("Z", 3000)
}

type fixedBodyTool struct {
	name string
	body string
}

func (t fixedBodyTool) Name() string               { return t.name }
func (t fixedBodyTool) Description() string        { return "returns a fixed body" }
func (t fixedBodyTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t fixedBodyTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (t fixedBodyTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

// elisionScriptCompleter drives two agent turns with a process-local step
// counter. Compaction can drop earlier user messages, so counting RoleUser in
// the request is not a stable turn signal.
//
//	turn 1: tool_calls (big tool) then final
//	turn 2: optional second tool then final (multiStepTurn2)
type elisionScriptCompleter struct {
	multiStepTurn2 bool
	toolName       string
	smallToolName  string
	calls          int
}

func (c *elisionScriptCompleter) Name() string { return "elision-script" }
func (c *elisionScriptCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
func (c *elisionScriptCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *elisionScriptCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	switch c.calls {
	case 1:
		// Turn 1 step 1: plant oversized prior tool result.
		return toolCallResponse("tc-elide-1", c.toolName), nil
	case 2:
		return &provider.Response{Content: "done-1", FinishReason: "stop"}, nil
	case 3:
		if c.multiStepTurn2 {
			name := c.smallToolName
			if name == "" {
				name = c.toolName
			}
			// Unique ID — prior unit remains in history after retention.
			return toolCallResponse("tc-elide-2", name), nil
		}
		return &provider.Response{Content: "done-2", FinishReason: "stop"}, nil
	default:
		return &provider.Response{Content: "done-2", FinishReason: "stop"}, nil
	}
}

func toolCallResponse(id, name string) *provider.Response {
	var call provider.ToolCall
	call.ID = id
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = `{}`
	return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}
}

func newElisionSession(t *testing.T, store contextstate.Store, completer provider.Completer, marker string, multiStepTurn2 bool) (*Session, contextstate.Principal) {
	t.Helper()
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(fixedBodyTool{name: "elision_probe_tool", body: marker})
	session.Tools.Register(fixedBodyTool{name: "elision_small_tool", body: "ok"})
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
	_ = multiStepTurn2 // documented on completer construction
	return session, principal
}

// forceCompactionBudget tightens the session prompt budget so the next user
// turn's estimate sits at or above the structural compaction trigger.
func forceCompactionBudget(t *testing.T, session *Session, nextUser string) {
	t.Helper()
	msgs := append(session.MessagesCopy(), provider.Message{Role: provider.RoleUser, Content: nextUser})
	var toolSpecs []provider.ToolSpec
	if session.Tools != nil {
		toolSpecs = session.Tools.OpenAITools()
	}
	cost, err := provider.EstimatePromptCost(msgs, toolSpecs)
	if err != nil {
		t.Fatal(err)
	}
	// budget == cost ⇒ trigger = floor(4/5*budget) < before ⇒ planCompact runs.
	if err := session.SetPromptBudget(cost); err != nil {
		// If the model base is smaller than cost (unlikely with defaults),
		// clamp to the effective budget and still expect compaction under Force
		// is not available on agent turns — fail loudly so the fixture is fixed.
		t.Fatalf("SetPromptBudget(%d): %v (PromptBudget=%d)", cost, err, session.PromptBudget())
	}
}

func completeCheckpointBodies(t *testing.T, db *sql.DB, sessionID string) [][]byte {
	t.Helper()
	rows, err := db.Query(`
		SELECT active_context FROM context_checkpoints
		WHERE session_id=? AND complete=1
		ORDER BY durable_revision ASC
	`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var bodies [][]byte
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return bodies
}

// TestIntegrationElisionCommitsActiveWhilePriorCheckpointKeepsBody is the
// durable §7 proof: turn 1 commits an oversized prior tool body; turn 2
// elides it in the new active context while the earlier complete checkpoint
// row still holds the original bytes. Observation uses store.Load plus the
// established test-only RO SQL pattern — no product history API.
func TestIntegrationElisionCommitsActiveWhilePriorCheckpointKeepsBody(t *testing.T) {
	store, db := openSharedContextStore(t)
	marker := elisionMarker()
	// multiStepTurn2: a newer tool unit must exist before the prior oversized
	// result is non-mandatory (latest tool unit is never elided).
	completer := &elisionScriptCompleter{
		multiStepTurn2: true,
		toolName:       "elision_probe_tool",
		smallToolName:  "elision_small_tool",
	}
	session, principal := newElisionSession(t, store, completer, marker, true)

	if _, err := session.SendUser(context.Background(), "read big file", io.Discard); err != nil {
		t.Fatal(err)
	}
	priorSnap, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(priorSnap.Active.ActiveContext), marker) {
		t.Fatal("turn-1 active context missing oversized tool body")
	}
	priorBody := append([]byte(nil), priorSnap.Active.ActiveContext...)
	priorDurable := priorSnap.Revision.Durable

	nextUser := "follow up"
	forceCompactionBudget(t, session, nextUser)
	if _, err := session.SendUser(context.Background(), nextUser, io.Discard); err != nil {
		t.Fatal(err)
	}

	afterSnap, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	active := string(afterSnap.Active.ActiveContext)
	if strings.Contains(active, marker) {
		t.Fatal("turn-2 active context still contains original oversized body")
	}
	if !strings.Contains(active, "context elided prior tool result") {
		t.Fatalf("turn-2 active context missing elision notice: %s", active)
	}
	if afterSnap.Revision.Durable <= priorDurable {
		t.Fatalf("durable revision did not advance: before=%d after=%d", priorDurable, afterSnap.Revision.Durable)
	}

	bodies := completeCheckpointBodies(t, db, session.SessionID)
	if len(bodies) < 2 {
		t.Fatalf("complete checkpoints=%d, want ≥2", len(bodies))
	}
	// Immediately preceding complete row must still hold the original body.
	priorRow := bodies[len(bodies)-2]
	if !strings.Contains(string(priorRow), marker) {
		t.Fatal("prior complete checkpoint lost original oversized body")
	}
	if string(priorRow) != string(priorBody) {
		// Rows can differ only if something rewrote the prior blob; byte-equal
		// to the Load capture is the strongest immutability claim.
		t.Fatal("prior checkpoint active_context diverged from turn-1 Load capture")
	}
	latest := bodies[len(bodies)-1]
	if strings.Contains(string(latest), marker) {
		t.Fatal("latest checkpoint still contains original body")
	}

	if err := provider.ValidateToolPairing(session.MessagesCopy()); err != nil {
		t.Fatalf("pairing after elision: %v", err)
	}
	for _, msg := range session.MessagesCopy() {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "tc-elide-1" {
			if msg.Content == marker || !strings.HasPrefix(msg.Content, "[context elided prior tool result;") {
				t.Fatalf("in-memory prior tool body not elided: %q", msg.Content[:min(80, len(msg.Content))])
			}
		}
	}
}

// TestIntegrationElisionMultiStepTurnPublishesCompactionEventCounters pins the
// chat publication path: a multi-step turn whose first prepare elides (real
// StructuralPreparationManager) still seals one CompactionEvent with
// aggregate counters after commit.
func TestIntegrationElisionMultiStepTurnPublishesCompactionEventCounters(t *testing.T) {
	store, _ := openSharedContextStore(t)
	marker := elisionMarker()
	completer := &elisionScriptCompleter{
		multiStepTurn2: true,
		toolName:       "elision_probe_tool",
		smallToolName:  "elision_small_tool",
	}
	session, _ := newElisionSession(t, store, completer, marker, true)

	bus := events.New()
	session.EventBus = bus
	var typed []agent.Event
	var busEvents []events.Event
	session.OnAgentEvent = func(ev agent.Event) {
		if ev.Kind == agent.EventCompaction {
			typed = append(typed, ev)
		}
	}
	bus.Subscribe(events.KindCompaction, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		busEvents = append(busEvents, ev)
	}))

	if _, err := session.SendUser(context.Background(), "read big file", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(typed) != 0 {
		t.Fatalf("unexpected compaction on turn 1: %d", len(typed))
	}

	nextUser := "follow up multi-step"
	forceCompactionBudget(t, session, nextUser)
	if _, err := session.SendUser(context.Background(), nextUser, io.Discard); err != nil {
		t.Fatal(err)
	}
	bus.Flush()

	if len(typed) != 1 {
		t.Fatalf("compaction events=%d, want 1", len(typed))
	}
	ev := typed[0]
	if ev.Compaction == nil {
		t.Fatal("typed event missing Compaction payload")
	}
	if ev.Compaction.ElidedMessages < 1 {
		t.Fatalf("ElidedMessages=%d, want ≥1", ev.Compaction.ElidedMessages)
	}
	if ev.Compaction.ElidedBytes != len(marker) {
		t.Fatalf("ElidedBytes=%d, want %d", ev.Compaction.ElidedBytes, len(marker))
	}
	if ev.Compaction.AfterTokens > ev.Compaction.BeforeTokens {
		t.Fatalf("AfterTokens %d > BeforeTokens %d", ev.Compaction.AfterTokens, ev.Compaction.BeforeTokens)
	}
	if ev.Content != "" || ev.Input != "" || ev.Output != "" {
		t.Fatalf("generic content on compaction event: %+v", ev)
	}
	if strings.Contains(ev.Detail, marker) || strings.Contains(ev.Detail, "elision_probe_tool") {
		t.Fatalf("detail leaked tool content/name: %q", ev.Detail)
	}
	if !strings.Contains(ev.Detail, "elided") {
		t.Fatalf("detail missing elision counts: %q", ev.Detail)
	}

	raw, err := events.MarshalCompactionEvent(*ev.Compaction)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if strings.Contains(encoded, marker) || strings.Contains(encoded, "elision_probe_tool") || strings.Contains(encoded, "SECRET") {
		t.Fatalf("serialized event leaked content: %s", encoded)
	}

	if len(busEvents) != 1 {
		t.Fatalf("bus compaction events=%d, want 1", len(busEvents))
	}
	if strings.Contains(busEvents[0].Detail, marker) {
		t.Fatalf("bus detail leaked marker: %q", busEvents[0].Detail)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
