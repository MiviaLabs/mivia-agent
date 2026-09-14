package transcript

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// scrollbackWaitDelay bounds the wait for pipes a grandchild still holds after the
// child exits. Without it, Wait blocks on the pipe rather than the process.
const scrollbackWaitDelay = 5 * time.Second

// The two rule 6.3 handovers. Both run through tea.Exec: Bubble Tea
// handles execMsg synchronously in its event loop, and p.exec releases
// the terminal - flushing the renderer and writing the alternate-screen
// exit to the tty - BEFORE the command runs. That program order is the
// ordering guarantee: whatever the command writes lands in the primary
// screen's scrollback, never on the alternate screen. tea.Println
// cannot give this guarantee; its insertAbove bypasses the render queue
// and races the frame flush, and it is documented as a no-op while the
// alternate screen is active.

// handedOverMsg reports that the scrollback write ran. err is non-nil
// only when the terminal could not be released or the write failed.
type handedOverMsg struct{ err error }

// scrollbackWriter is a tea.ExecCommand with no child process: its Run
// writes one block of text to the terminal while Bubble Tea has
// released it. The release is what makes the write reach native
// scrollback, so the writer never needs to touch terminal modes itself.
type scrollbackWriter struct {
	text string
	out  io.Writer
}

// SetStdin implements tea.ExecCommand; the writer reads nothing.
func (w *scrollbackWriter) SetStdin(io.Reader) {}

// SetStderr implements tea.ExecCommand; the writer reports errors
// through its Run result, not stderr.
func (w *scrollbackWriter) SetStderr(io.Writer) {}

// SetStdout captures the writer Bubble Tea injects: the program's own
// terminal output.
func (w *scrollbackWriter) SetStdout(out io.Writer) { w.out = out }

// Run writes the text followed by one newline, so the last line lands
// in scrollback too and the terminal cursor sits on a fresh row.
func (w *scrollbackWriter) Run() error {
	if w.out == nil {
		return errors.New("no output writer was injected")
	}
	_, err := io.WriteString(w.out, strings.TrimRight(w.text, "\n")+"\n")
	return err
}

// handoverDone is the ExecCallback for the scrollback write. A named
// function, not a closure, so its behavior is directly testable.
func handoverDone(err error) tea.Msg {
	return handedOverMsg{err: err}
}

// dumpScrollback enters handover mode and returns the Exec Cmd that
// writes the whole conversation, tool output expanded (Dump), into
// native scrollback.
func (s Screen) dumpScrollback() (Screen, tea.Cmd) {
	w := &scrollbackWriter{text: s.conv.Dump()}
	return s.withMode(modeHandover), tea.Exec(w, handoverDone)
}

// editorDoneMsg reports that the editor opened by `v` exited.
type editorDoneMsg struct {
	err  error
	path string
}

// editorNotice is the status-line text after the editor returns.
func editorNotice(m editorDoneMsg) string {
	if m.err != nil {
		return "editor failed: " + m.err.Error()
	}
	return "transcript saved to " + m.path
}

// errNoEditor is returned when neither $VISUAL nor $EDITOR is set.
var errNoEditor = errors.New("no editor: set $VISUAL or $EDITOR")

// editorCommand resolves the editor, writes the dump to a temp file,
// and builds the command to run on it. dir is the temp directory (tests
// pass a private one); "" means the system default. getenv and
// writeFile are injected the same way: they are the environment and the
// filesystem, and both failure paths are contract, not luck.
//
// os.CreateTemp creates the file with mode 0600, which is the privacy
// contract: a transcript is conversation data and is never
// world-readable.
func editorCommand(getenv func(string) string, writeFile func(string, []byte, fs.FileMode) error, dump, dir string) (*exec.Cmd, string, error) {
	editor := strings.TrimSpace(getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(getenv("EDITOR"))
	}
	if editor == "" {
		return nil, "", errNoEditor
	}
	f, err := os.CreateTemp(dir, "mivia-transcript-*.md")
	if err != nil {
		return nil, "", err
	}
	path := f.Name()
	f.Close()
	if err := writeFile(path, []byte(dump), 0o600); err != nil {
		os.Remove(path)
		return nil, "", err
	}

	// $VISUAL may carry arguments ("code -w"); split, then append the
	// file as the last argument.
	fields := strings.Fields(editor)
	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.WaitDelay = scrollbackWaitDelay
	return cmd, path, nil
}

// openEditor writes the conversation to a temp file and opens it in
// $VISUAL or $EDITOR. With no editor configured it says so on the
// status line instead of failing silently.
func (s Screen) openEditor() (Screen, tea.Cmd) {
	cmd, path, err := editorCommand(os.Getenv, os.WriteFile, s.conv.Dump(), "")
	if err != nil {
		next := s.withMode(modePager)
		next.notice = editorNotice(editorDoneMsg{err: err, path: path})
		return next, nil
	}
	return s, tea.ExecProcess(cmd, editorCallback(path))
}

// editorCallback is the ExecCallback for `v`. It is a constructor, not
// an inline closure, so a test can call the callback itself and pin
// what the screen receives when the editor exits.
func editorCallback(path string) tea.ExecCallback {
	return func(err error) tea.Msg {
		return editorDoneMsg{err: err, path: path}
	}
}

// withMode returns the screen with the mode field set. Screen is a
// value type, so this is how one field changes inside Update.
func (s Screen) withMode(m mode) Screen {
	s.mode = m
	return s
}
