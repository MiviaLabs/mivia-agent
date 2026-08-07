package controller

import (
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// evidencePreviewBytes bounds the preview inside a reference envelope.
const evidencePreviewBytes = 4 << 10

// buildEvidenceEnvelope builds the ledger-backed reference envelope substituted
// for a prior-step output that exceeds the binding's max_bytes. The artifact
// fields point into the workflow ledger (run-scoped, immutable content), so the
// downstream child reads the full value with workflow_inspect(run_id, step,
// attempt) instead of receiving the oversized bytes inline. The preview is
// bounded, rune-safe, UTF-8-sanitized, and redacted, so the envelope itself
// always fits a normal binding cap.
func buildEvidenceEnvelope(runID string, prior workflowledger.StepAttempt, raw []byte) map[string]any {
	return map[string]any{
		"artifact": map[string]any{
			"step":    prior.StepID,
			"attempt": prior.AttemptNo,
			"ref":     prior.OutputRef,
			"bytes":   len(raw),
			"digest":  prior.OutputDigest,
			"preview": evidencePreview(raw, evidencePreviewBytes),
		},
		"note": "full artifact is in the workflow ledger; read it with workflow_inspect(run_id=" + runID + ", step=<step>, attempt=<attempt>)",
	}
}

// evidencePreview returns the first max bytes of raw as a valid UTF-8 string
// (invalid bytes replaced with U+FFFD), truncated on a rune boundary, and
// redacted by the workspace redaction policy (a nil policy redacts nothing).
func evidencePreview(raw []byte, max int) string {
	text := strings.ToValidUTF8(string(raw), "\uFFFD")
	if len(text) > max {
		text = text[:max]
		for len(text) > 0 && !utf8.RuneStart(text[len(text)-1]) {
			text = text[:len(text)-1]
		}
	}
	return redact.Text(text)
}
