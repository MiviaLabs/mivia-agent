package clichat

// Tests for --session, which resumes a saved session (by the name/id
// "mivia sessions list" reports) before a chat surface starts. Two things are
// covered:
//
//  1. parseChatInvocation actually captures --session into
//     chatInvocation.session (cheap, no I/O).
//  2. The resume mechanism itself - sess.Load(name) called with the exact
//     same session the desktop app would build via "mivia sessions list" -
//     really does restore prior history before any new turn is appended, and
//     really does fail closed on an unknown name (this repo's convention:
//     session identity is always system-minted via RotateSessionID, never
//     caller-chosen, so --session never silently starts a new session under
//     an unrecognized name - see the chatInvocation.session doc comment in
//     chat_command.go).
//
// Full end-to-end coverage through runConfiguredChatOnce would need a live
// (or fake) provider dial; TestR4ChatOnce* in ollama_r4_hostile_audit_test.go
// already exercises that path's error surfacing. What is new here - the
// --session wiring and the Load-based resume semantics - is tested directly
// against the same chat.Session/newCatalogSession primitives
// runConfiguredChatOnce itself uses, which is where the actual behavior
// lives.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestParseChatInvocationCapturesSessionFlag(t *testing.T) {
	invocation, err := parseChatInvocation([]string{"--session", "resume-me", "-p", "hi"})
	if err != nil {
		t.Fatalf("parseChatInvocation: %v", err)
	}
	if invocation.session != "resume-me" {
		t.Fatalf("invocation.session = %q, want %q", invocation.session, "resume-me")
	}
	if invocation.prompt != "hi" {
		t.Fatalf("invocation.prompt = %q, want %q (unrelated flags must still parse)", invocation.prompt, "hi")
	}
}

func TestParseChatInvocationSessionFlagRequiresValue(t *testing.T) {
	if _, err := parseChatInvocation([]string{"--session"}); err == nil {
		t.Fatal("--session with no value: want an error, got nil")
	}
}

// TestChatSessionResumeRestoresPriorHistory exercises the exact call
// runConfiguredChatOnce makes for --session (sess.Load(invocation.session))
// against a session with one prior turn, and confirms MessagesCopy reflects
// that turn before anything new is appended - the resume contract task 2
// requires.
func TestChatSessionResumeRestoresPriorHistory(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)

	priorTurn := []provider.Message{
		{Role: provider.RoleUser, Content: "what is the capital of france"},
		{Role: provider.RoleAssistant, Content: "Paris"},
	}
	seedCatalogSession(t, ws, "resume-me", priorTurn)

	// A fresh session, built the same way runConfiguredChatOnce builds one
	// for a resumed chat: chat.NewSession + the same context-catalog wiring.
	sess, store, err := newCatalogSession(ws)
	if err != nil {
		t.Fatalf("newCatalogSession: %v", err)
	}
	defer store.Close()

	if err := sess.Load("resume-me"); err != nil {
		t.Fatalf("Load(resume-me): %v", err)
	}
	restored := sess.MessagesCopy()
	if len(restored) != len(priorTurn) {
		t.Fatalf("after Load: got %d messages, want %d: %+v", len(restored), len(priorTurn), restored)
	}
	for i, want := range priorTurn {
		if restored[i].Role != want.Role || restored[i].Content != want.Content {
			t.Fatalf("after Load: message[%d] = %+v, want %+v", i, restored[i], want)
		}
	}

	// Simulate the next turn starting: appending a new user message must
	// extend the restored history, not replace or lose it. sess.SendUser
	// would dispatch to a real completer (none is wired here, by design -
	// this is a read/resume-only session), so the new turn is appended
	// directly, exercising exactly the invariant under test: Load happened
	// first, and did not get clobbered.
	sess.Messages = append(sess.Messages, provider.Message{Role: provider.RoleUser, Content: "and germany?"})
	afterNewTurn := sess.MessagesCopy()
	if len(afterNewTurn) != len(priorTurn)+1 {
		t.Fatalf("after new turn: got %d messages, want %d (prior %d + 1 new)", len(afterNewTurn), len(priorTurn)+1, len(priorTurn))
	}
	for i, want := range priorTurn {
		if afterNewTurn[i].Role != want.Role || afterNewTurn[i].Content != want.Content {
			t.Fatalf("after new turn: prior message[%d] = %+v, want unchanged %+v", i, afterNewTurn[i], want)
		}
	}
	last := afterNewTurn[len(afterNewTurn)-1]
	if last.Role != provider.RoleUser || last.Content != "and germany?" {
		t.Fatalf("after new turn: last message = %+v, want the new user turn", last)
	}
}

// TestChatSessionResumeUnknownNameFailsClosed documents and locks in the
// chosen behavior for --session <new-name>: this codebase never lets a
// caller mint a session's identity (RotateSessionID always generates it), so
// resuming a name that does not exist yet is a clear error, not a silent
// fresh session under that name.
func TestChatSessionResumeUnknownNameFailsClosed(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, store, err := newCatalogSession(ws)
	if err != nil {
		t.Fatalf("newCatalogSession: %v", err)
	}
	defer store.Close()

	if err := sess.Load("never-saved"); err == nil {
		t.Fatal("Load(never-saved): want an error, got nil")
	}
	if len(sess.MessagesCopy()) != 0 {
		t.Fatalf("a failed Load must not leave partial history: got %+v", sess.MessagesCopy())
	}
}

// TestChatOnceSessionFlagUnknownNameFailsBeforeDial runs the real
// runConfiguredChatOnceImpl entrypoint (not just the Load primitive) with
// --session pointed at a name that was never saved, against a closed
// loopback port. The --session error must fire before any provider dial: if
// it instead surfaced a "connection refused" error, that would mean
// sess.Load never ran (or its error got swallowed) and the flow fell through
// to oneShot anyway.
func TestChatOnceSessionFlagUnknownNameFailsBeforeDial(t *testing.T) {
	addr := closedLoopbackPort(t)
	res, ws := writeOllamaChatConfig(t, "http://"+addr+"/v1")

	err := runChatOnceGuardedWithSession(t, res, ws, "never-saved")
	if err == nil {
		t.Fatal("--session never-saved against closed port: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "--session") {
		t.Fatalf("err = %v, want the --session resume error", err)
	}
	if strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v: a provider dial happened before --session was resolved", err)
	}
}

// TestChatOnceSessionFlagResumesKnownSession pre-saves a session under the
// exact catalog storage runConfiguredChatOnce wires up for ws, then runs with
// --session pointed at it against a closed loopback port. Reaching the
// dial-refused error (rather than the --session resume error) proves
// sess.Load succeeded and the flow proceeded past the resume step, exactly
// as it should for a name that exists.
func TestChatOnceSessionFlagResumesKnownSession(t *testing.T) {
	addr := closedLoopbackPort(t)
	res, ws := writeOllamaChatConfig(t, "http://"+addr+"/v1")
	// seedCatalogSession's config.Load (via newCatalogSession) has no
	// --config flag; point it at the same fixture writeOllamaChatConfig just
	// wrote so it resolves the identical provider/model res carries.
	t.Setenv("MIVIA_CONFIG", filepath.Join(ws, ".mivia", "mivia.toml"))

	seedCatalogSession(t, ws, "resume-me", []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
	})

	err := runChatOnceGuardedWithSession(t, res, ws, "resume-me")
	if err == nil {
		t.Fatal("--session resume-me against closed port: want the dial error, got nil")
	}
	if strings.Contains(err.Error(), "--session") {
		t.Fatalf("err = %v: the resume step itself failed instead of proceeding to the dial", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want a connection-refused provider/dial error (resume should have succeeded)", err)
	}
}

// runChatOnceGuardedWithSession is runChatOnceGuarded (see
// ollama_r4_hostile_audit_test.go) with --session added to the invocation.
func runChatOnceGuardedWithSession(t *testing.T, res *config.Resolved, ws, session string) (err error) {
	t.Helper()
	orig, werr := os.Getwd()
	if werr != nil {
		t.Fatalf("getwd before chat: %v", werr)
	}
	defer func() { _ = os.Chdir(orig) }()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runConfiguredChatOnce PANICKED: %v", r)
		}
	}()
	return runConfiguredChatOnceImpl(chatInvocation{prompt: "hi", workspacePath: ws, noTools: true, session: session}, res)
}
