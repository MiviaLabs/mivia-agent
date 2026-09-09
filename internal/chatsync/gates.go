package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

func redactText(s string) string {
	return redact.Text(s)
}

// redactionActive reports whether a redaction policy would rewrite anything.
//
// It gates per-delta STREAMING, not content. redactText runs on one fragment
// at a time, and a pattern cannot match across two fragments, so a secret the
// policy would catch in a settled message escapes when the same bytes arrive
// split. Suppressing the deltas restores the message-sized boundary the
// policy was written against; the settled message still carries the whole
// answer, redacted, so the reader loses nothing but liveness.
func redactionActive() bool {
	return redact.Active()
}

// shouldIncludeToolIO composes the two privacy gates as an AND. Both inputs
// are VALUES on ProjectorOptions: settled decision 7 keeps this package a leaf,
// and reading internal/tools' process-global redaction flag here would also
// mean that any test of this composition mutates a package global.
func shouldIncludeToolIO(opts ProjectorOptions) bool {
	if !opts.IncludeToolIO {
		return false
	}
	if opts.RedactToolArgs {
		return false
	}
	return true
}
