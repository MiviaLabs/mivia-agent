package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

func redactText(s string) string {
	return redact.Text(s)
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
