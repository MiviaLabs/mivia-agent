package clichat

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

func readOutputPageEnd(content string, offset, limit int) int {
	remaining := len(content) - offset
	end := offset + Min(limit, remaining)
	for end > offset && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return end
}

func readOutputPagePayload(ref string, originalBytes, offset, limit, end int, content string) readOutputPayload {
	hasMore := end < len(content)
	payload := readOutputPayload{
		Status:        "ok",
		Ref:           ref,
		Kind:          "output",
		Bytes:         originalBytes,
		Offset:        offset,
		Limit:         limit,
		ReturnedBytes: end - offset,
		HasMore:       hasMore,
		Truncated:     hasMore,
		ContentIsData: true,
		Note:          readOutputDataNote,
		Content:       content[offset:end],
	}
	if hasMore {
		next := end
		payload.NextOffset = &next
	}
	return payload
}

// marshalPayloadJSON is a test seam: the payload is strings and ints, so
// encoding cannot fail in production, but every caller below still has to
// decide what to do when it does.
var marshalPayloadJSON = json.Marshal

func marshalReadOutputPayload(payload readOutputPayload) (string, error) {
	encoded, err := marshalPayloadJSON(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// jsonEscapedRuneLen reports how many bytes the rune at s[0] contributes to a
// json.Marshal-escaped string (HTML escaping on) and that rune's byte size.
// It mirrors encoding/json's appendString: printable ASCII counts 1, the
// short escapes (\" \\ \b \f \n \r \t) count 2, HTML-significant characters
// (& < >), the remaining control bytes and invalid UTF-8 count as \u00xx or
// \ufffd (6), U+2028/U+2029 count 6, and valid multi-byte runes are copied
// verbatim (their raw byte size).
func jsonEscapedRuneLen(s string) (int, int) {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 1 {
		return 6, 1 // invalid byte -> \ufffd
	}
	if r == '\u2028' || r == '\u2029' {
		return 6, size
	}
	if size > 1 {
		return size, size
	}
	switch s[0] {
	case '"', '\\', '\b', '\f', '\n', '\r', '\t':
		return 2, 1
	case '&', '<', '>':
		return 6, 1
	default:
		if s[0] < 0x20 {
			return 6, 1
		}
		return 1, 1
	}
}

// decimalDigits returns the number of decimal digits in n (n >= 0).
func decimalDigits(n int) int {
	d := 1
	for n >= 10 {
		n /= 10
		d++
	}
	return d
}

// readOutputDigitDelta reports how many bytes the numeric envelope fields
// (returned_bytes, next_offset, has_more, truncated) shift when the page end
// moves from offset to next, measured against the framing probe marshalled
// with end equal to offset. The digit widths are the only per-boundary part
// of the encoded length the escaped-content walk cannot accumulate.
func readOutputDigitDelta(content string, offset, next int) int {
	delta := decimalDigits(next-offset) - 1 // returned_bytes: 0 -> next-offset
	if next < len(content) {
		return delta + decimalDigits(next) - decimalDigits(offset) // next_offset
	}
	// next == len(content): has_more/truncated flip to false and next_offset
	// becomes null. When offset is already at the end, the probe is in the
	// same state and every term below is zero.
	if offset < len(content) {
		return delta + 4 - decimalDigits(offset) + 2
	}
	return 0
}

func (t *readOutputTool) pageResponse(ref string, originalBytes int, offset, limit int, content string) (string, error) {
	pageEnd := readOutputPageEnd(content, offset, limit)
	if pageEnd == offset && offset < len(content) {
		return "", fmt.Errorf("read_output: limit cannot include the next character")
	}
	cap := t.resultLimit()
	empty, err := marshalReadOutputPayload(readOutputPagePayload(ref, originalBytes, offset, limit, offset, content))
	if err != nil {
		return "", err
	}
	if len(empty) > cap {
		return "", fmt.Errorf("read_output: result cap %d is too small for response framing", cap)
	}
	// Single pass over the page: accumulate the escaped JSON length of
	// content[offset:pageEnd] and remember the last rune boundary whose full
	// encoded payload fits under the envelope cap. The old code materialized
	// every boundary into a []int and binary-searched it, re-marshalling the
	// whole payload ~log2(page) times. This walk selects the identical
	// boundary with one marshal (the framing probe) plus one at the end.
	escaped := 0
	end := offset
	for cursor := offset; cursor < pageEnd; {
		esc, size := jsonEscapedRuneLen(content[cursor:])
		next := cursor + size
		escaped += esc
		if len(empty)+escaped+readOutputDigitDelta(content, offset, next) <= cap {
			end = next
		}
		cursor = next
	}
	if end == offset && offset < len(content) {
		return "", fmt.Errorf("read_output: result cap %d cannot include the next character", cap)
	}
	encoded, err := marshalReadOutputPayload(readOutputPagePayload(ref, originalBytes, offset, limit, end, content))
	if err != nil {
		return "", err
	}
	if len(encoded) > cap {
		return "", fmt.Errorf("read_output: result cap %d cannot fit the encoded page", cap)
	}
	return encoded, nil
}
