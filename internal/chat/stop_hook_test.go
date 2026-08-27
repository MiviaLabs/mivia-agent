package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
)

// installMarkerStopHook arms a Stop hook, at the given workspace, that reads
// its JSON payload from stdin and appends "session_id turn_id" to a marker
// file - so a test can pin exactly which identifiers a fired hook actually
// received. A hook that ran but got the wrong turn/session id would look
// identical to a correctly wired one under any assertion that only checks
// "did it run at all".
//
// This mirrors internal/uiadapter/runner_test.go's
// TestCommandRunner_HooksListsArmedHooks: write <ws>/.mivia/mivia.toml, then
// hooksession.Load(ws) - the only cross-package way to install a real
// *hooksession.Session, since its fields are unexported.
func installMarkerStopHook(t *testing.T) (marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script hook fixture is POSIX-only; see internal/hooksession/stop_test.go for the Windows translation this package does not duplicate")
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	marker = filepath.Join(ws, "marker.txt")
	script := filepath.Join(ws, ".mivia", "stop.sh")
	body := "#!/bin/sh\n" +
		"payload=$(cat)\n" +
		"sid=$(printf '%s' \"$payload\" | sed -n 's/.*\"session_id\":\"\\([^\"]*\\)\".*/\\1/p')\n" +
		"tid=$(printf '%s' \"$payload\" | sed -n 's/.*\"turn_id\":\"\\([^\"]*\\)\".*/\\1/p')\n" +
		"printf '%s %s\\n' \"$sid\" \"$tid\" >> \"" + marker + "\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mivia", "mivia.toml"), []byte(`[[hooks]]
event = "Stop"

  [[hooks.handlers]]
  type = "command"
  argv = ["./stop.sh"]
`), 0o600); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	session, err := hooksession.Load(ws)
	if err != nil {
		t.Fatalf("hooksession.Load: %v", err)
	}
	t.Cleanup(hooksession.SetForTest(session))
	return marker
}

func markerLines(t *testing.T, marker string) []string {
	t.Helper()
	data, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// TestSendUserFiresStopHookOnceForEachRootTurn drives two plain-path turns
// and asserts the Stop hook fired exactly once per turn, each carrying the
// session id and the same "turn:N" identifier PreToolUse/PostToolUse use
// (fmt.Sprintf("turn:%d", ...) in internal/chat/session.go) - not the reply
// text, which is what the first (rejected) Wave 2 plan would have sent.
func TestSendUserFiresStopHookOnceForEachRootTurn(t *testing.T) {
	marker := installMarkerStopHook(t)

	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	sess.SessionID = "sess-xyz"
	// NewSession's own resetSystem (via Clear) already advances turnID once
	// at construction, so the first SendUser's turn is next+1, not 1 - this
	// reads the session's own counter rather than hardcoding that offset, to
	// pin "matches the real per-session counter" without coupling to it.
	sess.mu.RLock()
	first, second := sess.turnID+1, sess.turnID+2
	sess.mu.RUnlock()

	if _, err := sess.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatalf("first SendUser: %v", err)
	}
	if _, err := sess.SendUser(context.Background(), "second", io.Discard); err != nil {
		t.Fatalf("second SendUser: %v", err)
	}

	lines := markerLines(t, marker)
	if len(lines) != 2 {
		t.Fatalf("marker lines = %v, want exactly 2 (one Stop firing per root turn)", lines)
	}
	wantFirst := fmt.Sprintf("sess-xyz turn:%d", first)
	wantSecond := fmt.Sprintf("sess-xyz turn:%d", second)
	if lines[0] != wantFirst {
		t.Fatalf("first line = %q, want %q", lines[0], wantFirst)
	}
	if lines[1] != wantSecond {
		t.Fatalf("second line = %q, want %q", lines[1], wantSecond)
	}
}

// TestSendUserFiresStopHookOnAgentPath pins the agent-loop branch
// (sendAgent) separately from the plain branch above: both funnel through
// sendUserWithTurn, but they wrap done() independently, so a fix applied to
// only one would leave the other silently unfired.
func TestSendUserFiresStopHookOnAgentPath(t *testing.T) {
	marker := installMarkerStopHook(t)

	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	sess.SessionID = "sess-agent"
	sess.mu.RLock()
	want := fmt.Sprintf("sess-agent turn:%d", sess.turnID+1)
	sess.mu.RUnlock()

	if _, err := sess.SendUser(context.Background(), "hi", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}

	lines := markerLines(t, marker)
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("marker lines = %v, want exactly [%q]", lines, want)
	}
}

// TestSendUserFiresStopHookOnCanceledContext pins the required firing
// contract from the Wave 2 plan review: a turn that already began fires
// Stop on EVERY outcome, including a canceled ctx, and the run is recorded
// (not silently skipped) exactly like internal/hooksession's own
// TestStopHookOnACanceledTurnIsRecordedNotSilent. This test pins the same
// contract one layer up, at the sendPlain call site that wraps done().
func TestSendUserFiresStopHookOnCanceledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script hook fixture is POSIX-only")
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mivia", "stop.sh"), []byte("#!/bin/sh\ncat >/dev/null\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mivia", "mivia.toml"), []byte(`[[hooks]]
event = "Stop"

  [[hooks.handlers]]
  type = "command"
  argv = ["./stop.sh"]
`), 0o600); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	hs, err := hooksession.Load(ws)
	if err != nil {
		t.Fatalf("hooksession.Load: %v", err)
	}
	t.Cleanup(hooksession.SetForTest(hs))

	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	sess.SessionID = "sess-cancel"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = sess.SendUser(ctx, "hi", io.Discard)

	if got := hooksession.Current().List(); !strings.Contains(got, "warning:") {
		t.Fatalf("a Stop hook on a canceled root turn must be recorded as a run warning, /hooks = %q", got)
	}
}

// TestSendUserFiresNoStopHookWhenTheTurnNeverBegan pins the other half of
// the firing contract: begin*Turn failing (session switching or loading)
// must not fire Stop at all - there is nothing to report as "done" for a
// turn that never started running.
func TestSendUserFiresNoStopHookWhenTheTurnNeverBegan(t *testing.T) {
	marker := installMarkerStopHook(t)

	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	sess.SessionID = "sess-never"
	sess.mu.Lock()
	sess.switching = true
	sess.mu.Unlock()

	if _, err := sess.SendUser(context.Background(), "hi", io.Discard); err == nil {
		t.Fatal("SendUser during a surface switch must fail")
	}

	if lines := markerLines(t, marker); len(lines) != 0 {
		t.Fatalf("marker lines = %v, want none: a turn that never began must not fire Stop", lines)
	}
}

// TestFireRootTurnEndHookSurfacesOutputAsAnEventHook pins that a fired Stop
// hook's output reaches the same OnAgentEvent channel Pre/PostToolUse hook
// runs use (internal/agent/hook_events.go's emitHookRuns), with the same
// EventHook shape a renderer already knows how to draw
// (internal/uiadapter/event_kind.go's translateHook), rather than a bespoke
// notice string a renderer would need a second code path to show.
func TestFireRootTurnEndHookSurfacesOutputAsAnEventHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script hook fixture is POSIX-only")
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mivia", "stop.sh"), []byte("#!/bin/sh\ncat >/dev/null\nprintf 'turn cost noted'\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mivia", "mivia.toml"), []byte(`[[hooks]]
event = "Stop"

  [[hooks.handlers]]
  type = "command"
  argv = ["./stop.sh"]
`), 0o600); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	hs, err := hooksession.Load(ws)
	if err != nil {
		t.Fatalf("hooksession.Load: %v", err)
	}
	t.Cleanup(hooksession.SetForTest(hs))

	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	sess.SessionID = "sess-event"
	var got []agent.Event
	sess.OnAgentEvent = func(e agent.Event) { got = append(got, e) }

	if _, err := sess.SendUser(context.Background(), "hi", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}

	var hookEvents []agent.Event
	for _, e := range got {
		if e.Kind == agent.EventHook {
			hookEvents = append(hookEvents, e)
		}
	}
	if len(hookEvents) != 1 {
		t.Fatalf("got %d EventHook events, want 1: %+v", len(hookEvents), got)
	}
	if hookEvents[0].Name != "Stop" {
		t.Fatalf("Name = %q, want %q", hookEvents[0].Name, "Stop")
	}
	if !strings.Contains(hookEvents[0].Output, "turn cost noted") {
		t.Fatalf("Output = %q, want it to contain the hook's stdout", hookEvents[0].Output)
	}
}
