package clichat

// chat_json_chunk_writer_edges_test.go covers the degenerate inputs of the
// NDJSON chunk writer: an empty Write, a Flush with buffered bytes, and the
// two splitTrailingIncompleteRune paths that have nothing identifiable to
// hold back.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestNDJSONChunkWriterEmptyWriteEmitsNothing pins that a zero-length delta
// reports success without emitting a chunk event. An empty {"type":"chunk"}
// line would tell a consumer the model produced text it never produced.
func TestNDJSONChunkWriterEmptyWriteEmitsNothing(t *testing.T) {
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	n, err := w.Write(nil)
	if n != 0 || err != nil {
		t.Fatalf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	n, err = w.Write([]byte{})
	if n != 0 || err != nil {
		t.Fatalf("Write(empty) = (%d, %v), want (0, nil)", n, err)
	}
	if buf.Len() != 0 {
		t.Fatalf("empty Write emitted %q, want no output", buf.String())
	}
	// A non-empty write on the same writer does emit, so the silence above is
	// the empty-input branch and not a dead writer.
	if _, err := w.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, `"text":"hi"`) {
		t.Fatalf("output = %q, want a chunk carrying \"hi\"", got)
	}
}

// TestNDJSONChunkWriterFlushEmitsHeldBackBytes pins the end-of-turn contract:
// bytes Write held back as a possibly-incomplete rune are emitted by Flush, so
// a truncated trailing sequence is surfaced rather than silently dropped. The
// buffer is cleared, so a second Flush emits nothing.
func TestNDJSONChunkWriterFlushEmitsHeldBackBytes(t *testing.T) {
	var buf bytes.Buffer
	w := newNDJSONChunkWriter(&buf)
	// 0xE2 is the lead byte of a 3-byte rune: Write must hold it back.
	if _, err := w.Write([]byte{'o', 'k', 0xE2}); err != nil {
		t.Fatal(err)
	}
	first := buf.String()
	if !strings.Contains(first, `"text":"ok"`) {
		t.Fatalf("after Write, output = %q, want only the complete prefix \"ok\"", first)
	}
	before := len(splitNonEmptyLines(first))

	w.Flush()
	lines := splitNonEmptyLines(buf.String())
	if len(lines) != before+1 {
		t.Fatalf("Flush emitted %d extra lines, want 1 (all: %q)", len(lines)-before, buf.String())
	}
	var ev ndjsonEvent
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &ev); err != nil {
		t.Fatalf("flush line %q is not JSON: %v", lines[len(lines)-1], err)
	}
	if ev.Type != "chunk" || ev.Text == "" {
		t.Fatalf("flush event = %+v, want a non-empty chunk carrying the held-back byte", ev)
	}

	// The buffer is now empty: a second Flush must add nothing.
	n := len(splitNonEmptyLines(buf.String()))
	w.Flush()
	if got := len(splitNonEmptyLines(buf.String())); got != n {
		t.Fatalf("second Flush emitted %d extra lines, want 0", got-n)
	}

	// Discard is the opposite contract: held-back bytes are dropped.
	var dbuf bytes.Buffer
	d := newNDJSONChunkWriter(&dbuf)
	if _, err := d.Write([]byte{0xE2}); err != nil {
		t.Fatal(err)
	}
	d.Discard()
	d.Flush()
	if dbuf.Len() != 0 {
		t.Fatalf("Discard then Flush emitted %q, want nothing", dbuf.String())
	}
}

// TestSplitTrailingIncompleteRuneNothingToHoldBack pins the two inputs the
// splitter must pass through whole: an empty slice, and a tail with no
// rune-start byte inside the utf8.UTFMax lookback window (stray continuation
// bytes). Holding those back would strand bytes in the buffer forever.
func TestSplitTrailingIncompleteRuneNothingToHoldBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{"nil", nil},
		{"empty non-nil slice", []byte{}},
		{"single stray continuation byte", []byte{0x80}},
		{"all continuation bytes past the lookback window", []byte{0x80, 0x80, 0x80, 0x80, 0x80}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			complete, incomplete := splitTrailingIncompleteRune(tc.in)
			if !bytes.Equal(complete, tc.in) {
				t.Fatalf("complete = %v, want the whole input %v", complete, tc.in)
			}
			if incomplete != nil {
				t.Fatalf("incomplete = %v, want nil (nothing identifiable to hold back)", incomplete)
			}
		})
	}
	// A genuine truncated lead byte IS held back, so the pass-through above is
	// a real branch.
	complete, incomplete := splitTrailingIncompleteRune([]byte{'a', 0xE2, 0x82})
	if string(complete) != "a" {
		t.Fatalf("complete = %q, want %q", complete, "a")
	}
	if !bytes.Equal(incomplete, []byte{0xE2, 0x82}) {
		t.Fatalf("incomplete = %v, want the truncated lead bytes", incomplete)
	}
}
