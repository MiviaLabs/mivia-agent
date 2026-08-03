package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	defaultLedgerReadMaxBytes    = 32 << 10
	defaultLedgerReadResultBytes = 256 << 10
	minimumLedgerReadLimit       = utf8.UTFMax
)

var errInvalidLedgerReadArguments = errors.New("invalid arguments")

type ledgerReadParams struct {
	Ref      string
	Offset   int
	Limit    int
	HasLimit bool
}

func decodeLedgerReadParams(args json.RawMessage) (ledgerReadParams, error) {
	dec := json.NewDecoder(bytes.NewReader(args))
	token, err := dec.Token()
	if err != nil {
		return ledgerReadParams{}, errInvalidLedgerReadArguments
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return ledgerReadParams{}, errInvalidLedgerReadArguments
	}
	var params ledgerReadParams
	seen := make(map[string]bool, 3)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return ledgerReadParams{}, errInvalidLedgerReadArguments
		}
		name, ok := token.(string)
		if !ok {
			return ledgerReadParams{}, errInvalidLedgerReadArguments
		}
		if seen[name] {
			return ledgerReadParams{}, errInvalidLedgerReadArguments
		}
		seen[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return ledgerReadParams{}, errInvalidLedgerReadArguments
		}
		switch name {
		case "ref":
			if string(value) == "null" || json.Unmarshal(value, &params.Ref) != nil {
				return ledgerReadParams{}, errInvalidLedgerReadArguments
			}
		case "offset":
			if string(value) == "null" || json.Unmarshal(value, &params.Offset) != nil {
				return ledgerReadParams{}, errInvalidLedgerReadArguments
			}
			if params.Offset < 0 {
				return ledgerReadParams{}, errInvalidLedgerReadArguments
			}
		case "limit":
			if string(value) == "null" || json.Unmarshal(value, &params.Limit) != nil {
				return ledgerReadParams{}, errInvalidLedgerReadArguments
			}
			if params.Limit < minimumLedgerReadLimit {
				return ledgerReadParams{}, errInvalidLedgerReadArguments
			}
			params.HasLimit = true
		default:
			return ledgerReadParams{}, errInvalidLedgerReadArguments
		}
	}
	if _, err := dec.Token(); err != nil {
		return ledgerReadParams{}, errInvalidLedgerReadArguments
	}
	if _, err := dec.Token(); err != io.EOF {
		return ledgerReadParams{}, errInvalidLedgerReadArguments
	}
	return params, nil
}

func (t *ledgerReadTool) pageLimit() int {
	if t.maxBytes > 0 && t.maxBytes < defaultLedgerReadMaxBytes {
		return t.maxBytes
	}
	return defaultLedgerReadMaxBytes
}

func (t *ledgerReadTool) resultLimit() int {
	if t.resultCapBytes > 0 && t.resultCapBytes < defaultLedgerReadResultBytes {
		return t.resultCapBytes
	}
	return defaultLedgerReadResultBytes
}

func normalizeLedgerContent(data []byte) string {
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

func ledgerPageEnd(content string, offset, limit int) int {
	remaining := len(content) - offset
	end := offset + min(limit, remaining)
	for end > offset && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return end
}

func ledgerReadPagePayload(ref, kind string, originalBytes, offset, limit, end int, content string) ledgerReadPayload {
	hasMore := end < len(content)
	payload := ledgerReadPayload{
		Status:        "ok",
		Ref:           ref,
		Kind:          kind,
		Bytes:         originalBytes,
		Offset:        offset,
		Limit:         limit,
		ReturnedBytes: end - offset,
		HasMore:       hasMore,
		Truncated:     hasMore,
		ContentIsData: true,
		Note:          contentIsDataNote,
		Content:       content[offset:end],
	}
	if hasMore {
		next := end
		payload.NextOffset = &next
	}
	return payload
}

func marshalLedgerReadPayload(payload ledgerReadPayload) (string, error) {
	encoded, err := marshalPayloadJSON(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ledgerDigitDelta is the ledger_read mirror of readOutputDigitDelta: it
// reports how many bytes the numeric envelope fields (returned_bytes,
// next_offset, has_more, truncated) shift when the page end moves from offset
// to next, measured against the framing probe marshalled with end equal to
// offset. The digit widths are the only per-boundary part of the encoded
// length the escaped-content walk cannot accumulate.
func ledgerDigitDelta(content string, offset, next int) int {
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

func (t *ledgerReadTool) pageResponse(ref, kind string, originalBytes int, offset, limit int, content string) (string, error) {
	pageEnd := ledgerPageEnd(content, offset, limit)
	if pageEnd == offset && offset < len(content) {
		return "", fmt.Errorf("ledger_read: limit cannot include the next character")
	}
	cap := t.resultLimit()
	empty, err := marshalLedgerReadPayload(ledgerReadPagePayload(ref, kind, originalBytes, offset, limit, offset, content))
	if err != nil {
		return "", err
	}
	if len(empty) > cap {
		return "", fmt.Errorf("ledger_read: result cap %d is too small for response framing", cap)
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
		if len(empty)+escaped+ledgerDigitDelta(content, offset, next) <= cap {
			end = next
		}
		cursor = next
	}
	if end == offset && offset < len(content) {
		return "", fmt.Errorf("ledger_read: result cap %d cannot include the next character", cap)
	}
	encoded, err := marshalLedgerReadPayload(ledgerReadPagePayload(ref, kind, originalBytes, offset, limit, end, content))
	if err != nil {
		return "", err
	}
	if len(encoded) > cap {
		return "", fmt.Errorf("ledger_read: result cap %d cannot fit the encoded page", cap)
	}
	return encoded, nil
}
