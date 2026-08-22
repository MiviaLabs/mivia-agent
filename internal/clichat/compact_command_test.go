package clichat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// seedCompactableCatalogSession seeds a session whose context-catalog
// checkpoint state is real and compactable, returning its system-assigned
// SessionID (the "name" a later `mivia compact --session <id>` must use to
// resolve it - see loadLiveContextSession/liveContextSessionSQL, which
// matches a live context row by its own session_id, not by an arbitrary
// human-chosen catalog name).
//
// Unlike seedCatalogSession's plain sess.Messages + Save (which persists a
// snapshot but never drives a checkpoint commit, leaving nothing for Compact
// to find), this constructs the session directly with a working completer
// so SendUser's own checkpoint commit registers it, and deliberately never
// calls SwitchBinding: SwitchBinding always advances the binding's
// generation, which a later Session.Load, using the same
// provider/model default and taking the CLI's SwitchBinding-free
// adoptLoadedMessages fast path (see chat.reconcileCatalogBinding), would
// never re-publish - leaving the CAS generation mismatched and every
// CompactWithResult call failing with "stale binding: context binding
// changed".
func seedCompactableCatalogSession(t *testing.T, ws string) string {
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
	defer store.Close()
	// Real, committed turns (not an in-memory-only Messages append): Compact
	// reads the durably committed checkpoint, not this seeding session's
	// live memory, so padding must go through SendUser to register.
	for i := 0; i < 12; i++ {
		if _, err := sess.SendUser(context.Background(), strings.Repeat("old question ", 20), nil); err != nil {
			t.Fatalf("SendUser(%d): %v", i, err)
		}
	}
	return sess.SessionID
}

func TestRunCompactJSON(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	name := seedCompactableCatalogSession(t, ws)

	var buf bytes.Buffer
	if err := runCompactWithIO([]string{"--session", name, "--workspace", ws, "--json"}, &buf); err != nil {
		t.Fatalf("compact %s: %v", name, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("decode compact JSON: %v\nraw: %s", err, buf.String())
	}
	for _, field := range []string{"session", "before_tokens", "after_tokens", "elided_messages", "elided_bytes"} {
		if _, ok := raw[field]; !ok {
			t.Fatalf("compact JSON missing %q: %s", field, buf.String())
		}
	}
	if raw["session"] != name {
		t.Fatalf("session = %v, want %s", raw["session"], name)
	}
	before := raw["before_tokens"].(float64)
	after := raw["after_tokens"].(float64)
	if before <= after {
		t.Fatalf("before/after = %v/%v, want before > after", before, after)
	}
}

func TestRunCompactHumanReadable(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	name := seedCompactableCatalogSession(t, ws)

	var buf bytes.Buffer
	if err := runCompactWithIO([]string{"--session", name, "--workspace", ws}, &buf); err != nil {
		t.Fatalf("compact %s: %v", name, err)
	}
	out := buf.String()
	if !strings.Contains(out, fmt.Sprintf("compacted session %q", name)) {
		t.Fatalf("output = %q, want it to mention the session name", out)
	}
	if !strings.Contains(out, "->") {
		t.Fatalf("output = %q, want a before -> after token summary", out)
	}
}

func TestRunCompactMissingSessionFlag(t *testing.T) {
	err := runCompactWithIO([]string{"--workspace", "."}, io.Discard)
	if err == nil {
		t.Fatal("compact with no --session should fail")
	}
	if !strings.Contains(err.Error(), "--session") {
		t.Fatalf("error = %v, want it to mention --session", err)
	}
}

func TestRunCompactUnknownSession(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	err := runCompactWithIO([]string{"--session", "nope", "--workspace", ws}, io.Discard)
	if err == nil {
		t.Fatal("compact on an unknown session should fail")
	}
}

func TestRunCompactNothingToCompact(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	// seedCatalogSession (plain Save, no committed turns) leaves no
	// checkpoint state to compact - Compact must surface that as an error,
	// not succeed silently or panic.
	seedCatalogSession(t, ws, "empty", []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	})
	err := runCompactWithIO([]string{"--session", "empty", "--workspace", ws}, io.Discard)
	if err == nil {
		t.Fatal("compact on a session with no committed context state should fail")
	}
}
