package cli

import (
	"bytes"
	"os/signal"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// noteSession returns a session holding one queued admission note, standing in
// for a resume that dropped a stale set or a stage that could not publish.
func noteSession(t *testing.T) *chat.Session {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, stubAgentCompleter{})
	sess.SessionDir = t.TempDir()
	sess.SetAdmissionBinding("reader", "digest-1")
	store, err := chat.NewFileSessionStore(sess.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	if err := sess.Save("snap"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAdmission("snap", contextstate.SessionAdmission{
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

func TestSlashToolsReportsSchemaMass(t *testing.T) {
	previous := classicAgentState
	t.Cleanup(func() { classicAgentState = previous })
	classicAgentState = &agentSessionState{LastSchemaMass: schemaMass{Advertised: 4, Tokens: 321, Deferred: 2, HeldTokens: 210}}
	res := &config.Resolved{ProviderName: "p", Model: "m"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = tierRegistry("read_file")
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	if _, _, err := handleSlashInfo("/tools", []string{"/tools"}, sess, res, true, term); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "4 tools advertised") || !strings.Contains(out, "2 deferred") {
		t.Fatalf("/tools output = %q, want the schema-mass line", out)
	}
}

func TestTuiSurfacesAdmissionNotes(t *testing.T) {
	m := newSmokeModel(t)
	m.session = noteSession(t)
	before := len(m.messages)
	m.appendAdmissionNotes()
	if len(m.messages) == before {
		t.Fatal("the TUI swallowed an admission note")
	}
	joined := strings.Join(m.messages, "\n")
	if !strings.Contains(joined, "grep") {
		t.Fatalf("TUI notes = %q, want the dropped tool named", joined)
	}
	before = len(m.messages)
	m.appendAdmissionNotes()
	if len(m.messages) != before {
		t.Fatal("a drained note was repeated")
	}
}

// TestReplRestorePrintsAdmissionNotes: the classic REPL's auto-resume is one of
// the load sites, so a dropped admitted set has to be visible there too.
func TestReplRestorePrintsAdmissionNotes(t *testing.T) {
	dir := t.TempDir()
	res := &config.Resolved{ProviderName: "p", Model: "m"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.SessionDir = dir
	sess.SetAdmissionBinding("reader", "digest-1")
	sess.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello"},
	}
	if err := sess.Save(chat.AutoSaveName); err != nil {
		t.Fatal(err)
	}
	store, err := chat.NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAdmission(chat.AutoSaveName, contextstate.SessionAdmission{
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

// TestLineModeTurnPrintsAdmissionNotes covers the plain line-mode turn path,
// where a deferral note is the only signal the user gets.
func TestLineModeTurnPrintsAdmissionNotes(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, stubAgentCompleter{})
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
	if err := sendLineMode(sess, "hello", nil); err != nil {
		t.Fatalf("line-mode turn: %v", err)
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want the line-mode turn to have drained them", notes)
	}
}
