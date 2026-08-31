package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func redactText(s string) string {
	return redact.Text(s)
}

func shouldIncludeToolIO(opts ProjectorOptions) bool {
	if !opts.IncludeToolIO {
		return false
	}
	if tools.RedactToolArgs() {
		return false
	}
	return true
}
