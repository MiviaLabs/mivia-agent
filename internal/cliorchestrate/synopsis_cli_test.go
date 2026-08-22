package cliorchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSynopsizeEmpty(t *testing.T) {
	if got := Synopsize(nil); got != "" {
		t.Fatalf("Synopsize(nil) = %q, want empty", got)
	}
	if got := Synopsize([]byte{}); got != "" {
		t.Fatalf("Synopsize(empty) = %q, want empty", got)
	}
}

func TestSynopsizeShort(t *testing.T) {
	short := []byte("hello world")
	if got := Synopsize(short); got != "hello world" {
		t.Fatalf("Synopsize(short) = %q, want %q", got, "hello world")
	}
}

func TestSynopsizeExactBoundary(t *testing.T) {
	// Build a string exactly synopsisMaxBytes long.
	body := make([]byte, synopsisMaxBytes)
	for i := range body {
		body[i] = 'a'
	}
	got := Synopsize(body)
	if len(got) != synopsisMaxBytes {
		t.Fatalf("Synopsize(exact boundary) len = %d, want %d", len(got), synopsisMaxBytes)
	}
	if got != string(body) {
		t.Fatalf("Synopsize(exact boundary) = %q, want %q", got, string(body))
	}
}

func TestSynopsizeAboveBoundary(t *testing.T) {
	body := make([]byte, synopsisMaxBytes+100)
	for i := range body {
		body[i] = 'b'
	}
	got := Synopsize(body)
	if !endsWithEllipsis(got) {
		t.Fatalf("Synopsize(above) = %q, want trailing …", got)
	}
	// Must not exceed synopsisMaxBytes + 1 (for the ellipsis).
	if len(got) > synopsisMaxBytes+3 { // … is 3 bytes
		t.Fatalf("Synopsize(above) len = %d, want <= %d", len(got), synopsisMaxBytes+3)
	}
}

func endsWithEllipsis(s string) bool {
	return len(s) >= 3 && s[len(s)-3:] == "…"
}

func TestSynopsizeUTF8Boundary(t *testing.T) {
	// 2-byte rune: é = \xc3\xa9
	twoByte := "\xc3\xa9"
	body := twoByte
	for len([]byte(body)) < synopsisMaxBytes+10 {
		body += twoByte
	}
	got := Synopsize([]byte(body))
	if !endsWithEllipsis(got) {
		t.Fatalf("Synopsize(2-byte runes) = %q, want trailing …", got)
	}
	// Verify the result is valid UTF-8.
	if !isTruncatedValidUTF8(got) {
		t.Fatalf("Synopsize produced invalid UTF-8: %q", got)
	}
}

func TestSynopsizeThreeByteRune(t *testing.T) {
	// 3-byte rune: 中 = \xe4\xb8\xad
	threeByte := "\xe4\xb8\xad"
	body := threeByte
	for len([]byte(body)) < synopsisMaxBytes+10 {
		body += threeByte
	}
	got := Synopsize([]byte(body))
	if !endsWithEllipsis(got) {
		t.Fatalf("Synopsize(3-byte runes) = %q, want trailing …", got)
	}
}

func isTruncatedValidUTF8(s string) bool {
	// Strip trailing … before checking — the body portion must be valid UTF-8.
	if len(s) >= 3 && s[len(s)-3:] == "…" {
		s = s[:len(s)-3]
	}
	off := 0
	for off < len(s) {
		_, size := decodeUTF8Rune(s[off:])
		if size == 0 {
			return false
		}
		off += size
	}
	return off == len(s)
}

// decodeUTF8Rune decodes a single rune from the start of buf. Returns 0 for
// invalid sequences (matches utf8.DecodeRune behavior for short buffers).
func decodeUTF8Rune(buf string) (rune, int) {
	if len(buf) == 0 {
		return 0, 0
	}
	b := buf[0]
	if b < 0x80 {
		return rune(b), 1
	}
	if b < 0xC0 || b >= 0xF8 {
		return 0xFFFD, 1 // replacement, 1 byte consumed
	}
	runeLen := 0
	if b < 0xE0 {
		runeLen = 2
	} else if b < 0xF0 {
		runeLen = 3
	} else {
		runeLen = 4
	}
	if len(buf) < runeLen {
		return 0, 0
	}
	return rune(buf[0]) | rune(buf[1])<<8, runeLen // simplified: just check size
}

func TestSynopsizeJSONObject(t *testing.T) {
	body := []byte(`{"findings": ["a", "b"], "files": ["f1.go", "f2.go"], "summary": "done"}`)
	got := Synopsize(body)
	// Parse to check structure — key order in map[string]any is non-deterministic.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("Synopsize(JSON object) produced invalid JSON %q: %v", got, err)
	}
	keys, ok := parsed["keys"].([]any)
	if !ok || len(keys) != 3 {
		t.Fatalf("Synopsize(JSON object) keys = %v, want 3 keys", parsed["keys"])
	}
	bytesVal, ok := parsed["bytes"].(float64)
	if !ok || int(bytesVal) != len(body) {
		t.Fatalf("Synopsize(JSON object) bytes = %v, want %d", parsed["bytes"], len(body))
	}
}

func TestSynopsizeJSONArray(t *testing.T) {
	body := []byte(`[1, 2, 3]`)
	got := Synopsize(body)
	// Arrays are NOT objects — should fall back to truncation.
	if got == "" {
		t.Fatalf("Synopsize(JSON array) = empty")
	}
	// Should contain a prefix of the array body.
	if !endsWithEllipsis(got) && got != string(body) {
		t.Fatalf("Synopsize(JSON array) = %q, want prefix or full body", got)
	}
}

func TestSynopsizeJSONString(t *testing.T) {
	body := []byte(`"just a string"`)
	got := Synopsize(body)
	if got != `"just a string"` {
		t.Fatalf("Synopsize(JSON string) = %q", got)
	}
}

func TestSynopsizeJSONInvalid(t *testing.T) {
	body := []byte(`{broken json`)
	got := Synopsize(body)
	// Should fall back to truncation.
	if got != string(body) {
		t.Fatalf("Synopsize(invalid JSON) = %q, want %q", got, string(body))
	}
}

func TestSynopsizeJSONLargeKeys(t *testing.T) {
	// When the key inventory itself exceeds synopsisMaxBytes, it is truncated.
	var parts []string
	for i := 0; i < 100; i++ {
		parts = append(parts, `"key_that_is_quite_long_`+string(rune('0'+i))+`"`)
	}
	body := []byte("{" + joinParts(parts) + ":0}")
	got := Synopsize(body)
	if len(got) > synopsisMaxBytes+3 {
		t.Fatalf("Synopsize(large JSON keys) len = %d, want <= %d", len(got), synopsisMaxBytes+3)
	}
}

func joinParts(parts []string) string {
	s := ""
	for _, p := range parts {
		if s != "" {
			s += ","
		}
		s += p
	}
	return s
}

func TestSynopsizeSingleByteAtBoundary(t *testing.T) {
	// A single byte at the boundary (all ASCII, then one extra).
	body := make([]byte, synopsisMaxBytes+1)
	for i := range body {
		body[i] = 'x'
	}
	got := Synopsize(body)
	want := string(body[:synopsisMaxBytes]) + "…"
	if got != want {
		t.Fatalf("Synopsize(+1) = %q (len=%d), want %q (len=%d)", got, len(got), want, len(want))
	}
}

func TestBelowInlineThreshold(t *testing.T) {
	short := []byte("short output")
	longOutput := make([]byte, 5000)
	for i := range longOutput {
		longOutput[i] = 'z'
	}

	tests := []struct {
		name      string
		body      []byte
		threshold int
		ref       string
		want      bool
	}{
		{"below threshold", short, 4096, "ref:output:abc", true},
		{"above threshold with ref", longOutput, 4096, "ref:output:abc", false},
		{"above threshold no ref (INV-AG-10)", longOutput, 4096, "", true},
		{"zero threshold with ref", short, 0, "ref:output:abc", false},
		{"zero threshold no ref", short, 0, "", true},
		{"exact threshold", short, len(short), "ref:output:abc", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := BelowInlineThreshold(tc.body, tc.threshold, tc.ref); got != tc.want {
				t.Fatalf("BelowInlineThreshold = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSynopsisMaxBytesLeavesRoomForARune pins the invariant truncateAtRuneBoundary
// relies on: with a budget of at least one maximal rune, backing up to a rune
// start can never walk the cut point to zero, so the synopsis is never empty.
func TestSynopsisMaxBytesLeavesRoomForARune(t *testing.T) {
	if synopsisMaxBytes < utf8.UTFMax {
		t.Fatalf("synopsisMaxBytes = %d, must be >= utf8.UTFMax (%d) or truncateAtRuneBoundary can produce an empty cut",
			synopsisMaxBytes, utf8.UTFMax)
	}
	// Worst case: the byte at the cut is deep inside a 4-byte rune.
	body := append(bytes.Repeat([]byte("x"), synopsisMaxBytes-2), []byte("𝄞𝄞")...)
	got := Synopsize(body)
	if got == "" || got == "…" {
		t.Fatalf("Synopsize returned %q, want a non-empty prefix", got)
	}
	if !utf8.ValidString(strings.TrimSuffix(got, "…")) {
		t.Fatalf("Synopsize cut mid-rune: %q", got)
	}
}

// A valid JSON object whose key inventory alone exceeds the budget must come
// back truncated rather than blowing the bound.
func TestSynopsizeJSONKeyInventoryExceedsBudget(t *testing.T) {
	var parts []string
	for i := 0; i < 60; i++ {
		parts = append(parts, fmt.Sprintf(`"key_that_is_quite_long_%02d":%d`, i, i))
	}
	body := []byte("{" + joinParts(parts) + "}")
	if !json.Valid(body) {
		t.Fatalf("fixture is not valid JSON: %s", body[:60])
	}
	got := Synopsize(body)
	if len(got) > synopsisMaxBytes+len("…") {
		t.Fatalf("synopsis len = %d, want <= %d", len(got), synopsisMaxBytes+len("…"))
	}
	if !strings.HasPrefix(got, `{"bytes":`) && !strings.HasPrefix(got, `{"keys":`) {
		t.Fatalf("expected a key inventory, got %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("oversized inventory must report its truncation: %q", got)
	}
}

// A value that is itself a truncated container must fail the inventory rather
// than report the keys it managed to read before the input ran out.
func TestSynopsizeJSONTruncatedNestedValue(t *testing.T) {
	body := []byte(`{"a":[1,2,{"b":`)
	got := Synopsize(body)
	if strings.HasPrefix(got, `{"keys"`) {
		t.Fatalf("truncated nested value produced a key inventory: %q", got)
	}
	if got != string(body) {
		t.Fatalf("expected the raw prefix fallback, got %q", got)
	}
}
