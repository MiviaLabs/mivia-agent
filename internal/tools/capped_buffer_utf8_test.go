package tools

import (
	"testing"
	"unicode/utf8"
)

// TestCappedBufferRepairsRuneSplitAtQuotaBoundary: head/tail quotas are raw
// byte offsets with no rune awareness, so a multi-byte rune (here "世", 3
// UTF-8 bytes) written right at the cut point gets split across head and
// tail. That must never surface as invalid UTF-8 to a caller - the whole
// point of the fix in assembleHeadTail.
func TestCappedBufferRepairsRuneSplitAtQuotaBoundary(t *testing.T) {
	// max=2 -> headQuota=1, tailQuota=1 (see splitCaptureBudget): the
	// 3-byte rune is guaranteed to be cut mid-sequence.
	c := newCappedBuffer(2)
	if _, err := c.Write([]byte("世")); err != nil {
		t.Fatal(err)
	}
	got := c.Bytes()
	if !utf8.Valid(got) {
		t.Fatalf("capped output is not valid UTF-8: %q (bytes: %x)", got, got)
	}
}

// TestDualCaptureRepairsRuneSplitAtQuotaBoundary: same defect, through the
// stdout/stderr-tagged capture path a real run_command invocation uses.
func TestDualCaptureRepairsRuneSplitAtQuotaBoundary(t *testing.T) {
	d := newDualCapture(2)
	if _, err := d.Stdout().Write([]byte("世")); err != nil {
		t.Fatal(err)
	}
	got := d.StdoutString()
	if !utf8.ValidString(got) {
		t.Fatalf("stdout capture is not valid UTF-8: %q", got)
	}
}

// TestAssembleHeadTailRepairsInvalidUTF8: a tool can also legitimately emit
// bytes that were never valid UTF-8 to begin with (binary output on stdout,
// an unexpected encoding) - assembleHeadTail must repair those too, not just
// rune splits introduced by its own truncation.
func TestAssembleHeadTailRepairsInvalidUTF8(t *testing.T) {
	head := []byte("hello\xff")
	tail := []byte("world")
	got := assembleHeadTail(head, tail, false)
	if !utf8.Valid(got) {
		t.Fatalf("assembled output is not valid UTF-8: %q", got)
	}
}
