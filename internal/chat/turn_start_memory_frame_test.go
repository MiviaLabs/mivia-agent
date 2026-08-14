package chat

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// memoryWidener stands in for the host's rebuild with a memory promotion in
// flight: it builds the candidate registry (like replayWidener) and stamps a
// MemoryBlock on the publication, so TryPublishAgentSurface places the
// core-memory frame through setMemoryMessageLocked.
type memoryWidener struct {
	mu          sync.Mutex
	calls       int
	sess        *Session
	memoryBlock string
}

func (w *memoryWidener) fn(admitted []string, req AgentSurfacePublication) (bool, error) {
	w.mu.Lock()
	w.calls++
	sess := w.sess
	block := w.memoryBlock
	w.mu.Unlock()
	registry := tools.NewRegistry()
	registry.Register(fixedBodyTool{name: "core_tool"})
	for _, name := range admitted {
		registry.Register(fixedBodyTool{name: name})
	}
	req.Registry = registry
	req.MemoryBlock = block
	return sess.TryPublishAgentSurface(req), nil
}

func (w *memoryWidener) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// memoryFrames returns the content of every session message carrying the
// core-memory sentinel Name.
func memoryFrames(msgs []provider.Message) []string {
	var frames []string
	for _, m := range msgs {
		if m.Name == MemoryContextMessageName {
			frames = append(frames, m.Content)
		}
	}
	return frames
}

// A start-of-turn admission publication that carries a changed MemoryBlock
// places the core-memory frame via setMemoryMessageLocked - but the turn's
// snapshot of s.Messages was cloned BEFORE that publication. Pre-fix, the
// loop ran on the stale snapshot and commitPreparedTurn stomped the freshly
// published frame. surfaceForTurnStart now re-reads the live history after
// the publication, so exactly one frame with the NEW content survives the
// turn (legacy persistence path).
func TestTurnStartMemoryPublicationSurvivesLegacyCommit(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	widener := &memoryWidener{sess: sess, memoryBlock: "remember: the new fact"}
	sess.SetSurfaceWidener(widener.fn)

	// Advance to turn 1 so the stage is owned by a strictly earlier turn:
	// the SendUser turn's start is then the earliest safe publication point.
	if _, doneFirst, err := sess.beginAgentTurn("first", nil); err != nil {
		t.Fatal(err)
	} else {
		doneFirst()
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 1); err != nil {
		t.Fatalf("stage turn-1 admission: %v", err)
	}

	reply, err := sess.SendUser(context.Background(), "next", io.Discard)
	if err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if reply != "turn answer" {
		t.Fatalf("reply = %q, want the completer output", reply)
	}
	if widener.count() != 1 {
		t.Fatalf("widener ran %d times, want exactly the one start-of-turn publication", widener.count())
	}

	frames := memoryFrames(sess.MessagesCopy())
	if len(frames) != 1 {
		t.Fatalf("memory frames after the turn = %d, want exactly 1 (contents: %q)", len(frames), frames)
	}
	if !strings.Contains(frames[0], "remember: the new fact") {
		t.Fatalf("frame content = %q, want the NEW memory block", frames[0])
	}
	blob := historyBlob(sess)
	if !strings.Contains(blob, "next") || !strings.Contains(blob, "turn answer") {
		t.Fatalf("turn history was not adopted alongside the frame: %s", blob)
	}
}

// capturingContextPublisher records every committed TurnResult so a test can
// inspect the durable active context.
type capturingContextPublisher struct {
	mu      sync.Mutex
	results []contextmgr.TurnResult
}

func (p *capturingContextPublisher) Commit(_ context.Context, _ contextmgr.Preparation, result contextmgr.TurnResult) error {
	p.mu.Lock()
	p.results = append(p.results, result)
	p.mu.Unlock()
	return nil
}

func (p *capturingContextPublisher) committed() []contextmgr.TurnResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.results)
}

// Durable-context path of the same defect: the committed active context must
// contain the frame the start-of-turn publication placed, because the loop
// (and therefore commitContextTurn) now runs on the post-publication history.
func TestTurnStartMemoryPublicationSurvivesContextCommit(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	pub := &capturingContextPublisher{}
	manager := &contextmgr.ContextManager{
		PreparationManager:  &contextPreparationProbe{},
		CheckpointPublisher: pub,
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	widener := &memoryWidener{sess: sess, memoryBlock: "remember: the new fact"}
	sess.SetSurfaceWidener(widener.fn)

	if _, doneFirst, err := sess.beginAgentTurn("first", nil); err != nil {
		t.Fatal(err)
	} else {
		doneFirst()
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 1); err != nil {
		t.Fatalf("stage turn-1 admission: %v", err)
	}

	if _, err := sess.SendUser(context.Background(), "next", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	results := pub.committed()
	if len(results) != 1 {
		t.Fatalf("committed %d turn results, want 1", len(results))
	}
	frames := memoryFrames(results[0].Active)
	if len(frames) != 1 {
		t.Fatalf("frames in committed active context = %d, want exactly 1 (contents: %q)", len(frames), frames)
	}
	if !strings.Contains(frames[0], "remember: the new fact") {
		t.Fatalf("committed frame content = %q, want the NEW memory block", frames[0])
	}
	// The in-memory head the session adopted must agree: one frame, new
	// content, never a duplicate.
	sessFrames := memoryFrames(sess.MessagesCopy())
	if len(sessFrames) != 1 || !strings.Contains(sessFrames[0], "remember: the new fact") {
		t.Fatalf("session frames after context commit = %q, want exactly one NEW frame", sessFrames)
	}
}

// A stale pre-publication frame in the snapshot must never combine with the
// published one: even when the session already carried an OLD frame before the
// turn, exactly one frame - the new one - survives.
func TestTurnStartMemoryPublicationNeverDuplicatesFrame(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	sess.mu.Lock()
	setMemoryMessageLocked(sess, "the old fact")
	sess.mu.Unlock()
	widener := &memoryWidener{sess: sess, memoryBlock: "the new fact"}
	sess.SetSurfaceWidener(widener.fn)

	if _, doneFirst, err := sess.beginAgentTurn("first", nil); err != nil {
		t.Fatal(err)
	} else {
		doneFirst()
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 1); err != nil {
		t.Fatalf("stage turn-1 admission: %v", err)
	}
	if _, err := sess.SendUser(context.Background(), "next", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	frames := memoryFrames(sess.MessagesCopy())
	if len(frames) != 1 {
		t.Fatalf("memory frames = %d, want exactly 1 (contents: %q)", len(frames), frames)
	}
	if !strings.Contains(frames[0], "the new fact") || strings.Contains(frames[0], "old fact") {
		t.Fatalf("frame content = %q, want only the NEW block", frames[0])
	}
}

// A stage owned by the current, not-yet-run turn stays deferred at turn start
// (D7): the fast path returns the snapshot untouched, no start-of-turn
// publication runs, and the frame appears only through the END-of-turn
// boundary as before.
func TestTurnStartDeferredStageKeepsSnapshotPath(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	widener := &memoryWidener{sess: sess, memoryBlock: "boundary fact"}
	sess.SetSurfaceWidener(widener.fn)

	// Stage owned by the turn the next SendUser will run: turn start defers.
	stageForNextTurn(t, sess, "grep")

	reply, err := sess.SendUser(context.Background(), "question", io.Discard)
	if err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if reply != "turn answer" {
		t.Fatalf("reply = %q", reply)
	}
	// Exactly one publication: the end-of-turn boundary, never turn start.
	if widener.count() != 1 {
		t.Fatalf("widener ran %d times, want exactly the one end-of-turn publication", widener.count())
	}
	blob := historyBlob(sess)
	if !strings.Contains(blob, "question") || !strings.Contains(blob, "turn answer") {
		t.Fatalf("turn history missing: %s", blob)
	}
	// The end-boundary publication rewrites live history directly, so the
	// frame is present exactly once after the turn - unchanged behavior.
	frames := memoryFrames(sess.MessagesCopy())
	if len(frames) != 1 || !strings.Contains(frames[0], "boundary fact") {
		t.Fatalf("frames after end-boundary publication = %q, want exactly one", frames)
	}
}
