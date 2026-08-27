package cliorchestrate

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// synopsisMaxBytes is the maximum size of a synopsis string.
const synopsisMaxBytes = 512

// Synopsize produces a bounded, injection-inert preview of body. It is NOT a
// summary — it is enough context for the parent to decide whether and which
// range to page via ledger_read.
//
// Rules:
//   - If body is valid JSON with top-level object keys, emit a key inventory
//     ("{\"keys\":[...],\"bytes\":N}") truncated to synopsisMaxBytes.
//   - Otherwise, take the first min(synopsisMaxBytes, len(body)) bytes, cut at
//     a UTF-8 rune boundary (never mid-rune), and append '…' if truncated.
func Synopsize(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	// Try the JSON key inventory first — but only for top-level objects.
	if synopsis, ok := JsonKeySynopsis(body); ok {
		return synopsis
	}

	return TruncateAtRuneBoundary(body)
}

// TruncateAtRuneBoundary takes the first min(synopsisMaxBytes, len(body)) bytes
// of body, ensuring the cut lands on a UTF-8 boundary, and appends '…' if
// the result was truncated.
func TruncateAtRuneBoundary(body []byte) string {
	max := synopsisMaxBytes
	if len(body) <= max {
		return string(body)
	}
	// Find a safe cut point not past max. A UTF-8 rune is at most utf8.UTFMax
	// bytes, so this backs up at most UTFMax-1 positions and cannot reach 0
	// while synopsisMaxBytes >= UTFMax - the property
	// TestSynopsisMaxBytesLeavesRoomForARune pins, so the result is never the
	// empty string.
	end := max
	for end > 0 && !utf8.RuneStart(body[end]) {
		end--
	}
	return string(body[:end]) + "…"
}

// JsonKeySynopsis extracts top-level keys from a JSON object. Returns the
// synopsis string and true on success; returns ("", false) for non-object JSON
// or unparseable input.
func JsonKeySynopsis(body []byte) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Read opening bracket/brace.
	t, err := decoder.Token()
	if err != nil || t != json.Delim('{') {
		return "", false
	}

	var keys []string
	for decoder.More() {
		t, err = decoder.Token()
		if err != nil {
			return "", false
		}
		// Each value token position must be consumed so More() can advance.
		if key, ok := t.(string); ok {
			keys = append(keys, key)
		}
		// Consume the value (skip its tokens).
		if err := SkipValue(decoder); err != nil {
			return "", false
		}
	}

	// A map of a string slice and an int always marshals - no channel, func or
	// NaN can reach here - so there is no error case to branch on.
	out, _ := json.Marshal(map[string]any{
		"keys":  keys,
		"bytes": len(body),
	})

	// Ensure the synopsis itself stays bounded.
	s := string(out)
	if len(s) > synopsisMaxBytes {
		return TruncateAtRuneBoundary([]byte(s)), true
	}
	return s, true
}

// SkipValue advances the decoder past one JSON value (object, array, string,
// number, boolean, or null).
func SkipValue(decoder *json.Decoder) error {
	t, err := decoder.Token()
	if err != nil {
		return err
	}
	switch v := t.(type) {
	case json.Delim:
		if v == '{' || v == '[' {
			for decoder.More() {
				if err := SkipValue(decoder); err != nil {
					return err
				}
			}
			// Consume closing delimiter.
			_, err := decoder.Token()
			return err
		}
	}
	return nil
}

// BelowInlineThreshold reports whether body should be inlined. When body is
// below threshold, it is returned inline with no synopsis. When above
// threshold and a ref is available, the caller should emit synopsis+ref
// instead. When above threshold but no ref is available (INV-AG-10: content
// write failed), the body must still be inlined to avoid losing data.
func BelowInlineThreshold(body []byte, threshold int, ref string) bool {
	if threshold <= 0 {
		// threshold == 0 means "always use refs"; but only if a ref exists.
		return ref == ""
	}
	if len(body) <= threshold {
		return true
	}
	// Above threshold but no ref — must inline to avoid losing data.
	return ref == ""
}

// ReadHint returns the hint string for above-threshold results, or empty.
func ReadHint(threshold int, bodyLen int, ref string) string {
	if threshold > 0 && bodyLen > threshold && ref != "" {
		return "use ledger_read with this ref; offset/limit paginate"
	}
	return ""
}

// ModelVisibleOutput returns the handler response as a JSON value when valid,
// otherwise as text. The accompanying reference is a resolvable handle to the
// persisted content, and the actual result is also included inline in this
// response while the completed run is in memory.
func ModelVisibleOutput(raw json.RawMessage) any {
	if json.Valid(raw) {
		return json.RawMessage(raw)
	}
	return string(raw)
}
