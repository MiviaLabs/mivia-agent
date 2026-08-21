// Clipboard and text selection.
//
// A full-screen TUI with mouse capture takes the terminal's native selection
// away - you cannot drag-select a message, and you cannot even select what
// you typed in the composer. Two complementary answers:
//
//   - Block copy (y / ctrl+y, right-click, and ctrl+c when idle with a
//     selection) puts a whole message on the system clipboard via OSC 52,
//     which works over SSH and needs no external binary.
//   - Select mode (F2, or /select) releases mouse capture so the terminal’s own
//     selection works everywhere, including the composer. Nothing the app
//     can implement beats the terminal at arbitrary drag-selection, so the
//     honest fix is to get out of its way and say so in the chrome.
//
// ctrl+c keeps its terminal meaning while a turn runs: it cancels. At rest it
// copies a selected message (in either focus - requiring scrollback focus made
// the reflexive press quit the app instead), clears a draft rather than
// destroying it, and otherwise arms a quit that a second press confirms.
// ctrl+q remains the unambiguous immediate quit. See tui_cancel.go.
package legacytui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func timeNow() time.Time { return time.Now() }

// copiedNotice is the acknowledgement shown after a successful copy.
func copiedNotice(n int) string {
	return fmt.Sprintf("copied %d chars", n)
}

// copyNoticeTTL bounds how long a copy acknowledgement stays on screen. A
// notice that never clears becomes furniture, and then becomes a claim about
// a copy made minutes ago.
const copyNoticeTTL = 4 * time.Second

// osc52MaxBytes bounds a clipboard write. Terminals silently drop or
// truncate very large OSC 52 payloads, and a half-pasted message is worse
// than a refusal the user can see.
const osc52MaxBytes = 64 * 1024

// osc52Copy builds the OSC 52 sequence that sets the system clipboard.
// Returns "" when there is nothing safe to send.
func osc52Copy(text string) string {
	if text == "" || len(text) > osc52MaxBytes {
		return ""
	}
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
}

// clipboardTools are the local clipboard writers, in preference order.
// Wayland first, then X11, macOS, then Windows.
var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
	{"clip"},
}

// clipboardToolCommand returns a command that writes text to the system
// clipboard using a local binary, or nil when none is installed.
func clipboardToolCommand(text string) *exec.Cmd {
	for _, argv := range clipboardTools {
		if cmd := clipboardWriteCommandAt(argv, text); cmd != nil {
			return cmd
		}
	}
	return nil
}

// clipboardWriteCommandAt builds one writer command when its binary exists.
func clipboardWriteCommandAt(argv []string, text string) *exec.Cmd {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil
	}
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	return cmd
}

// writeClipboardTools runs the installed writers in order until one succeeds.
// Stopping at the first binary *found* meant an X11 session with both
// wl-clipboard and xclip installed reported "no clipboard tool reachable"
// after wl-copy failed, without ever trying xclip. The read path already
// falls through; the write path must too.
func writeClipboardTools(text string) (bool, error) {
	var lastErr error
	found := false
	for _, argv := range clipboardTools {
		cmd := clipboardWriteCommandAt(argv, text)
		if cmd == nil {
			continue
		}
		found = true
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		return true, nil
	}
	if !found {
		return false, nil
	}
	return false, lastErr
}

// copyResultMsg reports what a copy actually achieved. Without it the UI
// acknowledged copies that never landed: cmd.Run()'s error was discarded, so
// a wl-copy with no compositor still printed "copied N chars".
type copyResultMsg struct {
	n   int
	err error
}

// envClipboardTTY is the environment variable overriding the terminal device
// clipboardTTY writes OSC 52 to.
const envClipboardTTY = "MIVIA_CLIPBOARD_TTY"

// clipboardTTY returns the terminal device OSC 52 is written to. Overridable
// for tests; never a real device there.
func clipboardTTY() string {
	if p := strings.TrimSpace(os.Getenv(envClipboardTTY)); p != "" {
		return p
	}
	return "/dev/tty"
}

// writeOSC52 sends the clipboard sequence to the terminal.
//
// /dev/tty, not stdout: the sequence must reach the terminal even when stdout
// is redirected. Known accepted race - bubbletea's renderer writes frames from
// its own goroutine under a private mutex, and nothing serializes an
// out-of-band write against it, so a frame can in principle interleave with a
// large payload. One Write call keeps the window as small as it can be
// without a clipboard API in bubbletea v1.
func writeOSC52(seq string) error {
	f, err := os.OpenFile(clipboardTTY(), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(seq)
	return err
}

// copyToClipboardCmd delivers text to the system clipboard and reports the
// outcome.
//
// A local clipboard binary is tried first: OSC 52 is refused by default in
// several terminals and multiplexers (tmux without set-clipboard, xterm
// without allowWindowOps), so relying on it alone made copying silently do
// nothing. OSC 52 still runs as the fallback because it is the only thing
// that works over SSH, and it is harmless when both succeed. The OSC 52 size
// limit is a property of that transport alone - a local binary copies text of
// any size, so an oversized block is refused only when no binary exists.
func copyToClipboardCmd(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	seq := osc52Copy(text)
	if seq == "" && clipboardToolCommand(text) == nil {
		return nil
	}
	return func() tea.Msg {
		delivered, lastErr := writeClipboardTools(text)
		if seq != "" {
			if err := writeOSC52(seq); err != nil {
				if lastErr == nil {
					lastErr = err
				}
			} else {
				delivered = true
			}
		}
		if delivered {
			return copyResultMsg{n: len(text)}
		}
		if lastErr == nil {
			lastErr = errors.New("no clipboard delivery path")
		}
		return copyResultMsg{n: len(text), err: lastErr}
	}
}

// setNotice publishes a transient acknowledgement.
func (m *TUIModel) setNotice(text string) {
	m.notice = text
	m.noticeAt = timeNow()
}

// freshNotice returns the transient acknowledgement while it is still
// current. Copy and paste notices expire (copyNoticeTTL) so a stale one never
// reads as a claim about what just happened. The live tool heartbeat is a
// different field (stepDetail) with a different lifetime.
func (m *TUIModel) freshNotice() string {
	if m.notice == "" || m.noticeAt.IsZero() {
		return ""
	}
	if timeNow().Sub(m.noticeAt) > copyNoticeTTL {
		return ""
	}
	return m.notice
}

// noteCopyResult turns a delivery outcome into the on-screen acknowledgement.
func (m *TUIModel) noteCopyResult(msg copyResultMsg) {
	if msg.err != nil {
		m.setNotice("copy failed - no clipboard tool reachable")
		return
	}
	m.setNotice(copiedNotice(msg.n))
}

// selectedBlockCopyText returns the SOURCE text of the selected block.
// Never the rendered lines: pasting ANSI escapes into an editor is worse
// than useless.
func (m *TUIModel) selectedBlockCopyText() (string, bool) {
	if m.selectedBlockID == "" {
		return "", false
	}
	for i := range m.blocks {
		if m.blocks[i].ID != m.selectedBlockID {
			continue
		}
		b := m.blocks[i]
		text := b.Text
		if text == "" {
			text = cli.StripANSI(b.Rendered)
		}
		if text == "" {
			return "", false
		}
		return text, true
	}
	return "", false
}

// copySelectedBlock copies the selected block. The acknowledgement is set
// when delivery reports back (copyResultMsg): claiming success up front is
// how a failed copy came to print "copied N chars".
func (m *TUIModel) copySelectedBlock() (tea.Cmd, bool) {
	text, ok := m.selectedBlockCopyText()
	if !ok {
		return nil, false
	}
	cmd := copyToClipboardCmd(text)
	if cmd == nil {
		m.setNotice("too large to copy without a local clipboard tool")
		return nil, true
	}
	return cmd, true
}

// copyBlockByID copies a specific block (mouse path) without disturbing the
// keyboard selection.
func (m *TUIModel) copyBlockByID(id string) (tea.Cmd, bool) {
	prev := m.selectedBlockID
	m.selectedBlockID = id
	cmd, ok := m.copySelectedBlock()
	m.selectedBlockID = prev
	return cmd, ok
}

// toggleSelectMode releases or reclaims mouse capture.
func (m *TUIModel) toggleSelectMode() tea.Cmd {
	m.mouseEnabled = !m.mouseEnabled
	if m.mouseEnabled {
		m.stepDetail = ""
		return tea.EnableMouseCellMotion
	}
	m.stepDetail = "select mode · drag to select · F2 back"
	m.stepDetailAt = timeNow()
	return tea.DisableMouse
}

// ctrlCCopy handles the one case where ctrl+c copies instead of cancelling:
// idle, scrollback focus, a block selected. Mid-turn ctrl+c must always
// cancel, so the guard is deliberately narrow.
func (m *TUIModel) ctrlCCopy() (tea.Cmd, bool) {
	if m.waiting || m.focus != cli.FocusScrollback || m.selectedBlockID == "" {
		return nil, false
	}
	return m.copySelectedBlock()
}
