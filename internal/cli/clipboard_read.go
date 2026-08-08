package cli

// Reading the system clipboard for ctrl+v.
//
// bubbles' textarea binds ctrl+v to atotto/clipboard, which shells out to
// xclip/xsel/pbpaste only - there is no Wayland reader, so ctrl+v is dead on
// most modern Linux desktops. Worse, the failure is reported through
// textarea.Err, which nothing in this app has ever read: the paste simply did
// not happen and the UI said nothing.
//
// mivia reads the clipboard itself, with the same tool list it writes with,
// and turns both outcomes into messages the model can act on.

import (
	"errors"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// clipboardReadTools are the local clipboard readers, in preference order.
// Mirrors clipboardTools (the write side) so copy and paste agree about
// which clipboard they are talking to.
var clipboardReadTools = [][]string{
	{"wl-paste", "--no-newline"},
	{"xclip", "-selection", "clipboard", "-o"},
	{"xsel", "--clipboard", "--output"},
	{"pbpaste"},
}

// errNoClipboardTool reports that no local clipboard reader is installed.
var errNoClipboardTool = errors.New("no clipboard tool found (install wl-clipboard, xclip or xsel)")

// errClipboardToolFailed reports that a reader exists but could not read -
// an empty selection, no compositor, no display. Telling that user to install
// software they already have sends them after the wrong fix.
var errClipboardToolFailed = errors.New("clipboard tool failed to read")

// readClipboardText returns the system clipboard contents using the first
// available local tool.
func readClipboardText() (string, error) {
	found := false
	for _, argv := range clipboardReadTools {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		found = true
		out, err := exec.Command(path, argv[1:]...).Output()
		if err != nil {
			// Tool present but failed (no compositor, no display, empty
			// selection): try the next reader rather than reporting success.
			continue
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	}
	if found {
		return "", errClipboardToolFailed
	}
	return "", errNoClipboardTool
}

// pasteTextMsg carries clipboard text back into the model.
type pasteTextMsg struct{ text string }

// pasteFailedMsg carries a clipboard read failure back into the model, so it
// can be shown instead of swallowed.
type pasteFailedMsg struct{ err error }

// readClipboardCmd reads the clipboard off the update loop.
func readClipboardCmd() tea.Cmd {
	return func() tea.Msg {
		text, err := readClipboardText()
		if err != nil {
			return pasteFailedMsg{err: err}
		}
		return pasteTextMsg{text: text}
	}
}

// routePastedInput decides where a bracketed paste goes. Returns the
// skipTextarea / skipViewport flags for the update path.
//
// Pasted input is never dropped: a blurred textarea ignores every key, and the
// "printable input refocuses the composer" rule in routeFocusKey cannot fire
// for a paste (bubbletea wraps pasted runes in "[...]", so the key string is
// multi-rune). Clicking a message and pasting therefore lost the clipboard
// with no feedback. Modal surfaces still swallow it: they own the screen, and
// text appearing in a composer hidden behind a dialog is worse than nothing.
func (m *tuiModel) routePastedInput() (skipTextarea, skipViewport bool) {
	if m.overlay != nil || m.worktreeDlg != nil {
		return true, true
	}
	if m.mode == modeChat {
		m.setFocus(focusComposer)
	} else {
		m.textarea.Focus()
	}
	return false, true
}

// applyPastedText inserts clipboard text fetched by readClipboardCmd.
func (m *tuiModel) applyPastedText(text string) {
	if text == "" {
		return
	}
	if m.mode == modeChat {
		m.setFocus(focusComposer)
	} else {
		m.textarea.Focus()
	}
	m.textarea.InsertString(text)
	if m.mode == modeChat || m.mode == modeWelcome {
		m.syncSuggest()
	}
}

// notePasteFailure surfaces a clipboard read failure in the chrome. Silence
// here is the defect: the previous ctrl+v path stored its error in
// textarea.Err, which nothing rendered.
func (m *tuiModel) notePasteFailure(err error) {
	if errors.Is(err, errNoClipboardTool) {
		m.setNotice("no clipboard tool - install wl-clipboard/xclip, or paste with ctrl+shift+v")
		return
	}
	m.setNotice("clipboard read failed - use the terminal's own paste (ctrl+shift+v)")
}
