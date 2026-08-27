package transcript

import (
	"bytes"
	"io/fs"
	"os"
	"runtime"
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
	cmd, path, err := editorCommand(getenv, os.WriteFile, dump, dir)
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
	// Mode bits are POSIX semantics: Windows Stat reports 0666 (chmod keeps
	// only the read-only attribute); privacy there rides on %TEMP% ACL
	// inheritance. The 0600 contract itself stays pinned on Unix.
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
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

	cmd, _, err := editorCommand(func(string) string { return "" }, os.WriteFile, "x", dir)
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
	}, os.WriteFile, "x", dir)
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

// TestEditorCommandFailsWhenTempAreaUnwritable covers both filesystem
// failure paths: a temp directory that cannot be created in, and a file
// that cannot be written.
func TestEditorCommandFailsWhenTempAreaUnwritable(t *testing.T) {
	getenv := func(k string) string {
		if k == "VISUAL" {
			return "vi"
		}
		return ""
	}

	if _, _, err := editorCommand(getenv, os.WriteFile, "x", "/nonexistent-dir-for-mivia"); err == nil {
		t.Error("expected an error when the temp directory does not exist")
	}

	// CreateTemp succeeds, then WriteFile must fail: the directory is
	// read-only for the write step. Root ignores directory permissions,
	// so skip under root. Windows needs the extra guard: Geteuid() is -1
	// there (so the root skip never fires), and chmod 0500 does not revoke
	// NTFS write access in the first place.
	if os.Geteuid() != 0 && runtime.GOOS != "windows" {
		dir := t.TempDir()
		f, err := os.CreateTemp(dir, "probe-*")
		if err != nil {
			t.Fatal(err)
		}
		probe := f.Name()
		f.Close()
		os.Remove(probe)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		_, _, err = editorCommand(getenv, os.WriteFile, "x", dir)
		if err == nil {
			t.Error("expected an error when the temp directory is read-only for writes")
		}
	}
}

// TestExecCommandNoopSetters: the writer accepts the full ExecCommand
// interface without using stdin or stderr.
func TestExecCommandNoopSetters(t *testing.T) {
	w := &scrollbackWriter{text: "x"}
	w.SetStdin(nil)
	w.SetStderr(nil)
	w.SetStdout(&bytes.Buffer{})
	if err := w.Run(); err != nil {
		t.Errorf("Run with all setters called: %v", err)
	}
}

// TestEditorCommandWriteFailure keeps the temp file from lingering
// when the dump cannot be written.
func TestEditorCommandWriteFailure(t *testing.T) {
	dir := t.TempDir()
	var seen string
	fail := func(path string, _ []byte, _ fs.FileMode) error {
		seen = path
		return errString("disk full")
	}
	cmd, _, err := editorCommand(func(k string) string {
		if k == "VISUAL" {
			return "vi"
		}
		return ""
	}, fail, "the dump", dir)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got %v, want the write error", err)
	}
	if cmd != nil {
		t.Error("a failed write must build no editor command")
	}
	if _, statErr := os.Stat(seen); !os.IsNotExist(statErr) {
		t.Errorf("the temp file %q must be removed after a failed write", seen)
	}
}

// TestExecCallbacksDirectly pins what the screen receives when each
// handover command finishes. The callbacks are named constructors, not
// inline closures, exactly so this is testable.
func TestExecCallbacksDirectly(t *testing.T) {
	if m, ok := handoverDone(nil).(handedOverMsg); !ok || m.err != nil {
		t.Errorf("handoverDone(nil) = %#v, want a clean handedOverMsg", m)
	}
	want := errString("boom")
	if m, ok := handoverDone(want).(handedOverMsg); !ok || m.err != want {
		t.Errorf("handoverDone(err) = %#v, want the error carried through", m)
	}
	cb := editorCallback("/tmp/t.md")
	if m, ok := cb(nil).(editorDoneMsg); !ok || m.path != "/tmp/t.md" || m.err != nil {
		t.Errorf("editorCallback(nil) = %#v, want a clean editorDoneMsg with the path", m)
	}
	if m, ok := cb(want).(editorDoneMsg); !ok || m.err != want {
		t.Errorf("editorCallback(err) = %#v, want the error carried through", m)
	}
}

// TestVKeyOpensTheEditor drives the keymap path, not the method.
func TestVKeyOpensTheEditor(t *testing.T) {
	t.Setenv("VISUAL", "true")
	t.Setenv("EDITOR", "")
	s := sizedPager(t, 80, 24)
	next, cmd := s.Update(tea.KeyPressMsg{Code: 'v'})
	if cmd == nil {
		t.Fatal("v must return the ExecProcess Cmd through the keymap")
	}
	_ = next
}
