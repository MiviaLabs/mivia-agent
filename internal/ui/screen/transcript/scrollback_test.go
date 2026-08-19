package transcript

import (
	"bytes"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// TestBracketKeyHandsScrollbackToTerminal is the transcript-mode test
// for rule 6.3's primary mitigation. `[` must enter handover mode - the
// alternate screen is released, so the terminal can actually show what
// was written - and must return the Exec Cmd that writes the dump.
func TestBracketKeyHandsScrollbackToTerminal(t *testing.T) {
	s := sizedPager(t, 80, 24)
	if s.ViewFlags().AltScreen != true {
		t.Fatal("precondition: the pager holds the alternate screen")
	}

	next, cmd := s.Update(tea.KeyPressMsg{Code: '['})
	pager := next.(Screen)
	if cmd == nil {
		t.Fatal("`[` returned no Cmd; the scrollback write would never run")
	}
	if got := pager.ViewFlags().AltScreen; got {
		t.Error("`[` must release the alternate screen (handover mode); ViewFlags still reports it held")
	}
	if !strings.Contains(pager.View(), "scrollback") {
		t.Errorf("handover view must say what happened, got:\n%s", pager.View())
	}

	// The Cmd must execute the writer, and the writer must carry the
	// whole conversation - tool output expanded - to the injected
	// terminal. Running the writer here proves the content; the teatest
	// integration proves the alt-screen ordering on a real pty.
	var buf bytes.Buffer
	writer := &scrollbackWriter{text: pager.conv.Dump()}
	writer.SetStdout(&buf)
	if err := writer.Run(); err != nil {
		t.Fatalf("scrollback write failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"first zebra question", "tool line", "second zebra note"} {
		if !strings.Contains(out, want) {
			t.Errorf("scrollback output lacks %q; the whole conversation must be written", want)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("scrollback output must end with a newline so the last row reaches scrollback")
	}
}

// TestAnyKeyReturnsFromHandover: the handover lasts until a key.
func TestAnyKeyReturnsFromHandover(t *testing.T) {
	s := sizedPager(t, 80, 24)
	next, _ := s.Update(tea.KeyPressMsg{Code: '['})
	s = next.(Screen)

	next, _ = s.Update(key("x"))
	s = next.(Screen)
	if s.mode != modePager {
		t.Error("a key press must return to the pager")
	}
	if s.ViewFlags().AltScreen != true {
		t.Error("returning to the pager must re-enter the alternate screen")
	}
}

// TestHandoverFailureReturnsToPager: a failed release must not leave the
// user on a surface that silently shows nothing.
func TestHandoverFailureReturnsToPager(t *testing.T) {
	s := sizedPager(t, 80, 24)
	next, _ := s.Update(tea.KeyPressMsg{Code: '['})
	s = next.(Screen)

	next, _ = s.Update(handedOverMsg{err: errTestRelease})
	s = next.(Screen)
	if s.mode != modePager {
		t.Error("a failed handover must return to the pager")
	}
	if !strings.Contains(s.View(), "could not write") {
		t.Errorf("a failed handover must say why, got:\n%s", s.View())
	}
}

var errTestRelease = errString("release failed")

type errString string

func (e errString) Error() string { return string(e) }

// TestScrollbackWriterWithoutOutput: a writer that never received the
// terminal reports it instead of writing nowhere.
func TestScrollbackWriterWithoutOutput(t *testing.T) {
	w := &scrollbackWriter{text: "x"}
	if err := w.Run(); err == nil || !strings.Contains(err.Error(), "no output") {
		t.Errorf("Run without SetStdout = %v, want an error", err)
	}
}

// TestEditorCommandCreatesPrivateFileWithDump pins the `v` contract: the
// temp file is private (0600) and holds the whole conversation, and the
// editor command is $VISUAL before $EDITOR, with arguments preserved.
func TestEditorCommandCreatesPrivateFileWithDump(t *testing.T) {
	dir := t.TempDir()
	dump := "the whole conversation\nwith every tool output\n"

	getenv := func(k string) string {
		switch k {
		case "VISUAL":
			return "/usr/bin/fake-editor -w"
		case "EDITOR":
			return "/usr/bin/other-editor"
		}
		return ""
	}
	cmd, path, err := editorCommand(getenv, dump, dir)
	if err != nil {
		t.Fatalf("editorCommand: %v", err)
	}
	wantArgs := []string{"/usr/bin/fake-editor", "-w", path}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("cmd.Args = %v, want %v", cmd.Args, wantArgs)
	}
	for i := range wantArgs {
		if cmd.Args[i] != wantArgs[i] {
			t.Errorf("cmd.Args[%d] = %q, want %q", i, cmd.Args[i], wantArgs[i])
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("transcript file mode is %o, want 0600 (never world-readable)", got)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != dump {
		t.Errorf("file holds %q, want the dump verbatim", got)
	}
}

// TestEditorCommandPrefersVISUALThenEDITOR covers the fallback order
// and the no-editor refusal.
func TestEditorCommandPrefersVISUALThenEDITOR(t *testing.T) {
	dir := t.TempDir()

	cmd, _, err := editorCommand(func(string) string { return "" }, "x", dir)
	if err == nil || !strings.Contains(err.Error(), "no editor") {
		t.Errorf("no editor: got %v, want the no-editor error", err)
	}
	if cmd != nil {
		t.Error("no editor must build no command")
	}

	cmd, _, err = editorCommand(func(k string) string {
		if k == "EDITOR" {
			return "vi"
		}
		return ""
	}, "x", dir)
	if err != nil {
		t.Fatalf("EDITOR fallback: %v", err)
	}
	if cmd.Args[0] != "vi" {
		t.Errorf("EDITOR fallback ran %q, want vi", cmd.Args[0])
	}
}

// TestOpenEditorWithoutEditorSaysSo: `v` with no editor configured is a
// visible refusal, not a silent no-op.
func TestOpenEditorWithoutEditorSaysSo(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	s := sizedPager(t, 80, 24)

	pager, cmd := s.openEditor()
	if cmd != nil {
		t.Error("no editor must not start an exec")
	}
	if !strings.Contains(pager.View(), "no editor") {
		t.Errorf("the status line must say no editor is set, got:\n%s", pager.View())
	}
}

// TestEditorDoneNotice covers both editorDoneMsg outcomes.
func TestEditorDoneNotice(t *testing.T) {
	s := sizedPager(t, 80, 24)

	next, _ := s.Update(editorDoneMsg{path: "/tmp/x.md"})
	if v := next.(Screen).View(); !strings.Contains(v, "/tmp/x.md") {
		t.Errorf("success notice must name the file, got:\n%s", v)
	}

	next, _ = s.Update(editorDoneMsg{err: errString("exit 1")})
	if v := next.(Screen).View(); !strings.Contains(v, "exit 1") {
		t.Errorf("failure notice must name the error, got:\n%s", v)
	}
}

// TestOpenEditorReturnsExecCmd: with an editor set, `v` returns a Cmd
// (the tea.ExecProcess) rather than acting inline.
func TestOpenEditorReturnsExecCmd(t *testing.T) {
	t.Setenv("VISUAL", "true")
	t.Setenv("EDITOR", "")
	s := sizedPager(t, 80, 24)
	_, cmd := s.openEditor()
	if cmd == nil {
		t.Fatal("`v` returned no Cmd with an editor configured")
	}
}

// TestHandoverViewIsOneLine: the inline handover frame must be a single
// row - it is drawn below the native scrollback the user is reading.
func TestHandoverViewIsOneLine(t *testing.T) {
	s := sizedPager(t, 80, 24)
	next, _ := s.Update(tea.KeyPressMsg{Code: '['})
	v := next.(Screen).View()
	if n := strings.Count(v, "\n") + 1; n != 1 {
		t.Errorf("handover view is %d rows, want 1", n)
	}
}

// TestKeyEscapesNoticeAfterNextKey: the notice line clears when the
// next pager key arrives.
func TestKeyEscapesNoticeAfterNextKey(t *testing.T) {
	s := sizedPager(t, 80, 24)
	next, _ := s.Update(editorDoneMsg{path: "/tmp/x.md"})
	s = next.(Screen)
	next, _ = s.Update(key("j"))
	if next.(Screen).notice != "" {
		t.Error("a pager key must clear the notice line")
	}
}

// compile-time pin: the handover path types stay inside the app.Screen
// contract.
var _ app.Screen = Screen{}

var _ = theme.TierTrueColor // keep the theme import for fixtures
