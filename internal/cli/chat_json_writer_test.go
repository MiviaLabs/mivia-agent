package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestNDJSONChunkWriterReassemblesSplitMultiByteRune pins the correctness
// requirement this writer exists for: a multi-byte UTF-8 character split
// across two separate Write() calls - exactly what agent.Loop's FinalWriter
// does with streamed content deltas - must reassemble into one valid chunk
// event with the original text intact, not two mangled halves.
func TestNDJSONChunkWriterReassemblesSplitMultiByteRune(t *testing.T) {
	// "café 🎉" - a 2-byte rune (é, U+00E9 = 0xC3 0xA9) and a 4-byte rune
	// (🎉, U+1F389) both straddle Write boundaries below.
	const want = "café 🎉"
	full := []byte(want)

	// Split mid-é (after the 0xC3 lead byte) and mid-🎉 (after its first
	// byte) by writing one byte at a time across the whole string - the
	// worst case for a naive per-Write json.Marshal approach.
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	for i := range full {
		if _, err := w.Write(full[i : i+1]); err != nil {
			t.Fatalf("Write byte %d: %v", i, err)
		}
	}
	w.Flush()

	lines := splitNonEmptyLines(buf.String())
	if len(lines) == 0 {
		t.Fatal("no chunk lines emitted")
	}
	var reconstructed strings.Builder
	for _, line := range lines {
		var ev ndjsonEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		if ev.Type != "chunk" {
			t.Fatalf("line %q: type = %q, want %q", line, ev.Type, "chunk")
		}
		if strings.ContainsRune(ev.Text, '�') {
			t.Fatalf("line %q: chunk text contains U+FFFD (mangled UTF-8): %q", line, ev.Text)
		}
		reconstructed.WriteString(ev.Text)
	}
	if got := reconstructed.String(); got != want {
		t.Fatalf("reconstructed text = %q, want %q", got, want)
	}
}

// TestNDJSONChunkWriterHoldsBackIncompleteRuneUntilComplete verifies the
// buffering behavior directly: writing only the lead byte of a multi-byte
// rune must not emit anything until the rest of the rune arrives.
func TestNDJSONChunkWriterHoldsBackIncompleteRuneUntilComplete(t *testing.T) {
	full := []byte("é") // 0xC3 0xA9
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)

	if _, err := w.Write(full[:1]); err != nil {
		t.Fatalf("Write lead byte: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("emitted output after only a lead byte: %q", buf.String())
	}

	if _, err := w.Write(full[1:]); err != nil {
		t.Fatalf("Write trailing byte: %v", err)
	}
	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	var ev ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if ev.Text != "é" {
		t.Fatalf("text = %q, want %q", ev.Text, "é")
	}
}

// TestNDJSONChunkWriterDiscardDropsBufferedBytes pins the cancellation
// contract: bytes held back as a possibly-incomplete trailing rune must never
// surface as a phantom chunk once the turn that produced them is discarded.
func TestNDJSONChunkWriterDiscardDropsBufferedBytes(t *testing.T) {
	full := []byte("é")
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	if _, err := w.Write(full[:1]); err != nil {
		t.Fatalf("Write lead byte: %v", err)
	}
	w.Discard()
	w.Flush()
	if buf.Len() != 0 {
		t.Fatalf("Discard did not prevent a later Flush from emitting buffered bytes: %q", buf.String())
	}
}

// TestNDJSONChunkWriterPlainASCIIEmitsImmediately guards the common-case
// fast path: ordinary ASCII text needs no buffering delay.
func TestNDJSONChunkWriterPlainASCIIEmitsImmediately(t *testing.T) {
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	var ev ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if ev.Text != "hello" {
		t.Fatalf("text = %q, want %q", ev.Text, "hello")
	}
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
