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

func ledgerPageBoundaries(content string, offset, limit int) []int {
	end := ledgerPageEnd(content, offset, limit)
	boundaries := []int{offset}
	for cursor := offset; cursor < end; {
		_, size := utf8.DecodeRuneInString(content[cursor:])
		cursor += size
		boundaries = append(boundaries, cursor)
	}
	return boundaries
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
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (t *ledgerReadTool) pageResponse(ref, kind string, originalBytes int, offset, limit int, content string) (string, error) {
	boundaries := ledgerPageBoundaries(content, offset, limit)
	end := boundaries[len(boundaries)-1]
	if end == offset && offset < len(content) {
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
	low, high := 0, len(boundaries)-1
	for low < high {
		mid := low + (high-low+1)/2
		candidate := boundaries[mid]
		encoded, err := marshalLedgerReadPayload(ledgerReadPagePayload(ref, kind, originalBytes, offset, limit, candidate, content))
		if err != nil {
			return "", err
		}
		if len(encoded) <= cap {
			low = mid
		} else {
			high = mid - 1
		}
	}
	end = boundaries[low]
	if end == offset && offset < len(content) {
		return "", fmt.Errorf("ledger_read: result cap %d cannot include the next character", cap)
	}
	return marshalLedgerReadPayload(ledgerReadPagePayload(ref, kind, originalBytes, offset, limit, end, content))
}
