package clichat

// paging_diffcoverage_test.go fills the uncovered branch/edge coverage of the
// single-pass read_output/ledger_read paging walk: the jsonEscapedRuneLen
// escape branches, the numeric-envelope digit-width transitions (9->10 and
// 99->100), the offset-at-content-end delta term, and the defensive refusals
// behind the marshal seams.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestReadOutputPagingJSONEscapedRuneLenCoversEscapeBranches pins the
// json.Marshal escaping mirror the walk uses: invalid bytes, U+2028/U+2029,
// and the HTML-significant characters each contribute exactly what
// encoding/json emits, so the single-pass length estimate matches the real
// envelope.
func TestReadOutputPagingJSONEscapedRuneLenCoversEscapeBranches(t *testing.T) {
	cases := []struct {
		in       string
		wantEsc  int
		wantSize int
	}{
		{"\xff", 6, 1},   // invalid byte -> \ufffd
		{"\u2028", 6, 3}, // JSON line separator escape
		{"\u2029", 6, 3}, // JSON paragraph separator escape
		{"&", 6, 1},      // HTML-significant -> \u0026
		{"<", 6, 1},      // -> \u003c
		{">", 6, 1},      // -> \u003e
	}
	for _, c := range cases {
		esc, size := jsonEscapedRuneLen(c.in)
		if esc != c.wantEsc || size != c.wantSize {
			t.Errorf("jsonEscapedRuneLen(%q) = (%d, %d), want (%d, %d)",
				c.in, esc, size, c.wantEsc, c.wantSize)
		}
	}
}

// TestReadOutputLedgerReadPagingDigitDeltaWidthTransitions pins the
// numeric-envelope width shifts the walk must fold into its estimate:
// returned_bytes and next_offset grow digits at 9->10 and 99->100, the
// terminal page flips has_more/truncated and nulls next_offset, and an offset
// already at the content end moves nothing.
func TestReadOutputLedgerReadPagingDigitDeltaWidthTransitions(t *testing.T) {
	mid := strings.Repeat("x", 1000) // next stays inside the content
	flip := strings.Repeat("x", 100) // next == len(content) flips the tail

	cases := []struct {
		content string
		offset  int
		next    int
		want    int
	}{
		{mid, 0, 9, 0},
		{mid, 0, 10, 2}, // returned_bytes and next_offset both 9->10
		{mid, 0, 99, 2},
		{mid, 0, 100, 4},  // both 99->100
		{mid, 9, 10, 1},   // next_offset only 9->10
		{mid, 99, 100, 1}, // next_offset only 99->100
		{flip, 0, 100, 7}, // terminal flip: has_more/truncated + null
		{flip, 99, 100, 4},
		{mid, 1000, 1000, 0}, // offset already at the end
		{"", 0, 0, 0},        // empty content
	}
	for _, c := range cases {
		if got := readOutputDigitDelta(c.content, c.offset, c.next); got != c.want {
			t.Errorf("readOutputDigitDelta(offset=%d, next=%d) = %d, want %d",
				c.offset, c.next, got, c.want)
		}
		if got := ledgerDigitDelta(c.content, c.offset, c.next); got != c.want {
			t.Errorf("ledgerDigitDelta(offset=%d, next=%d) = %d, want %d",
				c.offset, c.next, got, c.want)
		}
	}
}

// TestPageResponseRefusesOverlongEncodedPage makes the defensive
// len(encoded) > cap refusal fire by inflating the final marshal past the
// envelope budget the framing probe and walk agreed on.
func TestPageResponseRefusesOverlongEncodedPage(t *testing.T) {
	content := strings.Repeat("x", 64)
	probe, err := marshalReadOutputPayload(readOutputPagePayload("ref:output:x", len(content), 0, 64, 0, content))
	if err != nil {
		t.Fatal(err)
	}
	budget := len(probe) + 4
	tool := &readOutputTool{resultCapBytes: budget}

	restore := marshalPayloadJSON
	defer func() { marshalPayloadJSON = restore }()

	calls := 0
	marshalPayloadJSON = func(v any) ([]byte, error) {
		calls++
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if calls > 1 { // final page: one byte over the envelope budget
			if pad := budget + 1 - len(raw); pad > 0 {
				raw = append(raw, make([]byte, pad)...)
			}
		}
		return raw, nil
	}
	_, err = tool.pageResponse("ref:output:x", len(content), 0, 64, content)
	if err == nil || !strings.Contains(err.Error(), "cannot fit the encoded page") {
		t.Fatalf("over-long encoded page was not refused: %v", err)
	}
}

// TestLedgerPageResponsePropagatesFinalMarshalFailure makes the second (final
// page) marshal fail so the err path after the final payload marshal
// propagates.
func TestLedgerPageResponsePropagatesFinalMarshalFailure(t *testing.T) {
	content := strings.Repeat("x", 64)
	tool := &ledgerReadTool{}

	restore := marshalPayloadJSON
	defer func() { marshalPayloadJSON = restore }()

	calls := 0
	marshalPayloadJSON = func(v any) ([]byte, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("ledger final marshal failed")
		}
		return json.Marshal(v)
	}
	_, err := tool.pageResponse("ref:x", "ledger", len(content), 0, 64, content)
	if err == nil || !strings.Contains(err.Error(), "ledger final marshal failed") {
		t.Fatalf("final-marshal failure was not propagated: %v", err)
	}
}

// TestLedgerPageResponseRefusesOverlongEncodedPage mirrors the read_output
// refusal: an inflated final marshal trips the len(encoded) > cap check.
func TestLedgerPageResponseRefusesOverlongEncodedPage(t *testing.T) {
	content := strings.Repeat("x", 64)
	probe, err := marshalLedgerReadPayload(ledgerReadPagePayload("ref:x", "ledger", len(content), 0, 64, 0, content))
	if err != nil {
		t.Fatal(err)
	}
	budget := len(probe) + 4
	tool := &ledgerReadTool{resultCapBytes: budget}

	restore := marshalPayloadJSON
	defer func() { marshalPayloadJSON = restore }()

	calls := 0
	marshalPayloadJSON = func(v any) ([]byte, error) {
		calls++
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if calls > 1 {
			if pad := budget + 1 - len(raw); pad > 0 {
				raw = append(raw, make([]byte, pad)...)
			}
		}
		return raw, nil
	}
	_, err = tool.pageResponse("ref:x", "ledger", len(content), 0, 64, content)
	if err == nil || !strings.Contains(err.Error(), "cannot fit the encoded page") {
		t.Fatalf("over-long encoded ledger page was not refused: %v", err)
	}
}

func readOutputProbeLen(t *testing.T, original, limit int, content string) int {
	t.Helper()
	out, err := marshalReadOutputPayload(readOutputPagePayload("ref:output:x", original, 0, limit, 0, content))
	if err != nil {
		t.Fatal(err)
	}
	return len(out)
}

func ledgerProbeLen(t *testing.T, original, limit int, content string) int {
	t.Helper()
	out, err := marshalLedgerReadPayload(ledgerReadPagePayload("ref:x", "ledger", original, 0, limit, 0, content))
	if err != nil {
		t.Fatal(err)
	}
	return len(out)
}

func pageReturnedBytes(t *testing.T, out string) int {
	t.Helper()
	var env struct {
		ReturnedBytes int `json:"returned_bytes"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal page: %v", err)
	}
	return env.ReturnedBytes
}

// TestReadOutputWalkStopsAtDigitWidthBoundaries verifies the escaped-length
// walk folds the numeric-envelope digit widths into its estimate: with a
// budget that would fit byte 10 by raw content length, the extra
// returned_bytes/next_offset digit (9->10) must push the page end back to 9;
// likewise at 99->100.
func TestReadOutputWalkStopsAtDigitWidthBoundaries(t *testing.T) {
	for _, tc := range []struct {
		content string
		limit   int
		extra   int
		want    int
	}{
		{strings.Repeat("x", 20), 64, 10, 9},      // 9->10 digit flip
		{strings.Repeat("x", 200), 1000, 102, 99}, // 99->100 digit flip
	} {
		probeLen := readOutputProbeLen(t, len(tc.content), tc.limit, tc.content)
		tool := &readOutputTool{resultCapBytes: probeLen + tc.extra}
		out, err := tool.pageResponse("ref:output:x", len(tc.content), 0, tc.limit, tc.content)
		if err != nil {
			t.Fatalf("pageResponse: %v", err)
		}
		if got := pageReturnedBytes(t, out); got != tc.want {
			t.Fatalf("page end = %d, want %d (digit-width flip)", got, tc.want)
		}
	}
}

// TestLedgerReadWalkStopsAtDigitWidthBoundaries is the ledger_read mirror of
// TestReadOutputWalkStopsAtDigitWidthBoundaries.
func TestLedgerReadWalkStopsAtDigitWidthBoundaries(t *testing.T) {
	for _, tc := range []struct {
		content string
		limit   int
		extra   int
		want    int
	}{
		{strings.Repeat("x", 20), 64, 10, 9},
		{strings.Repeat("x", 200), 1000, 102, 99},
	} {
		probeLen := ledgerProbeLen(t, len(tc.content), tc.limit, tc.content)
		tool := &ledgerReadTool{resultCapBytes: probeLen + tc.extra}
		out, err := tool.pageResponse("ref:x", "ledger", len(tc.content), 0, tc.limit, tc.content)
		if err != nil {
			t.Fatalf("pageResponse: %v", err)
		}
		if got := pageReturnedBytes(t, out); got != tc.want {
			t.Fatalf("page end = %d, want %d (digit-width flip)", got, tc.want)
		}
	}
}
