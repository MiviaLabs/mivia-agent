// Clipboard and text selection.
//
// A full-screen TUI with mouse capture takes the terminal's native selection
// away — you cannot drag-select a message, and you cannot even select what
// you typed in the composer. Two complementary answers:
//
//   - Block copy (y / ctrl+y, right-click, and ctrl+c when idle with a
//     selection) puts a whole message on the system clipboard via OSC 52,
//     which works over SSH and needs no external binary.
//   - Select mode (ctrl+s) releases mouse capture so the terminal's own
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
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func timeNow() time.Time { return time.Now() }

// copiedNotice is the acknowledgement shown after a successful copy.
func copiedNotice(n int) string {
	return fmt.Sprintf("copied %d chars", n)
}

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

// copyToClipboardCmd writes the clipboard sequence to the terminal. The
// sequence is zero-width chrome, not frame content, so it is written
// directly rather than routed through the renderer.
func copyToClipboardCmd(text string) tea.Cmd {
	seq := osc52Copy(text)
	if seq == "" {
		return nil
	}
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(seq)
		return nil
	}
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

// copySelectedBlock copies the selected block and acknowledges it. Silence
// after a copy is indistinguishable from a broken key.
func (m *tuiModel) copySelectedBlock() (tea.Cmd, bool) {
	text, ok := m.selectedBlockCopyText()
	if !ok {
		return nil, false
	}
	cmd := copyToClipboardCmd(text)
	if cmd == nil {
		m.stepDetail = "too large to copy"
		m.stepDetailAt = timeNow()
		return nil, true
	}
	m.stepDetail = copiedNotice(len(text))
	m.stepDetailAt = timeNow()
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
	m.stepDetail = "select mode · drag to select · ctrl+s back"
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
