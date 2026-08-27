package clichat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// handleSlashCompact has three legs and only the --json one was covered: every
// other test calls it with term=nil AND an active JSON sink. The terminal leg
// (term != nil, writes into the pane) and the plain line-mode leg (term == nil,
// no sink, writes to stderr) were never executed, and neither was
// reportCompactFailure. These tests drive all four remaining paths.

// newEmptyContextSession builds a context-bound session with no committed
// history, so a compact fails with "nothing to compact" - the failure leg
// without a second seed.
func newEmptyContextSession(t *testing.T, ws string) *chat.Session {
	t.Helper()
	root, err := chatWorkspaceRoot(ws)
	if err != nil {
		t.Fatalf("chatWorkspaceRoot: %v", err)
	}
	res, err := config.Load(config.LoadOptions{WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	sess := chat.NewSession(res, nullCompleter{})
	store, err := setupChatSessionContext(sess, root, chatInvocation{workspacePath: root}, res)
	if err != nil {
		t.Fatalf("setupChatSessionContext: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return sess
}

// TestSlashCompactTerminalLegWritesToThePane pins the interactive terminal
// leg: with a Terminal attached the result goes into the pane, never to
// stderr, which a raw-mode pane would corrupt.
func TestSlashCompactTerminalLegWritesToThePane(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	var pane bytes.Buffer
	stderr := captureStderr(t)
	handled, exit, err := handleSlashCompact("/compact", sess, nil, &Terminal{out: &pane})
	captured := stderr()
	if err != nil {
		t.Fatalf("handleSlashCompact: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handled = %v, exit = %v, want true/false", handled, exit)
	}
	if !strings.Contains(pane.String(), "context compacted") {
		t.Fatalf("pane = %q, want the compaction result", pane.String())
	}
	if strings.Contains(captured, "context compacted") {
		t.Fatalf("terminal leg also wrote to stderr: %q", captured)
	}
}

// TestSlashCompactTerminalLegReportsFailureInThePane pins
// reportCompactFailure's terminal branch.
func TestSlashCompactTerminalLegReportsFailureInThePane(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess := newEmptyContextSession(t, ws)

	var pane bytes.Buffer
	handled, exit, err := handleSlashCompact("/compact", sess, nil, &Terminal{out: &pane})
	if err != nil {
		t.Fatalf("handleSlashCompact returned err instead of reporting: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handled = %v, exit = %v, want true/false", handled, exit)
	}
	if !strings.Contains(pane.String(), "context compaction failed") {
		t.Fatalf("pane = %q, want the failure report", pane.String())
	}
}

// TestSlashCompactPlainLineModeLegWritesToStderr pins the plain (non-json)
// line-mode leg: stdout carries the raw assistant text for a piping caller,
// so the compaction report belongs on stderr.
func TestSlashCompactPlainLineModeLegWritesToStderr(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	previousSink := activeJSONSlashSink
	activeJSONSlashSink = nil
	defer func() { activeJSONSlashSink = previousSink }()

	stderr := captureStderr(t)
	handled, exit, err := handleSlashCompact("/compact", sess, nil, nil)
	captured := stderr()
	if err != nil {
		t.Fatalf("handleSlashCompact: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handled = %v, exit = %v, want true/false", handled, exit)
	}
	if !strings.Contains(captured, "context compacted") {
		t.Fatalf("stderr = %q, want the compaction result", captured)
	}
}

// TestSlashCompactPlainLineModeLegReportsFailureOnStderr pins
// reportCompactFailure's stderr branch.
func TestSlashCompactPlainLineModeLegReportsFailureOnStderr(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess := newEmptyContextSession(t, ws)

	previousSink := activeJSONSlashSink
	activeJSONSlashSink = nil
	defer func() { activeJSONSlashSink = previousSink }()

	stderr := captureStderr(t)
	handled, exit, err := handleSlashCompact("/compact", sess, nil, nil)
	captured := stderr()
	if err != nil {
		t.Fatalf("handleSlashCompact returned err instead of reporting: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handled = %v, exit = %v, want true/false", handled, exit)
	}
	if !strings.Contains(captured, "context compaction failed") {
		t.Fatalf("stderr = %q, want the failure report", captured)
	}
}

// TestSlashCompactTerminalLegIgnoresAnActiveJSONSink pins the leg selection
// itself: the --json branch is chosen on term == nil, so an interactive
// session must keep using the pane even if a sink is somehow attached.
// Routing a TUI compact into an NDJSON sink would print wire lines into the
// user's pane.
func TestSlashCompactTerminalLegIgnoresAnActiveJSONSink(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	var sink bytes.Buffer
	previousSink := activeJSONSlashSink
	activeJSONSlashSink = &JSONSlashSink{w: &sink}
	defer func() { activeJSONSlashSink = previousSink }()

	var pane bytes.Buffer
	if _, _, err := handleSlashCompact("/compact", sess, nil, &Terminal{out: &pane}); err != nil {
		t.Fatalf("handleSlashCompact: %v", err)
	}
	if !strings.Contains(pane.String(), "context compacted") {
		t.Fatalf("pane = %q, want the compaction result", pane.String())
	}
	if sink.Len() != 0 {
		t.Fatalf("terminal leg wrote %q to the json sink", sink.String())
	}
}

// TestSlashCompactFocusReachesEveryLeg pins focus parsing outside the --json
// leg: "/compact <focus>" must compact with the bias, not treat the trailing
// text as an unknown command.
func TestSlashCompactFocusReachesEveryLeg(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	var pane bytes.Buffer
	handled, _, err := handleSlashCompact("/compact keep the auth discussion", sess, nil, &Terminal{out: &pane})
	if err != nil {
		t.Fatalf("handleSlashCompact with focus text: %v", err)
	}
	if !handled {
		t.Fatal("/compact with focus text was not handled")
	}
	if !strings.Contains(pane.String(), "context compacted") {
		t.Fatalf("pane = %q, want the compaction result", pane.String())
	}
	// The focus is a summarizer bias, never conversation content: it must not
	// be appended to the history the next request carries.
	for _, message := range sess.Messages {
		if strings.Contains(message.Content, "keep the auth discussion") {
			t.Fatal("focus text leaked into the conversation history")
		}
	}
}
