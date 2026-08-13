package chat

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// eventPublishingWriter republishes each write as a live "delta"
// EventAssistant on bus before forwarding it to w, so a plain (non-tool)
// chat turn's content streams to a cross-process observer (internal/hub's
// relay) exactly like the agent-loop path's own teeWriter
// (internal/agent/loop.go) already does for a tool-enabled turn. Both use
// Detail="delta" so internal/cli/chat_hub.go's relay treats them
// identically regardless of which path produced them - a --no-tools
// session (which never reaches internal/agent at all) previously never
// published to EventBus, so a relayed --no-tools turn showed no live
// activity whatsoever, not even the "thinking" the tool-enabled gap this
// fixes a sibling of still showed.
type eventPublishingWriter struct {
	w         io.Writer
	bus       *events.Bus
	sessionID string
	turnID    string
}

func (e *eventPublishingWriter) Write(p []byte) (int, error) {
	if len(p) > 0 && e.bus != nil {
		e.bus.Publish(events.Event{
			Kind: events.KindAssistant, SessionID: e.sessionID, TurnID: e.turnID,
			Content: string(p), Detail: "delta",
		})
	}
	if e.w == nil {
		return len(p), nil
	}
	return e.w.Write(p)
}

// plainTurnStreamWriter builds the writer chain both sendPlainLegacy and
// sendPlainContext stream a reply through: the caller's own writer (if
// any) plus the interrupted-turn-recovery capture buffer, wrapped in
// eventPublishingWriter when the session has a bus to publish to (nil
// otherwise - a session with no hub membership, and every test that builds
// a Session by hand, has no EventBus and pays nothing extra).
func (s *Session) plainTurnStreamWriter(w io.Writer, captured *strings.Builder, turnID string) io.Writer {
	streamWriter := io.Writer(captured)
	if w != nil {
		streamWriter = io.MultiWriter(w, captured)
	}
	if s.EventBus == nil {
		return streamWriter
	}
	return &eventPublishingWriter{w: streamWriter, bus: s.EventBus, sessionID: s.SessionID, turnID: turnID}
}

// sendPlainLegacy streams a plain-chat turn against the pruned (non-compacting)
// message history.
func (s *Session) sendPlainLegacy(ctx context.Context, persistedText string, w io.Writer, snapshot plainTurnSnapshot) (string, error) {
	prepared := provider.PruneMessagesKeepTurns(snapshot.messages, snapshot.budget)
	if snapshot.budget > 0 && provider.MessagesTokens(prepared) > snapshot.budget {
		return "", fmt.Errorf("%w (%d > %d tokens)", agent.ErrPromptBudgetExceeded, provider.MessagesTokens(prepared), snapshot.budget)
	}
	// The tee captures the already-streamed bytes: on an interrupted turn
	// ChatStream returns "" as the reply, so the writer is the only record of
	// the partial answer the user already read on screen. A nil caller writer
	// (tests, headless callers) keeps the capture-only surface.
	var captured strings.Builder
	streamWriter := s.plainTurnStreamWriter(w, &captured, fmt.Sprintf("turn:%d", snapshot.myTurn))
	reply, err := snapshot.binding.Completer.ChatStream(ctx, provider.Request{
		Model: snapshot.binding.Model, Messages: prepared, Temperature: snapshot.temperature,
		MaxTokens: snapshot.maxTokens, Stream: true,
		ReasoningLevel: snapshot.binding.Profile.Reasoning, ReasoningDialect: snapshot.binding.Profile.ReasoningDialect,
	}, streamWriter)
	if err != nil {
		// An interrupted turn (Ctrl+C / force-send / deadline) must not lose
		// the user's message or the answer already streamed: adopt both and
		// persist, then hand the partial back instead of the error. Only a
		// still-current turn may persist (stale-turn fence). Non-interrupted
		// errors keep today's drop-everything behavior.
		if partial, ok, persistErr := s.adoptInterruptedPlainTurn(ctx, err, snapshot, prepared, persistedText, captured.String()); ok {
			return partial, persistErr
		}
		return "", err
	}
	if !s.plainTurnCurrent(snapshot.token, snapshot.myTurn) {
		return reply, nil
	}
	s.mu.Lock()
	replaceNewestUserText(prepared, snapshot.messages[len(snapshot.messages)-1].Content, persistedText)
	s.Messages = prepared
	if strings.TrimSpace(reply) != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: reply, CreatedAt: time.Now()})
	}
	s.mu.Unlock()
	if err := s.persistPlainLegacyTurn(snapshot.token); err != nil {
		return reply, err
	}
	return reply, nil
}

// sendPlainContext streams a plain-chat turn through the context manager.
// Phase 2 summary injection: the request carries the validated summary of the
// omitted segment in an EPHEMERAL slice; `prepared` stays structural so the
// durable commit (candidate, checkpoint ActiveContext, compaction event) never
// contains summary content (INV-AG-32 omission stays).
func (s *Session) sendPlainContext(ctx context.Context, persistedText string, w io.Writer, snapshot plainTurnSnapshot) (string, error) {
	input := prepareInputForContext(snapshot.messages, snapshot.budget, snapshot.maxTokens, snapshot.binding, snapshot.context.principal, snapshot.context.policy, snapshot.context.worktree)
	input.Revision = snapshot.context.revision
	if snapshot.tools != nil {
		input.Tools = snapshot.tools.OpenAITools()
	}
	preparation, err := snapshot.context.manager.Prepare(ctx, input)
	if err != nil {
		return "", err
	}
	prepared := preparation.Messages
	requestMessages := prepared
	if snapshot.context.summarizer != nil {
		requestMessages = injectPlainSummary(ctx, snapshot, preparation, prepared)
	}
	// The tee captures the already-streamed bytes: on an interrupted turn
	// ChatStream returns "" as the reply, so the writer is the only record of
	// the partial answer the user already read on screen. A nil caller writer
	// (tests, headless callers) keeps the capture-only surface.
	var captured strings.Builder
	streamWriter := s.plainTurnStreamWriter(w, &captured, fmt.Sprintf("turn:%d", snapshot.myTurn))
	reply, err := snapshot.binding.Completer.ChatStream(ctx, provider.Request{
		Model: snapshot.binding.Model, Messages: requestMessages, Temperature: snapshot.temperature,
		MaxTokens: snapshot.maxTokens, Stream: true,
		ReasoningLevel: snapshot.binding.Profile.Reasoning, ReasoningDialect: snapshot.binding.Profile.ReasoningDialect,
	}, streamWriter)
	if err != nil {
		// An interrupted turn (Ctrl+C / force-send / deadline) must not lose
		// the user's message or the answer already streamed: the interrupted
		// branch commits the partial turn durably with OutcomeCancelled; all
		// other errors keep today's discard-and-drop behavior.
		return s.commitInterruptedPlainContext(ctx, err, snapshot, prepared, persistedText, captured.String(), preparation)
	}
	return s.commitPlainContextTurn(ctx, reply, snapshot, prepared, persistedText, preparation)
}
