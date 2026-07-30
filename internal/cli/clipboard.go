// Clipboard and text selection.
//
// A full-screen TUI with mouse capture takes the terminal's native selection
// away — you cannot drag-select a message, and you cannot even select what
// you typed in the composer. Two complementary answers:
//
//   - Block copy (y / ctrl+y, right-click, and ctrl+c when idle with a
//     selection) puts a whole message on the system clipboard via OSC 52,
//     which works over SSH and needs no external binary.
//   - Select mode (ctrl+e) releases mouse capture so the terminal's own
//     selection works everywhere, including the composer. Nothing the app
//     can implement beats the terminal at arbitrary drag-selection, so the
//     honest fix is to get out of its way and say so in the chrome.
//
// ctrl+c deliberately keeps its terminal meaning (cancel the turn, then
// quit). Remapping the most ingrained key in the terminal would cost more
// than it gains; it copies only in the one unambiguous case — idle, with a
// block selected — and ctrl+q is added as a plain quit for discoverability.
package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
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
// Wayland first, then X11, then macOS.
var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
}

// clipboardToolCommand returns a command that writes text to the system
// clipboard using a local binary, or nil when none is installed.
func clipboardToolCommand(text string) *exec.Cmd {
	for _, argv := range clipboardTools {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, argv[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd
	}
	return nil
}

// copyResultMsg reports what a copy actually achieved. Without it the UI
// acknowledged copies that never landed: cmd.Run()'s error was discarded, so
// a wl-copy with no compositor still printed "copied N chars".
type copyResultMsg struct {
	n   int
	err error
}

// clipboardTTY returns the terminal device OSC 52 is written to. Overridable
// for tests; never a real device there.
func clipboardTTY() string {
	if p := strings.TrimSpace(os.Getenv("MIVIA_CLIPBOARD_TTY")); p != "" {
		return p
	}
	return "/dev/tty"
}

// writeOSC52 sends the clipboard sequence to the terminal.
//
// /dev/tty, not stdout: the sequence must reach the terminal even when stdout
// is redirected. Known accepted race — bubbletea's renderer writes frames from
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
// limit is a property of that transport alone — a local binary copies text of
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
		delivered := false
		var lastErr error
		if cmd := clipboardToolCommand(text); cmd != nil {
			if err := cmd.Run(); err != nil {
				lastErr = err
			} else {
				delivered = true
			}
		}
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

// freshStepDetail returns the transient notice while it is still current.
// Copy and paste acknowledgements expire (copyNoticeTTL) so a stale one never
// reads as a claim about what just happened. Progress heartbeats during a
// turn keep their own rendering path in the composer footer.
func (m *tuiModel) freshStepDetail() string {
	if m.stepDetail == "" || m.stepDetailAt.IsZero() {
		return ""
	}
	if timeNow().Sub(m.stepDetailAt) > copyNoticeTTL {
		return ""
	}
	return m.stepDetail
}

// noteCopyResult turns a delivery outcome into the on-screen acknowledgement.
func (m *tuiModel) noteCopyResult(msg copyResultMsg) {
	if msg.err != nil {
		m.stepDetail = "copy failed — no clipboard tool reachable"
	} else {
		m.stepDetail = copiedNotice(msg.n)
	}
	m.stepDetailAt = timeNow()
}

// selectedBlockCopyText returns the SOURCE text of the selected block.
// Never the rendered lines: pasting ANSI escapes into an editor is worse
// than useless.
func (m *tuiModel) selectedBlockCopyText() (string, bool) {
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
			text = stripANSI(b.Rendered)
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
func (m *tuiModel) copySelectedBlock() (tea.Cmd, bool) {
	text, ok := m.selectedBlockCopyText()
	if !ok {
		return nil, false
	}
	cmd := copyToClipboardCmd(text)
	if cmd == nil {
		m.stepDetail = "too large to copy without a local clipboard tool"
		m.stepDetailAt = timeNow()
		return nil, true
	}
	return cmd, true
}

// copyBlockByID copies a specific block (mouse path) without disturbing the
// keyboard selection.
func (m *tuiModel) copyBlockByID(id string) (tea.Cmd, bool) {
	prev := m.selectedBlockID
	m.selectedBlockID = id
	cmd, ok := m.copySelectedBlock()
	m.selectedBlockID = prev
	return cmd, ok
}

// toggleSelectMode releases or reclaims mouse capture.
func (m *tuiModel) toggleSelectMode() tea.Cmd {
	m.mouseEnabled = !m.mouseEnabled
	if m.mouseEnabled {
		m.stepDetail = ""
		return tea.EnableMouseCellMotion
	}
	m.stepDetail = "select mode · drag to select · ctrl+e back"
	m.stepDetailAt = timeNow()
	return tea.DisableMouse
}

// ctrlCCopy handles the one case where ctrl+c copies instead of cancelling:
// idle, scrollback focus, a block selected. Mid-turn ctrl+c must always
// cancel, so the guard is deliberately narrow.
func (m *tuiModel) ctrlCCopy() (tea.Cmd, bool) {
	if m.waiting || m.focus != focusScrollback || m.selectedBlockID == "" {
		return nil, false
	}
	return m.copySelectedBlock()
}
