package cli

// The composer textarea and transcript viewport keymaps, declared in one
// place. Nothing here is "whatever bubbles defaults to": every binding the
// TUI honours is chosen, and both constructors are the only way the model
// (and its tests) build these components — tui_layout.go recreates the
// viewport on resize, so a keymap set anywhere else would silently vanish.

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

// newComposerTextarea builds the chat composer with its full keymap.
func newComposerTextarea() textarea.Model {
	ti := textarea.New()
	ti.Prompt = "❯ "
	ti.CharLimit = 0
	ti.ShowLineNumbers = false
	ti.KeyMap.InsertNewline.SetEnabled(true)
	// Word motion: bubbles binds only the Emacs alt-forms. Real terminals
	// deliver ctrl+←/→ as CSI 1;5D/C, which bubbletea parses to
	// "ctrl+left"/"ctrl+right" — bind both conventions.
	ti.KeyMap.WordForward = key.NewBinding(
		key.WithKeys("alt+right", "ctrl+right", "alt+f"),
		key.WithHelp("ctrl+→", "word forward"),
	)
	ti.KeyMap.WordBackward = key.NewBinding(
		key.WithKeys("alt+left", "ctrl+left", "alt+b"),
		key.WithHelp("ctrl+←", "word backward"),
	)
	// ctrl+v is handled by mivia, not by bubbles: the bubbles binding calls
	// atotto/clipboard, which has no Wayland reader and reports failure into
	// textarea.Err — a field nothing renders, so a failed paste was silent.
	ti.KeyMap.Paste.SetEnabled(false)
	// The readline kill/delete family (ctrl+u/ctrl+k/ctrl+w) rides on the
	// bubbles defaults. It is safe only because the transcript viewport
	// below strips its ctrl+u/ctrl+d scroll aliases: a destructive editing
	// key must never double as a scroll key in another pane.
	return ti
}

// newTranscriptViewport builds the transcript pane with scroll keys only.
func newTranscriptViewport(w, h int) viewport.Model {
	vp := viewport.New(w, h)
	// bubbles aliases ctrl+u/ctrl+d onto half-page scrolls. Those bytes are
	// composer editing keys (delete-before-cursor / delete-char-forward);
	// stripping the aliases keeps every destructive key single-meaning.
	// Bare u/d stay: letters reach the viewport only in scrollback focus
	// (INV-TUI-16).
	vp.KeyMap.HalfPageUp = key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "½ page up"),
	)
	vp.KeyMap.HalfPageDown = key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "½ page down"),
	)
	return vp
}
