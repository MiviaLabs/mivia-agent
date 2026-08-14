package chat

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// decodeActiveContextMessages decodes the durable ActiveContext bytes the same
// way the restore path (loadContextSnapshot) does.
func decodeActiveContextMessages(t *testing.T, body []byte) []provider.Message {
	t.Helper()
	if len(body) == 0 {
		return nil
	}
	var messages []provider.Message
	if err := contextstate.UnmarshalCanonical(body, &messages); err != nil {
		t.Fatalf("decode active context: %v", err)
	}
	return messages
}

// TestCompactionKeepsMemoryFrameInCommittedAndRestoredContext pins the BUG 3
// chain end to end on the durable-context agent path. A session-owned
// core-memory frame at index 1 is session surface, not conversation history:
// a turn that triggers structural compaction must keep the frame in the
// committed Active context (the durable checkpoint), in the in-memory session
// after commit, and in the history a restore from the durable checkpoint
// adopts. Pre-fix the planner dropped the frame once the conversation grew,
// commitContextTurn adopted the frame-less history unconditionally, and no
// later turn re-seeded it.
func TestCompactionKeepsMemoryFrameInCommittedAndRestoredContext(t *testing.T) {
	store, _ := openSharedContextStore(t)
	const memoryBlock = "remember: the promoted fact"
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	sess.mu.Lock()
	setMemoryMessageLocked(sess, memoryBlock)
	sess.mu.Unlock()
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}

	// A compaction must actually run for the test to mean anything: the
	// frame survives a NON-compacting turn by full-history clone alone.
	var compactions []agent.Event
	sess.OnAgentEvent = func(ev agent.Event) {
		if ev.Kind == agent.EventCompaction {
			compactions = append(compactions, ev)
		}
	}

	if _, err := sess.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(compactions) != 0 {
		t.Fatalf("compaction events on turn 1 = %d, want 0", len(compactions))
	}

	forceCompactionBudget(t, sess, "follow up")
	if _, err := sess.SendUser(context.Background(), "follow up", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(compactions) != 1 {
		t.Fatalf("compaction events on turn 2 = %d, want exactly 1", len(compactions))
	}

	// Durable checkpoint: the committed Active context carries the frame.
	snapshot, err := store.Load(context.Background(), principal, sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	active := decodeActiveContextMessages(t, snapshot.Active.ActiveContext)
	frames := memoryFrames(active)
	if len(frames) != 1 {
		t.Fatalf("durable active context frames = %d, want exactly 1 (contents: %q)", len(frames), frames)
	}
	if !strings.Contains(frames[0], memoryBlock) {
		t.Fatalf("durable frame content = %q, want the memory block", frames[0])
	}

	// In-memory session after commit: exactly one frame with the block.
	sessFrames := memoryFrames(sess.MessagesCopy())
	if len(sessFrames) != 1 || !strings.Contains(sessFrames[0], memoryBlock) {
		t.Fatalf("session frames after compaction = %q, want exactly one frame with the block", sessFrames)
	}

	// Restore from the durable checkpoint: the restore path adopts the frame.
	if err := sess.loadContextSnapshot("memory-frame-restore"); err != nil {
		t.Fatalf("loadContextSnapshot: %v", err)
	}
	restored := memoryFrames(sess.MessagesCopy())
	if len(restored) != 1 || !strings.Contains(restored[0], memoryBlock) {
		t.Fatalf("restored frames = %q, want exactly one frame with the block", restored)
	}
}
