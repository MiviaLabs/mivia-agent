package cli

import (
	"bytes"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
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

func TestSlashToolsReportsSchemaMassClassic(t *testing.T) {
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

// TestTuiToolsDialogReportsSchemaMass: the TUI is the default surface, so its
// /tools overlay owns the documented schema-mass claim.
func TestTuiToolsDialogReportsSchemaMass(t *testing.T) {
	m := newSmokeModel(t)
	m.session.Tools = tierRegistry("read_file")
	m.agentState = &agentSessionState{LastSchemaMass: schemaMass{Advertised: 4, Tokens: 321, Deferred: 2, HeldTokens: 210}}
	if !m.handleTuiInfoSlash("/tools", []string{"/tools"}) {
		t.Fatal("/tools was not handled")
	}
	if m.overlay == nil {
		t.Fatal("/tools opened no overlay")
	}
	out := stripANSI(strings.Join(m.overlay.lines, "\n"))
	if !strings.Contains(out, "4 tools advertised") || !strings.Contains(out, "2 deferred") {
		t.Fatalf("tools overlay = %q, want the schema-mass line", out)
	}
}

// TestTuiToolsDialogWithoutAgentState: a TUI built without agent state must
// still render the tool list rather than panicking on the measurement.
func TestTuiToolsDialogWithoutAgentState(t *testing.T) {
	m := newSmokeModel(t)
	m.session.Tools = tierRegistry("read_file")
	m.agentState = nil
	if !m.handleTuiInfoSlash("/tools", []string{"/tools"}) {
		t.Fatal("/tools was not handled")
	}
	out := stripANSI(strings.Join(m.overlay.lines, "\n"))
	if !strings.Contains(out, "read_file") {
		t.Fatalf("tools overlay = %q, want the tool list", out)
	}
	if strings.Contains(out, "tools advertised") {
		t.Fatalf("tools overlay = %q, want no schema-mass line without a measurement", out)
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

// pendingNoteSession returns a session whose widener refuses, so a completed
// turn boundary leaves exactly one queued admission note behind.
func pendingNoteSession(t *testing.T) *chat.Session {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, stubAgentCompleter{})
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
	if !strings.Contains(stripANSI(buf.String()), "grep") {
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
	if !strings.Contains(stripANSI(string(stderr)), "grep") {
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
	sess := chat.NewSession(res, stubAgentCompleter{})
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	if _, _, err := handleSlashInfo("/workspace", []string{"/workspace"}, sess, res, false, term); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "tools disabled") {
		t.Fatalf("/workspace = %q, want the tools-disabled notice", buf.String())
	}
}

func TestSkillTurnWithResourcesRequiresTools(t *testing.T) {
	// A skill carrying resources needs a live registry to scope; a tools-off
	// session must be refused rather than handed a nil one.
	m := newSmokeModel(t)
	m.session.Tools = nil
	m.session.UseTools = false
	root := t.TempDir()
	skillDir := filepath.Join(root, "probe")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: probe\nuser-invocable: true\n---\nBody.",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"t\"\npath = \"t.md\"\nsummary = \"s\"\n",
		"t.md":           "RESOURCE",
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("probe")
	if !ok || len(def.Resources) == 0 {
		t.Fatalf("resource skill not loaded: ok=%v resources=%d", ok, len(def.Resources))
	}
	if _, _, err := m.prepareSkillTurn(skillSlashSpec{definition: def}); err == nil ||
		!strings.Contains(err.Error(), "require tools") {
		t.Fatalf("error = %v, want the tools-off refusal", err)
	}
}
