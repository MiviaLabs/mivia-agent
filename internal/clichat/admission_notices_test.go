package clichat

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// noteSession returns a session holding one queued admission note, standing in
// for a resume that dropped a stale set or a stage that could not publish.
// The stale admission record is written directly to the durable catalog
// (store.SaveSessionAdmission), bypassing sess.Save's own admission persist,
// so it lands under a DIFFERENT digest than the session's current binding -
// exactly what a resume against an older admission record looks like.
func noteSession(t *testing.T) *chat.Session {
	t.Helper()
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess := wireTestContextSession(t, store, &config.Resolved{ProviderName: "p", Model: "m"})
	sess.SetAdmissionBinding("reader", "digest-1")
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	if err := sess.Save("snap"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionAdmission(context.Background(), sess.ContextPrincipal(), "snap", contextstate.SessionAdmission{
		Agent: "reader", Digest: "a-stale-digest", Names: []string{"grep"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Load("snap"); err != nil {
		t.Fatal(err)
	}
	return sess
}

// TestSlashLoadPrintsAdmissionNotes: the classic REPL must say when a resumed
// session came back with fewer tools than its transcript shows being used.
func TestSlashLoadPrintsAdmissionNotes(t *testing.T) {
	sess := noteSession(t)
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	writeModelRestoreNotice(term, sess)
	if !strings.Contains(buf.String(), "grep") {
		t.Fatalf("output = %q, want the dropped tool named", buf.String())
	}
	if len(sess.TakeAdmissionNotes()) != 0 {
		t.Fatal("printing a note must drain it, not repeat it next time")
	}
}

func TestSlashToolsReportsSchemaMassClassic(t *testing.T) {
	previous := cliagents.ClassicAgentState
	t.Cleanup(func() { cliagents.ClassicAgentState = previous })
	cliagents.ClassicAgentState = &AgentSessionState{LastSchemaMass: schemaMass{Advertised: 4, Tokens: 321, Locked: 2, LockedTokens: 210}}
	res := &config.Resolved{ProviderName: "p", Model: "m"}
	sess := chat.NewSession(res, nullCompleter{})
	sess.Tools = tierRegistry("read_file")
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	if _, _, err := handleSlashInfo("/tools", []string{"/tools"}, sess, res, true, term); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "4 tools advertised") || !strings.Contains(out, "2 locked") {
		t.Fatalf("/tools output = %q, want the schema-mass line", out)
	}
}

// TestReplRestorePrintsAdmissionNotes: the classic REPL's auto-resume is one of
// the load sites, so a dropped admitted set has to be visible there too.
func TestReplRestorePrintsAdmissionNotes(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	res := &config.Resolved{ProviderName: "p", Model: "m"}
	sess := wireTestContextSession(t, store, res)
	sess.SetAdmissionBinding("reader", "digest-1")
	sess.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello"},
	}
	if err := sess.Save(chat.AutoSaveName); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionAdmission(context.Background(), sess.ContextPrincipal(), chat.AutoSaveName, contextstate.SessionAdmission{
		Agent: "reader", Digest: "a-stale-digest", Names: []string{"grep"},
	}); err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	r := newREPLRuntime(sess, res, true, &Terminal{out: buf})
	defer signal.Stop(r.signal)
	if !strings.Contains(buf.String(), "grep") {
		t.Fatalf("restore output = %q, want the dropped tool named", buf.String())
	}
}

// pendingNoteSession returns a session whose widener refuses, so a completed
// turn boundary leaves exactly one queued admission note behind.
func pendingNoteSession(t *testing.T) *chat.Session {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	sess.SetSurfaceWidener(func([]string, chat.AgentSurfacePublication) (bool, error) {
		return false, nil
	})
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatal(err)
	}
	sess.PublishPendingAdmission()
	if len(sess.TakeAdmissionNotes()) == 0 {
		t.Fatal("a refused publication queued no note")
	}
	// Re-queue: the drain above was the assertion, not the fixture.
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatal(err)
	}
	sess.PublishPendingAdmission()
	return sess
}

// TestProcessLineChatPrintsAdmissionNotes: the classic interactive REPL turn is
// a turn-completion surface of its own, and a deferred admission is invisible
// there unless the turn drains the queue like line mode does.
func TestProcessLineChatPrintsAdmissionNotes(t *testing.T) {
	sess := pendingNoteSession(t)
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	renderer := NewChatRenderer(term, sess.CurrentModel())
	input := NewInputBuffer("> ")
	if err := processLineChat("hello", sess, &config.Resolved{ProviderName: "p", Model: "m"},
		false, term, renderer, input, "m"); err != nil {
		t.Fatalf("processLineChat: %v", err)
	}
	if !strings.Contains(stripAnsiOut(buf.String()), "grep") {
		t.Fatalf("output = %q, want the deferred admission named", buf.String())
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want processLineChat to have drained them", notes)
	}
}

// TestOneShotPrintsAdmissionNotesToStderr: `mivia chat -p` exits after the
// turn, so an undrained note is never seen at all. It must land on stderr -
// stdout is the answer channel a caller pipes into something else.
func TestOneShotPrintsAdmissionNotesToStderr(t *testing.T) {
	sess := pendingNoteSession(t)
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	runErr := oneShot(sess, "hello", false, &config.Resolved{ProviderName: "p", Model: "m"}, false)
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	stdout, _ := io.ReadAll(outR)
	stderr, _ := io.ReadAll(errR)
	if runErr != nil {
		t.Fatalf("oneShot: %v", runErr)
	}
	if !strings.Contains(stripAnsiOut(string(stderr)), "grep") {
		t.Fatalf("stderr = %q, want the deferred admission named", stderr)
	}
	if strings.Contains(string(stdout), "grep") {
		t.Fatalf("stdout = %q, want the note kept off the answer channel", stdout)
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want oneShot to have drained them", notes)
	}
}

// TestLineModeTurnPrintsAdmissionNotes covers the plain line-mode turn path,
// where a deferral note is the only signal the user gets.
func TestLineModeTurnPrintsAdmissionNotes(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	// A widener that refuses leaves the stage pending and queues the note the
	// line-mode turn is responsible for printing.
	sess.SetSurfaceWidener(func([]string, chat.AgentSurfacePublication) (bool, error) {
		return false, nil
	})
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatal(err)
	}
	sess.PublishPendingAdmission()
	if len(sess.TakeAdmissionNotes()) == 0 {
		t.Fatal("a refused publication queued no note")
	}
	if _, err := sess.StageToolAdmission([]string{"glob"}, 0); err != nil {
		t.Fatal(err)
	}
	sess.PublishPendingAdmission()
	if err := sendLineMode(sess, "hello", nil, false); err != nil {
		t.Fatalf("line-mode turn: %v", err)
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want the line-mode turn to have drained them", notes)
	}
}

func TestSlashWorkspaceReportsToolsDisabled(t *testing.T) {
	// /workspace reads the surface through the snapshot like /tools does; a
	// tools-off session has no registry to report against.
	res := &config.Resolved{ProviderName: "p", Model: "m"}
	sess := chat.NewSession(res, nullCompleter{})
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	if _, _, err := handleSlashInfo("/workspace", []string{"/workspace"}, sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "tools disabled") {
		t.Fatalf("/workspace = %q, want the tools-disabled notice", buf.String())
	}
}
