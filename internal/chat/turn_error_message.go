package chat

import "errors"

// TurnErrorMessage returns a redaction-safe, plain-text description of a failed
// turn, suitable for any surface outside this process.
//
// Provider and tool error text can carry request content verbatim (DC-14:
// external error text may carry request content; see
// .agents/quality/defect-taxonomy.md), so err.Error() is never put on a wire
// as-is. Only a couple of recognized internal sentinel failures get a slightly
// more specific, still content-free message; everything else collapses to one
// generic message, with the real error still available to an operator locally
// (line mode prints it to stderr).
//
// This lives in chat, not in one surface, because there is more than one
// boundary that must not leak: the --json NDJSON writer and internal/hub's
// cross-process wire. It used to exist only in the former, so the hub - the
// boundary that reaches ANOTHER process - was the one serializing raw text.
// A nil error returns "", so a caller can distinguish "no error" from "an
// error it must not describe".
func TurnErrorMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrPersistence):
		return "chat turn failed: could not persist session state"
	case errors.Is(err, ErrStaleOperation), errors.Is(err, ErrStaleAutosave):
		return "chat turn failed: superseded by a newer turn"
	default:
		return "chat turn failed"
	}
}
