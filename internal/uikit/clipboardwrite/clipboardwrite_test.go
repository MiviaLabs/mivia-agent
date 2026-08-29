package clipboardwrite

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func fakeLookPath(found ...string) func(string) (string, error) {
	set := make(map[string]bool, len(found))
	for _, f := range found {
		set[f] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestPickCandidateNoDisplayIsANoOp(t *testing.T) {
	// No local display at all: an SSH session onto a headless host.
	// Every tool present in PATH must still be skipped - this fallback
	// only ever runs for a local session, never over the wire.
	_, ok := pickCandidate("linux", nil, fakeLookPath("wl-copy", "xclip", "xsel"))
	if ok {
		t.Fatal("no display env must never pick a candidate")
	}
}

func TestPickCandidateWaylandPrefersWlCopy(t *testing.T) {
	env := []string{"WAYLAND_DISPLAY=wayland-0", "DISPLAY=:0"}
	c, ok := pickCandidate("linux", env, fakeLookPath("wl-copy", "xclip", "xsel"))
	if !ok || c.name != "wl-copy" {
		t.Fatalf("got %+v ok=%v, want wl-copy", c, ok)
	}
}

func TestPickCandidateWaylandFallsBackToXclipWhenWlCopyMissing(t *testing.T) {
	env := []string{"WAYLAND_DISPLAY=wayland-0", "DISPLAY=:0"}
	c, ok := pickCandidate("linux", env, fakeLookPath("xclip", "xsel"))
	if !ok || c.name != "xclip" {
		t.Fatalf("got %+v ok=%v, want xclip", c, ok)
	}
}

func TestPickCandidateX11FallsBackToXselWhenXclipMissing(t *testing.T) {
	env := []string{"DISPLAY=:0"}
	c, ok := pickCandidate("linux", env, fakeLookPath("xsel"))
	if !ok || c.name != "xsel" {
		t.Fatalf("got %+v ok=%v, want xsel", c, ok)
	}
}

func TestPickCandidateDisplaySetButNoToolInstalled(t *testing.T) {
	env := []string{"DISPLAY=:0", "WAYLAND_DISPLAY=wayland-0"}
	_, ok := pickCandidate("linux", env, fakeLookPath())
	if ok {
		t.Fatal("a display with no installed clipboard tool must not pick one")
	}
}

func TestPickCandidateDarwinPrefersPbcopyRegardlessOfDisplay(t *testing.T) {
	c, ok := pickCandidate("darwin", nil, fakeLookPath("pbcopy"))
	if !ok || c.name != "pbcopy" {
		t.Fatalf("got %+v ok=%v, want pbcopy", c, ok)
	}
}

func TestPickCandidateDarwinMissingPbcopyIsANoOp(t *testing.T) {
	_, ok := pickCandidate("darwin", nil, fakeLookPath())
	if ok {
		t.Fatal("darwin without pbcopy on PATH must not pick a candidate")
	}
}

func TestPickCandidateWindowsUsesClipExe(t *testing.T) {
	c, ok := pickCandidate("windows", nil, fakeLookPath("clip.exe"))
	if !ok || c.name != "clip.exe" {
		t.Fatalf("got %+v ok=%v, want clip.exe", c, ok)
	}
}

func TestPickCandidateWindowsMissingClipExeIsANoOp(t *testing.T) {
	_, ok := pickCandidate("windows", nil, fakeLookPath())
	if ok {
		t.Fatal("windows without clip.exe on PATH must not pick a candidate")
	}
}

func TestPickCandidateUnknownGOOSTreatedAsUnixLike(t *testing.T) {
	// freebsd, netbsd, openbsd, solaris, dragonfly: same X11/Wayland
	// rules as linux.
	env := []string{"DISPLAY=:0"}
	c, ok := pickCandidate("freebsd", env, fakeLookPath("xclip"))
	if !ok || c.name != "xclip" {
		t.Fatalf("got %+v ok=%v, want xclip", c, ok)
	}
}

// fakeRunner records the last command it was asked to run instead of
// actually executing anything, so Write's wiring is testable without a
// real xclip/pbcopy/clip.exe on the machine running the test.
type fakeRunner struct {
	name  string
	args  []string
	stdin string
	err   error
	calls int
}

func (r *fakeRunner) Run(name string, args []string, stdin string) error {
	r.name, r.args, r.stdin = name, args, stdin
	r.calls++
	return r.err
}

func TestWriteRunsThePickedCandidateWithTextOnStdin(t *testing.T) {
	r := &fakeRunner{}
	env := []string{"DISPLAY=:0"}
	err := write("hello clip", "linux", env, fakeLookPath("xclip"), r)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if r.calls != 1 || r.name != "xclip" || r.stdin != "hello clip" {
		t.Fatalf("runner got name=%q stdin=%q calls=%d", r.name, r.stdin, r.calls)
	}
}

func TestWriteNoCandidateReturnsErrNoTool(t *testing.T) {
	r := &fakeRunner{}
	if err := write("x", "linux", nil, fakeLookPath("xclip"), r); !errors.Is(err, ErrNoClipboardTool) {
		t.Fatalf("write err = %v, want ErrNoClipboardTool", err)
	}
	if r.calls != 0 {
		t.Fatal("no candidate must never invoke the runner")
	}
}

func TestWritePropagatesRunnerError(t *testing.T) {
	wantErr := errors.New("xclip: broken pipe")
	r := &fakeRunner{err: wantErr}
	env := []string{"DISPLAY=:0"}
	if err := write("x", "linux", env, fakeLookPath("xclip"), r); !errors.Is(err, wantErr) {
		t.Fatalf("write err = %v, want %v", err, wantErr)
	}
}

// TestExecRunnerFeedsStdinToTheCommand exercises the real Runner end
// to end, substituting the portable `cat` command for a real
// xclip/pbcopy install: it proves the happy path actually starts the
// process, writes the text, closes stdin, and waits, rather than
// short-circuiting on a misread error check.
func TestExecRunnerFeedsStdinToTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command (sh -c); no Windows equivalent wired here")
	}
	outPath := filepath.Join(t.TempDir(), "out.txt")
	r := execRunner{}
	if err := r.Run("sh", []string{"-c", "cat > " + outPath}, "hello clipboard"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello clipboard" {
		t.Fatalf("got %q, want %q", got, "hello clipboard")
	}
}

// TestExecRunnerPropagatesCommandFailure covers the Wait() error path:
// a command that exits non-zero must surface that as an error.
func TestExecRunnerPropagatesCommandFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command (sh -c); no Windows equivalent wired here")
	}
	r := execRunner{}
	if err := r.Run("sh", []string{"-c", "exit 7"}, ""); err == nil {
		t.Fatal("a non-zero exit must surface as an error")
	}
}

// TestExecRunnerUnknownCommandErrors covers the Start() error path: a
// command that does not exist must surface that as an error rather
// than panicking or hanging.
func TestExecRunnerUnknownCommandErrors(t *testing.T) {
	r := execRunner{}
	if err := r.Run("mivia-clipboardwrite-test-nonexistent-binary", nil, ""); err == nil {
		t.Fatal("an unresolvable command must surface as an error")
	}
}
