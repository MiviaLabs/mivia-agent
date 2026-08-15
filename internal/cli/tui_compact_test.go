package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newCompactableTuiModel builds a TUI model whose session can really compact:
// a durable context manager, one committed turn, and padded history.
func newCompactableTuiModel(t *testing.T) *tuiModel {
	t.Helper()
	m := newSmokeModel(t)
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "system"}, stubAgentCompleter{})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
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
	if _, err := session.SendUser(context.Background(), "first question", io.Discard); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		session.Messages = append(session.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("old question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("old answer ", 20)},
		)
	}
	m.session = session
	return m
}

// TestTuiCompactShowsIndicatorAndRunsAsync pins the indicator contract: the
// slash handler returns at once, the transcript shows the compaction notice
// immediately, the work rides a drained tea command, and the done message
// clears the busy state and reports the result. The handler must NOT run
// Compact on the update goroutine.
func TestTuiCompactShowsIndicatorAndRunsAsync(t *testing.T) {
	m := newCompactableTuiModel(t)
	if !m.handleSlash("/compact") {
		t.Fatal("/compact was not handled")
	}
	if !m.compacting {
		t.Fatal("/compact did not set the compacting indicator")
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "compacting context") {
		t.Fatalf("transcript tail = %q, want the immediate compaction notice", last)
	}
	cmds := m.takePendingSlashCmds()
	if len(cmds) != 1 {
		t.Fatalf("pending slash commands = %d, want the one compact command", len(cmds))
	}
	msg := cmds[0]()
	done, ok := msg.(compactionDoneMsg)
	if !ok {
		t.Fatalf("compact command produced %T, want compactionDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("compact failed: %v", done.err)
	}
	model, _ := m.handleTUIMessage(done)
	m = model.(*tuiModel)
	if m.compacting {
		t.Fatal("done message left the compacting indicator set")
	}
	result := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(result, "context compacted") {
		t.Fatalf("transcript tail = %q, want the compaction result", result)
	}
}

// TestTuiCompactReportsTypedCompactionNotice pins the TUI's parity with the
// --json leg: a manual /compact must surface the typed compaction record
// (the before/after token delta the session emits through OnAgentEvent), not
// only applyCompactionDone's percentage line. The bridge callback is
// attached per turn inside SendUser*, so a compact run outside a turn had no
// event sink at all and the record reached nobody - the --json leg attaches
// one for exactly this reason (handleSlashCompactJSON).
func TestTuiCompactReportsTypedCompactionNotice(t *testing.T) {
	m := newCompactableTuiModel(t)
	if !m.handleSlash("/compact") {
		t.Fatal("/compact was not handled")
	}
	cmds := m.takePendingSlashCmds()
	if len(cmds) != 1 {
		t.Fatalf("pending slash commands = %d, want the one compact command", len(cmds))
	}
	done, ok := cmds[0]().(compactionDoneMsg)
	if !ok {
		t.Fatal("compact command did not produce a compactionDoneMsg")
	}
	if done.err != nil {
		t.Fatalf("compact failed: %v", done.err)
	}
	if !strings.Contains(done.notice, "->") {
		t.Fatalf("compactionDoneMsg.notice = %q, want the typed before/after token delta", done.notice)
	}
	model, _ := m.handleTUIMessage(done)
	m = model.(*tuiModel)

	var sawNotice bool
	for _, block := range m.blocks {
		if strings.Contains(block.Text, done.notice) {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Fatalf("transcript never showed the typed notice %q", done.notice)
	}
}

// TestTuiCompactWithoutTypedEventStillReports pins the degraded leg: a
// compact that emits no typed record must still report its outcome and must
// not leave an empty notice line in the transcript.
func TestTuiCompactWithoutTypedEventStillReports(t *testing.T) {
	m := newCompactableTuiModel(t)
	m.compacting = true
	before := len(m.blocks)
	model, _ := m.handleTUIMessage(compactionDoneMsg{})
	m = model.(*tuiModel)
	if got := len(m.blocks) - before; got != 1 {
		t.Fatalf("appended %d blocks, want only the outcome line", got)
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "context compacted") {
		t.Fatalf("transcript tail = %q, want the compaction result", last)
	}
}

// TestTuiCompactRefusesWhileWaiting pins the busy guard: /compact during a
// turn refuses in place and stages no work.
func TestTuiCompactRefusesWhileWaiting(t *testing.T) {
	m := newCompactableTuiModel(t)
	m.waiting = true
	if !m.handleSlash("/compact") {
		t.Fatal("/compact was not handled")
	}
	if m.compacting {
		t.Fatal("refused /compact still set the indicator")
	}
	if cmds := m.takePendingSlashCmds(); len(cmds) != 0 {
		t.Fatalf("refused /compact staged %d commands", len(cmds))
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "finish the current turn") {
		t.Fatalf("refusal = %q, want the busy-turn refusal", last)
	}
}

// TestTuiCompactRefusesWhileCompacting pins the double-compact guard.
func TestTuiCompactRefusesWhileCompacting(t *testing.T) {
	m := newCompactableTuiModel(t)
	m.compacting = true
	if !m.handleSlash("/compact") {
		t.Fatal("/compact was not handled")
	}
	cmds := m.takePendingSlashCmds()
	if len(cmds) != 0 {
		t.Fatalf("double /compact staged %d commands", len(cmds))
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "already compacting") {
		t.Fatalf("refusal = %q, want the already-compacting refusal", last)
	}
}

// TestTuiCompactDoneMessageReportsFailure pins the error leg of the done
// message: the busy state clears and the failure text lands in the transcript.
func TestTuiCompactDoneMessageReportsFailure(t *testing.T) {
	m := newCompactableTuiModel(t)
	m.compacting = true
	model, _ := m.handleTUIMessage(compactionDoneMsg{err: context.Canceled})
	m = model.(*tuiModel)
	if m.compacting {
		t.Fatal("failed compact left the indicator set")
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "context compaction failed") {
		t.Fatalf("transcript tail = %q, want the failure report", last)
	}
}

// TestTuiStatusBarBusyWhileCompacting pins the visible status treatment: the
// status bar reads busy while compacting even though no turn is waiting.
func TestTuiStatusBarBusyWhileCompacting(t *testing.T) {
	m := newCompactableTuiModel(t)
	if detail := m.statusDetail(); strings.Contains(detail, "compacting") {
		t.Fatalf("idle detail = %q, must not claim compaction", detail)
	}
	m.compacting = true
	if detail := m.statusDetail(); !strings.Contains(detail, "compacting context") {
		t.Fatalf("compacting detail = %q, want the compaction label", detail)
	}
}
