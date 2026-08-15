package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// seedLiveCompactableSession builds a LIVE session (same construction rules as
// seedCompactableCatalogSession: real committed turns, no SwitchBinding) whose
// context state is compactable, and returns the session handle itself - the
// /compact slash path operates on the live session, not a catalog reload.
func seedLiveCompactableSession(t *testing.T, ws string) (*chat.Session, func()) {
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
	for i := 0; i < 12; i++ {
		if _, err := sess.SendUser(context.Background(), strings.Repeat("old question ", 20), nil); err != nil {
			t.Fatalf("SendUser(%d): %v", i, err)
		}
	}
	return sess, func() { store.Close() }
}

// TestSlashCompactJSONEmitsTypedEvents pins the --json line-mode /compact
// contract: a frontend that sends "/compact" over stdin gets the SAME typed
// "compaction" NDJSON event a turn's automatic compaction emits (built by the
// session's own post-commit emission path, not a second field mapping), plus a
// context_usage refresh so its context indicator updates in the same read.
func TestSlashCompactJSONEmitsTypedEvents(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	var buf bytes.Buffer
	activeJSONSlashSink = &jsonSlashSink{w: &buf}
	defer func() { activeJSONSlashSink = nil }()

	handled, exit, err := handleSlashCompact("/compact", sess, nil)
	if err != nil {
		t.Fatalf("handleSlashCompact: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handled/exit = %v/%v, want true/false", handled, exit)
	}

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (compaction + context_usage): %q", len(lines), buf.String())
	}
	var compacted ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &compacted); err != nil {
		t.Fatalf("decode compaction: %v\nraw: %s", err, lines[0])
	}
	if compacted.Type != "compaction" {
		t.Fatalf("first line type = %q, want compaction: %s", compacted.Type, lines[0])
	}
	if compacted.Compaction == nil {
		t.Fatalf("compaction payload missing: %s", lines[0])
	}
	if compacted.Compaction.Trigger != "threshold" {
		t.Fatalf("trigger = %q, want threshold", compacted.Compaction.Trigger)
	}
	if compacted.Compaction.BeforeTokens <= compacted.Compaction.AfterTokens {
		t.Fatalf("before/after = %d/%d, want before > after", compacted.Compaction.BeforeTokens, compacted.Compaction.AfterTokens)
	}
	if compacted.Message == "" {
		t.Fatalf("compaction message empty: %s", lines[0])
	}
	var usage ndjsonEvent
	if err := json.Unmarshal([]byte(lines[1]), &usage); err != nil {
		t.Fatalf("decode context_usage: %v\nraw: %s", err, lines[1])
	}
	if usage.Type != "context_usage" || usage.ContextUsage == nil {
		t.Fatalf("second line = %s, want context_usage with payload", lines[1])
	}
}

// TestHandleSlashCompactParsesFocusFromTheRawLine pins the /compact [focus
// instructions] slash-parsing contract: text after "/compact" must not break
// the command (no summarizer is wired in this fixture, so focus has no
// visible effect here beyond not erroring - the prompt-threading effect is
// covered by TestContextSummaryIntegrationManualCompactThreadsFocus), and
// bare "/compact" must keep working exactly as before (empty focus).
func TestHandleSlashCompactParsesFocusFromTheRawLine(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()

	handled, exit, err := handleSlashCompact("/compact keep the auth discussion", sess, nil)
	if err != nil {
		t.Fatalf("handleSlashCompact with focus text: %v", err)
	}
	if !handled || exit {
		t.Fatalf("handled/exit = %v/%v, want true/false", handled, exit)
	}
}

// TestSlashCompactJSONFailureEmitsSlashError pins the failure leg: a compact
// that cannot run reports "slash_error" (the authoritative failure signal on
// the json wire), never stderr prose the frontend cannot see.
func TestSlashCompactJSONFailureEmitsSlashError(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()
	// A second compact immediately after the first has nothing left to drop,
	// so the same live session provides the failure leg without a second seed.
	var first bytes.Buffer
	activeJSONSlashSink = &jsonSlashSink{w: &first}
	_, _, _ = handleSlashCompact("/compact", sess, nil)

	var buf bytes.Buffer
	activeJSONSlashSink = &jsonSlashSink{w: &buf}
	defer func() { activeJSONSlashSink = nil }()
	handled, _, err := handleSlashCompact("/compact", sess, nil)
	if err != nil {
		t.Fatalf("handleSlashCompact failure leg returned err: %v", err)
	}
	if !handled {
		t.Fatal("failure leg must still mark the line handled")
	}
	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 slash_error: %q", len(lines), buf.String())
	}
	var ev ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Type != "slash_error" || !strings.Contains(ev.Message, "compaction failed") {
		t.Fatalf("event = %+v, want slash_error naming the failure", ev)
	}
}
