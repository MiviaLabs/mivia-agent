package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// previewFor is the single write path for Metadata's payload previews, and the
// single place that holds the condition under which they exist at all: without
// a sink there is no consumer, so nothing is computed and the field stays
// empty. Any future preview site must go through here.
func (d *Dispatcher) previewFor(b []byte) string {
	if d.policy.Sink == nil {
		return ""
	}
	return redactMeta(b)
}

// redactMeta passes a payload through whatever redaction policy the workspace
// has configured, then caps it at 256 bytes. It guarantees nothing about the
// result: an unconfigured policy removes nothing, so the output may be the raw
// payload. The cap is volume control, not redaction, and applies regardless of
// policy.
func redactMeta(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return truncateText(redact.Text(string(b)))
	}
	x, _ := json.Marshal(redact.JSONValue(v))
	return truncateText(string(x))
}

func truncateText(s string) string {
	if len(s) > 256 {
		s = s[:256]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
