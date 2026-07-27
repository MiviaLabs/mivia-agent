// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

// Terminal wraps raw terminal mode for interactive input.
// Provides cursor management, screen manipulation, and key reading.
type Terminal struct {
	fd       int
	oldState *term.State
	width    int
	height   int
	out      io.Writer
	mu       sync.Mutex
}

// NewTerminal opens the terminal, enters raw mode, and reports size.
// Must be closed with Close() when done.
func NewTerminal() (*Terminal, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, fmt.Errorf("stdin is not a terminal")
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("raw terminal: %w", err)
	}
	w, h, err := term.GetSize(fd)
	if err != nil {
		term.Restore(fd, oldState)
		return nil, fmt.Errorf("get terminal size: %w", err)
	}

	// Enable bracketed paste mode so we can detect pasted text.
	t := &Terminal{
		fd:       fd,
		oldState: oldState,
		width:    w,
		height:   h,
		out:      os.Stderr,
	}
	// Enable bracketed paste mode.
	fmt.Fprint(t.out, "\033[?2004h")
	return t, nil
}

// Close restores the terminal to cooked mode and disables bracketed paste.
func (t *Terminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.oldState != nil {
		// Disable bracketed paste mode before restoring.
		fmt.Fprint(t.out, "\033[?2004l")
		term.Restore(t.fd, t.oldState)
		t.oldState = nil
	}
	return nil
}

// Size returns the terminal width and height.
func (t *Terminal) Size() (width, height int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Try to get live size, fall back to cached.
	w, h, err := term.GetSize(t.fd)
	if err == nil {
		t.width, t.height = w, h
	}
	return t.width, t.height
}

// ReadKey reads a single keypress in raw mode.
// Returns the key as a string (for multi-byte sequences like arrows)
// or the rune as a string (for regular keys).
func (t *Terminal) ReadKey() (string, error) {
	buf := make([]byte, 8)
	n, err := os.Stdin.Read(buf)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", io.EOF
	}
	return string(buf[:n]), nil
}

// Write implements io.Writer for Terminal, delegating to the underlying stderr writer.
func (t *Terminal) Write(p []byte) (n int, err error) {
	if t == nil {
		return os.Stderr.Write(p)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.out.Write(p)
}

// WriteString writes to the terminal output (stderr).
func (t *Terminal) WriteString(s string) {
	if t == nil {
		_, _ = fmt.Fprint(os.Stderr, s)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprint(t.out, s)
}

// Writef writes a formatted string to the terminal.
func (t *Terminal) Writef(format string, args ...any) {
	if t == nil {
		_, _ = fmt.Fprintf(os.Stderr, format, args...)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.out, format, args...)
}

// ClearLine clears the current line from cursor to end.
func (t *Terminal) ClearLine() {
	t.WriteString("\033[K")
}

// ClearLines clears n lines upward from current cursor position.
func (t *Terminal) ClearLines(n int) {
	for i := 0; i < n; i++ {
		t.WriteString("\033[2K") // clear entire line
		if i < n-1 {
			t.WriteString("\033[A") // cursor up
		}
	}
}

// MoveTo moves cursor to (row, col) — 1-based.
func (t *Terminal) MoveTo(row, col int) {
	t.Writef("\033[%d;%dH", row, col)
}

// MoveUp moves cursor up n rows.
func (t *Terminal) MoveUp(n int) {
	if n > 0 {
		t.Writef("\033[%dA", n)
	}
}

// MoveDown moves cursor down n rows.
func (t *Terminal) MoveDown(n int) {
	if n > 0 {
		t.Writef("\033[%dB", n)
	}
}

// SaveScreen saves the current screen contents.
func (t *Terminal) SaveScreen() {
	t.WriteString("\033[?47h")
}

// RestoreScreen restores the saved screen contents.
func (t *Terminal) RestoreScreen() {
	t.WriteString("\033[?47l")
}

// HideCursor hides the cursor.
func (t *Terminal) HideCursor() {
	t.WriteString("\033[?25l")
}

// ShowCursor shows the cursor.
func (t *Terminal) ShowCursor() {
	t.WriteString("\033[?25h")
}
