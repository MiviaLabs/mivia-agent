package chat

import "io"

// NewTestTerminal builds a Terminal that writes to w, for tests that need a
// Terminal without opening a real tty.
func NewTestTerminal(w io.Writer) *Terminal {
	return &Terminal{out: w}
}
