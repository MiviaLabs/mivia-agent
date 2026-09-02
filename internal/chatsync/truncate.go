package chatsync

import (
	"strings"
	"unicode/utf8"
)

const (
	BudgetPromptText    = 32 * 1024
	BudgetAssistantText = 32 * 1024
	BudgetToolOutput    = 16 * 1024
	BudgetDeltaText     = 8 * 1024
	BudgetToolInput     = 4 * 1024
	BudgetErrorMessage  = 2 * 1024
	BudgetShortField    = 200
)

// jsonEscapedLen reports how many bytes r occupies once JSON-encoded.
//
// This is the unit the STORE counts in. Postgres bounds the payload with
// octet_length(payload::text), and the receiving DTO measures
// Buffer.byteLength(JSON.stringify(...)) - both of which see the escaped form.
// A control byte costs 6 bytes there ("\u0007") and one byte here, so a field
// measured raw can be six times its budget by the time it is stored. That is
// not a corner case: it is the same content class that produced the NUL
// incident - a tool reading a binary file, where most bytes are control bytes.
func jsonEscapedLen(r rune) int {
	switch r {
	case '"', '\\', '\n', '\r', '\t':
		return 2
	}
	if r < 0x20 {
		return 6 // \u00XX
	}
	return utf8.RuneLen(r)
}

// escapedLen reports the JSON-encoded size of s, excluding its quotes.
func escapedLen(s string) int {
	n := 0
	for _, r := range s {
		n += jsonEscapedLen(r)
	}
	return n
}

// truncateString cuts s to fit within maxBytes on rune boundaries.
//
// maxBytes is a budget in ESCAPED bytes - what the store counts - so a field
// inside its budget here is inside it there. Measuring raw bytes let a 16 KiB
// tool output become 96 KiB stored, over a 64 KiB column bound, and a column
// rejection fails the whole batch it travels in rather than the one event.
//
// It returns the kept string, its escaped size, the original escaped size, and
// whether anything was cut. The two counts are what the truncation record
// reports, so they describe the same unit the budget does.
func truncateString(s string, maxBytes int) (string, int, int, bool) {
	totalBytes := escapedLen(s)
	if totalBytes <= maxBytes {
		return s, totalBytes, totalBytes, false
	}
	if maxBytes <= 0 {
		return "", 0, totalBytes, true
	}

	// Cut on a rune boundary at the point the ESCAPED budget runs out. A byte
	// index into s would be the wrong unit twice: once for multi-byte runes,
	// and once for runes that escape to more bytes than they occupy.
	spent := 0
	for i, r := range s {
		cost := jsonEscapedLen(r)
		if spent+cost > maxBytes {
			cut := s[:i]
			return cut, spent, totalBytes, true
		}
		spent += cost
	}
	return s, spent, totalBytes, false
}

func applyTruncation(env *Envelope, fieldName, value string, maxBytes int) string {
	// Sanitize BEFORE measuring, so the truncation record describes the bytes
	// that were actually sent rather than bytes that were removed on the way.
	keptStr, keptLen, totalLen, truncated := truncateString(sanitizeWireText(value), maxBytes)
	if truncated {
		if env.Trunc == nil {
			env.Trunc = &Truncation{Fields: make(map[string]TruncField)}
		}
		env.Trunc.Fields[fieldName] = TruncField{
			Kept:  keptLen,
			Total: totalLen,
		}
	}
	return keptStr
}

// sanitizeWireText removes code points the receiving store cannot hold.
//
// U+0000 is a legal Go rune and legal JSON, and Postgres cannot put it in a
// json or jsonb column: "unsupported Unicode escape sequence - \u0000 cannot
// be converted to text". A tool that reads a binary file produces one easily -
// the report that led here was a build cache's .sst file reaching a tool
// output preview.
//
// The cost is not one lost event. The API inserts a whole batch as ONE
// multi-row statement inside a transaction, so a single NUL anywhere in the
// batch rejects every event in it, and the ninety-nine good events go down
// with the one bad one.
//
// Removal, not replacement: a NUL carries nothing a reader could see, so a
// substitute character would add a mark that was never in the content. Every
// free-text field on this wire is routed through applyTruncation, which is why
// this is applied there and not at each of its twenty-six call sites.
func sanitizeWireText(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}
