package provider

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// onceErrReader returns its whole payload and an error in a single Read, the
// way a connection that dies mid-body does.
type onceErrReader struct {
	data []byte
	err  error
	done bool
}

func (r *onceErrReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, r.err
}

// blockingReader never returns, simulating a connection that went silent.
type blockingReader struct{ ch chan struct{} }

func newBlockingReader() *blockingReader { return &blockingReader{ch: make(chan struct{})} }

func (r *blockingReader) Read([]byte) (int, error) {
	<-r.ch
	return 0, io.EOF
}

// TestSSEContentWatchdogPumpAbortsMidFinish pins the shutdown race in pump's
// error path: the reader is closed while the pump is forwarding the partial
// trailing line after a source error, and the pump must take the done exit
// instead of blocking forever on a full channel.
func TestSSEContentWatchdogPumpAbortsMidFinish(t *testing.T) {
	r := newSSEContentWatchdogReader(
		&onceErrReader{data: []byte("data: tail without newline"), err: io.ErrUnexpectedEOF},
		"test",
	)
	// Fill the single result slot so the finish-forwarding send cannot
	// proceed, then close done: the only ready select case is the abort.
	r.resultCh <- wdContentResult{buf: []byte("pending")}
	r.closeDone()

	r.pump() // must return, not block

	if !r.sawDataLine() {
		t.Fatal("the trailing data line was never classified before the abort")
	}
	if len(r.resultCh) != 1 {
		t.Fatalf("resultCh holds %d items, want the 1 pre-filled item only (the finish segment must be dropped on abort)", len(r.resultCh))
	}
}

// TestSSEContentWatchdogDeadlineAbortThenPendingErr: a Read that starts with
// an already-expired deadline aborts with the ErrStreamIdle class, and the
// NEXT Read replays the same error via pendingErr - the caller must never see
// a silent (0, nil) after an abort.
func TestSSEContentWatchdogDeadlineAbortThenPendingErr(t *testing.T) {
	r := newSSEContentWatchdogReader(newBlockingReader(), "test")
	defer r.Close()
	// Consume the start-once so Read cannot re-arm the deadline, then park
	// the deadline in the past: the next Read must take the immediate abort.
	r.startOnce.Do(func() {})
	r.progressDeadline = time.Now().Add(-time.Second)

	buf := make([]byte, 16)
	n, err := r.Read(buf)
	if n != 0 || !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("first Read = (%d, %v), want (0, ErrStreamIdle)", n, err)
	}

	n, err = r.Read(buf)
	if n != 0 || !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("second Read = (%d, %v), want the replayed pendingErr", n, err)
	}
	if r.pendingErr != nil {
		t.Fatal("pendingErr survived the replay; the error would repeat forever")
	}
}

// TestSSEContentWatchdogDeliverStashesError: a result carrying both bytes and
// an error delivers the bytes now and holds the error for the next Read, in
// order - data is never dropped and the error is never lost.
func TestSSEContentWatchdogDeliverStashesError(t *testing.T) {
	r := &sseContentWatchdogReader{resultCh: make(chan wdContentResult, 1), done: make(chan struct{})}
	srcErr := errors.New("source died")
	p := make([]byte, 16)

	n, err := r.deliver(p, wdContentResult{buf: []byte("abc"), err: srcErr})
	if n != 3 || err != nil {
		t.Fatalf("deliver = (%d, %v), want (3, nil)", n, err)
	}
	if !bytes.Equal(p[:3], []byte("abc")) {
		t.Fatalf("delivered %q, want %q", p[:3], "abc")
	}
	if r.pendingErr == nil {
		t.Fatal("the accompanying error was not stashed for the next Read")
	}

	// A small buffer turns the rest into leftover; the stashed error still
	// waits behind it.
	n, err = r.deliver(p[:2], wdContentResult{buf: []byte("abc")})
	if n != 2 || err != nil {
		t.Fatalf("small-buffer deliver = (%d, %v), want (2, nil)", n, err)
	}
	if string(r.leftover) != "c" {
		t.Fatalf("leftover = %q, want %q", r.leftover, "c")
	}
}

// TestSSEContentClassifierOversizeNoNewline: in oversize mode a chunk without
// a newline streams through as raw progress, byte-identical, carrying the
// line's data-line verdict.
func TestSSEContentClassifierOversizeNoNewline(t *testing.T) {
	cases := []struct {
		name         string
		oversizeData bool
		chunk        string
	}{
		{"data line tail", true, "more of a huge data line"},
		{"non-data line tail", false, "still oversize"},
	}
	for _, tc := range cases {
		c := &sseContentClassifier{oversize: true, oversizeData: tc.oversizeData}
		segs := c.classify([]byte(tc.chunk))
		if len(segs) != 1 {
			t.Fatalf("%s: got %d segments, want 1", tc.name, len(segs))
		}
		seg := segs[0]
		if string(seg.data) != tc.chunk {
			t.Fatalf("%s: data = %q, want the exact wire bytes %q", tc.name, seg.data, tc.chunk)
		}
		if !seg.progress {
			t.Fatalf("%s: an oversize chunk is progress and must move the deadline", tc.name)
		}
		if seg.dataLine != tc.oversizeData {
			t.Fatalf("%s: dataLine = %v, want the remembered verdict %v", tc.name, seg.dataLine, tc.oversizeData)
		}
		if !c.oversize {
			t.Fatalf("%s: a chunk without a newline must keep oversize mode", tc.name)
		}
	}
}

// TestSSEContentClassifierFinishOversize: at end of stream an oversize line
// flushes nothing - its bytes already went downstream as raw progress - and
// the classifier resets so a following stream (connection reuse) starts clean.
func TestSSEContentClassifierFinishOversize(t *testing.T) {
	c := &sseContentClassifier{oversize: true, oversizeData: true}
	if segs := c.finish(); segs != nil {
		t.Fatalf("finish in oversize mode = %+v, want nil", segs)
	}
	if c.oversize {
		t.Fatal("finish did not reset oversize mode")
	}
}
