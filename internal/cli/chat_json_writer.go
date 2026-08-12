package cli

import (
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// ndjsonEvent is the wire schema for line-mode --json output. Exactly one
// event type is populated per line, and there are exactly four types:
//
//	{"type":"chunk","text":"..."}  - one per emitted piece of streamed content
//	{"type":"done"}                - exactly once, turn completed successfully
//	{"type":"cancelled"}           - exactly once, turn was SIGINT-interrupted
//	{"type":"error","message":"…"} - exactly once, turn failed
//
// A SIGINT-interrupted turn gets its own "cancelled" type rather than being
// folded into "error": a caller that wants to distinguish "the user stopped
// this on purpose" from "this genuinely failed" (e.g. to decide whether to
// surface an error toast) would otherwise have to string-match a message
// field, which is fragile - a bare type check is not.
type ndjsonEvent struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Message string `json:"message,omitempty"`
}

// writeNDJSONEvent marshals ev as one NDJSON line and writes it to w.
func writeNDJSONEvent(w io.Writer, ev ndjsonEvent) {
	line, err := json.Marshal(ev)
	if err != nil {
		// ev is our own control struct (a type tag plus plain strings), so
		// this should never happen; fall back to a minimal event that itself
		// cannot fail to marshal rather than dropping the line silently.
		line, _ = json.Marshal(ndjsonEvent{Type: "error", Message: "internal: failed to encode ndjson event"})
	}
	line = append(line, '\n')
	_, _ = w.Write(line)
}

// ndjsonChunkWriter reframes a stream of raw content-delta Write() calls (the
// FinalWriter contract agent.Loop uses - see agent/loop.go, "content deltas go
// to FinalWriter") as NDJSON chunk events.
//
// The deltas arrive as arbitrary byte slices with no guarantee that a
// multi-byte UTF-8 rune is not split across two consecutive Write calls.
// Marshaling each raw Write independently would let json.Marshal silently
// replace a split rune's dangling bytes with U+FFFD on each side, corrupting
// otherwise-valid text. This writer buffers any incomplete trailing UTF-8
// sequence across calls and only emits a chunk once the buffered bytes are
// confirmed to end on a complete rune boundary.
type ndjsonChunkWriter struct {
	w       io.Writer
	pending []byte
}

func newNDJSONChunkWriter(w io.Writer) *ndjsonChunkWriter {
	return &ndjsonChunkWriter{w: w}
}

// Write buffers p, emits a chunk event for whatever prefix is confirmed
// complete, and holds back any trailing partial rune for the next call. It
// always reports the full length of p as written (and never returns a
// non-nil error from the buffering step itself) so callers that only check
// (n, err) against len(p) - like io.Copy or io.WriteString - see success.
func (n *ndjsonChunkWriter) Write(p []byte) (int, error) {
	written := len(p)
	if len(p) == 0 {
		return written, nil
	}
	n.pending = append(n.pending, p...)
	complete, incomplete := splitTrailingIncompleteRune(n.pending)
	n.pending = incomplete
	if len(complete) > 0 {
		writeNDJSONEvent(n.w, ndjsonEvent{Type: "chunk", Text: string(complete)})
	}
	return written, nil
}

// Flush emits whatever is left in the buffer, complete or not, as a final
// chunk. Called at the end of a successful turn so trailing bytes are never
// silently dropped. Must NOT be called after a cancelled turn - see Discard.
func (n *ndjsonChunkWriter) Flush() {
	if len(n.pending) == 0 {
		return
	}
	pending := n.pending
	n.pending = nil
	writeNDJSONEvent(n.w, ndjsonEvent{Type: "chunk", Text: string(pending)})
}

// Discard drops any buffered, not-yet-emitted bytes without writing them.
// Used on the cancelled/errored-turn path: bytes held back by Write because
// they might have been the start of a split rune were never a complete,
// confirmed chunk, so surfacing them now would fabricate a phantom chunk for
// content the turn never actually finished producing.
func (n *ndjsonChunkWriter) Discard() {
	n.pending = nil
}

// splitTrailingIncompleteRune splits b into a leading portion that is safe to
// emit now and a trailing portion that may be an incomplete UTF-8 sequence
// waiting on more bytes. It scans back at most utf8.UTFMax bytes for the
// start byte of the last rune; if that rune is already complete (or no
// multi-byte start byte is found in range), the whole slice is safe to emit.
func splitTrailingIncompleteRune(b []byte) (complete, incomplete []byte) {
	n := len(b)
	if n == 0 {
		return b, nil
	}
	limit := n - utf8.UTFMax
	start := n - 1
	for start >= 0 && start >= limit && !utf8.RuneStart(b[start]) {
		start--
	}
	if start < 0 || start < limit {
		// No rune-start byte within the lookback window: either the tail is
		// all ASCII (handled above via RuneStart on the very last byte in the
		// common case) or the bytes are not valid UTF-8 continuation data at
		// all. Either way there is nothing identifiable to hold back.
		return b, nil
	}
	if utf8.FullRune(b[start:]) {
		return b, nil
	}
	return b[:start], b[start:]
}

// jsonTurnErrorMessage returns a redaction-safe, plain-text description of a
// failed --json turn for the wire ("error" event's message field). Provider
// and tool error text can carry request content verbatim (DC-14: external
// error text may carry request content; see .mivia/quality/defect-taxonomy.md),
// so err.Error() is never put on the wire as-is. Only a couple of recognized
// internal sentinel failures get a slightly more specific, still content-free
// message; everything else collapses to one generic message, with the real
// error still available to an operator via stderr (sendLineMode's caller
// prints it there).
func jsonTurnErrorMessage(err error) string {
	switch {
	case errors.Is(err, chat.ErrPersistence):
		return "chat turn failed: could not persist session state"
	case errors.Is(err, chat.ErrStaleOperation), errors.Is(err, chat.ErrStaleAutosave):
		return "chat turn failed: superseded by a newer turn"
	default:
		return "chat turn failed"
	}
}
