// Package clipboardwrite shells out to the local system's clipboard
// tool as a fallback delivery path alongside OSC 52. Some terminals
// (VTE-based ones - GNOME Terminal, Tilix, Terminator - refuse OSC 52
// on principle) never act on the escape sequence a program sends, so
// a copy silently goes nowhere. When mivia runs locally rather than
// over SSH, a direct clipboard-tool invocation still reaches the
// system clipboard even though the terminal ignored the sequence.
//
// Over SSH there is no local display, so no candidate ever applies and
// Write is a no-op - OSC 52 remains the only transport across that
// boundary, exactly as before this package existed.
package clipboardwrite

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// clipboardWaitDelay bounds the wait for pipes a grandchild still holds after the
// child exits. Without it, Wait blocks on the pipe rather than the process.
const clipboardWaitDelay = 5 * time.Second

// ErrNoClipboardTool reports that no local clipboard tool applies:
// either no display is set (an SSH session) or none of the tools this
// package knows about are on PATH. The caller treats this as benign -
// OSC 52 already carried the same text.
var ErrNoClipboardTool = errors.New("clipboardwrite: no local clipboard tool available")

// candidate is one clipboard command this package can shell out to.
type candidate struct {
	name string
	args []string
}

// lookPathFunc resolves a command name to a path, or errors if it is
// not on PATH. Matches exec.LookPath's signature so tests can inject a
// fake without needing real binaries installed.
type lookPathFunc func(name string) (string, error)

// Runner executes one clipboard command, feeding stdin the text to
// copy. The real implementation shells out; tests use a fake that
// records the call instead.
type Runner interface {
	Run(name string, args []string, stdin string) error
}

// execRunner is the real Runner: it starts the command, writes stdin,
// closes it, and waits. xclip and wl-copy fork and detach to keep
// serving the selection after this returns, exactly as intended.
type execRunner struct{}

func (execRunner) Run(name string, args []string, stdin string) error {
	cmd := exec.Command(name, args...)
	cmd.WaitDelay = clipboardWaitDelay
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := in.Write([]byte(stdin)); err != nil {
		return err
	}
	if err := in.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}

// pickCandidate chooses which clipboard command to run for goos and
// env, or reports that none applies. Wayland is preferred over X11 on
// Linux-like systems when both are present (a common XWayland
// coexistence case); darwin and windows have one well-known tool each
// and no display gate, since pbcopy/clip.exe talk to the OS directly
// rather than an X11/Wayland display server.
func pickCandidate(goos string, env []string, lookPath lookPathFunc) (candidate, bool) {
	switch goos {
	case "darwin":
		if _, err := lookPath("pbcopy"); err == nil {
			return candidate{name: "pbcopy"}, true
		}
		return candidate{}, false
	case "windows":
		if _, err := lookPath("clip.exe"); err == nil {
			return candidate{name: "clip.exe"}, true
		}
		return candidate{}, false
	default:
		return pickUnixCandidate(env, lookPath)
	}
}

func pickUnixCandidate(env []string, lookPath lookPathFunc) (candidate, bool) {
	if getenv(env, "WAYLAND_DISPLAY") != "" {
		if _, err := lookPath("wl-copy"); err == nil {
			return candidate{name: "wl-copy"}, true
		}
	}
	if getenv(env, "DISPLAY") != "" {
		if _, err := lookPath("xclip"); err == nil {
			return candidate{name: "xclip", args: []string{"-selection", "clipboard"}}, true
		}
		if _, err := lookPath("xsel"); err == nil {
			return candidate{name: "xsel", args: []string{"--clipboard", "--input"}}, true
		}
	}
	return candidate{}, false
}

func getenv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if len(kv) > len(prefix) && kv[:len(prefix)] == prefix {
			return kv[len(prefix):]
		}
	}
	return ""
}

// write is pickCandidate plus execution, both parameterized so tests
// never touch the real environment, PATH, or a subprocess.
func write(text, goos string, env []string, lookPath lookPathFunc, r Runner) error {
	c, ok := pickCandidate(goos, env, lookPath)
	if !ok {
		return ErrNoClipboardTool
	}
	return r.Run(c.name, c.args, text)
}

// Write attempts a local clipboard-tool copy for the current OS and
// environment. Errors (no tool, or the tool failed) are the caller's
// to ignore or log - this is a best-effort redundant path, and OSC 52
// already carries the same text to terminals that honor it.
func Write(text string) error {
	return write(text, runtime.GOOS, os.Environ(), exec.LookPath, execRunner{})
}
