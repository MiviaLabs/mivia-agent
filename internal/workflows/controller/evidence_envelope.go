package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// evidencePreviewBytes bounds the preview inside a reference envelope.
const evidencePreviewBytes = 4 << 10

// envelopeFitSlack is the safety margin subtracted from the preview budget so
// the marshaled envelope stays under the binding cap even when the measured
// skeleton is slightly conservative (e.g. key reordering or escaping edge
// cases). 64 bytes covers the worst realistic inflation of the fixed fields.
const envelopeFitSlack = 64

// maxEnvelopeFitIterations bounds the preview-halving loop that shrinks a
// preview whose HTML-escaped form overflows the binding cap. Each iteration
// halves the preview, so ~3 iterations cover even the densest escaping; 8 is
// a generous ceiling that keeps the loop strictly bounded.
const maxEnvelopeFitIterations = 8

// buildEvidenceEnvelope builds the ledger-backed reference envelope substituted
// for a prior-step output that exceeds the binding's max_bytes. The artifact
// fields point into the workflow ledger (run-scoped, immutable content), so the
// downstream child reads the full value with workflow_inspect(run_id, step,
// attempt) instead of receiving the oversized bytes inline.
//
// The envelope is sized so its MARSHALED form always fits cap: the no-preview
// skeleton overhead is measured exactly by marshaling the envelope map with an
// empty preview, the preview is budgeted to the remaining room (min of
// evidencePreviewBytes and cap - skeleton - envelopeFitSlack), and a bounded
// halving loop shrinks the preview if JSON HTML-escaping (& < > -> \u0026
// \u003c \u003e) pushes the marshaled size over cap. When the budget is
// exhausted the preview field is omitted entirely. Only a cap that cannot fit
// even the no-preview envelope returns an error; the caller keeps the
// historical size-reject as defense-in-depth for those pathological caps.
func buildEvidenceEnvelope(runID string, prior workflowledger.StepAttempt, raw []byte, cap int) (map[string]any, error) {
	note := "full artifact is in the workflow ledger; read it with workflow_inspect(run_id=" + runID + ", step=<step>, attempt=<attempt>)"
	// The preview key is added only after the skeleton is measured, so the
	// measured skeleton IS the true no-preview size (a "preview":"" entry
	// would overstate it by 12 bytes and reject caps that fit the ref-only
	// envelope).
	artifact := map[string]any{
		"step":    prior.StepID,
		"attempt": prior.AttemptNo,
		"ref":     prior.OutputRef,
		"bytes":   len(raw),
		"digest":  prior.OutputDigest,
	}
	envelope := map[string]any{"artifact": artifact, "note": note}
	skeleton, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if len(skeleton) > cap {
		return nil, fmt.Errorf("reference envelope skeleton is %d bytes, cap is %d", len(skeleton), cap)
	}
	budget := min(evidencePreviewBytes, cap-len(skeleton)-envelopeFitSlack)
	if budget <= 0 {
		// No room for a preview: keep the preview key absent, so the marshaled
		// envelope stays exactly the skeleton size (<= cap by the check above).
		return envelope, nil
	}
	artifact["preview"] = evidencePreview(raw, budget)
	for i := 0; i < maxEnvelopeFitIterations; i++ {
		marshaled, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		if len(marshaled) <= cap {
			return envelope, nil
		}
		preview := artifact["preview"].(string)
		if preview == "" {
			break
		}
		artifact["preview"] = truncateRunes(preview, len(preview)/2)
	}
	// The halving loop exhausted without fitting (pathologically dense
	// escaping): drop the preview; the no-preview envelope fits by the
	// skeleton check above.
	delete(artifact, "preview")
	marshaled, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if len(marshaled) > cap {
		return nil, fmt.Errorf("reference envelope is %d bytes, cap is %d", len(marshaled), cap)
	}
	return envelope, nil
}

// evidencePreview returns the first max bytes of raw as a valid UTF-8 string:
// invalid bytes are replaced with U+FFFD, the workspace redaction policy is
// applied FIRST (a nil policy redacts nothing), and only then is the result
// truncated to max bytes on a rune boundary. Redacting before truncating keeps
// placeholder growth (e.g. "[redacted]") inside the preview budget instead of
// inflating the envelope past the binding cap.
func evidencePreview(raw []byte, max int) string {
	text := redact.Text(strings.ToValidUTF8(string(raw), "\uFFFD"))
	return truncateRunes(text, max)
}

// truncateRunes truncates s to at most max bytes, cutting only on a rune
// boundary so the result stays valid UTF-8. s is expected to be valid UTF-8
// (evidencePreview sanitizes and redacts before truncating), so the only
// invalid tail is a rune cut in half by the byte limit: DecodeLastRuneInString
// reports RuneError with size 1 for that incomplete trailing rune, which is
// then dropped (a genuine U+FFFD rune at the cut reports size 3 and is kept).
func truncateRunes(s string, max int) string {
	if max < 0 || len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-size]
	}
	return s
}
