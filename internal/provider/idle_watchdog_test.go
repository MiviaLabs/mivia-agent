package provider

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingReader mirrors internal/tools/fs_guard_scan_cancel_test.go's
// blockForever pattern: the first Read returns a header, every Read after
// that blocks on an unbuffered channel until the test closes it - standing
// in for a connection that accepted the response but then went silent.
type stallingReader struct {
	first   []byte
	sent    bool
	unblock chan struct{}
}

func (r *stallingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		if len(r.first) > 0 {
			n := copy(p, r.first)
			return n, nil
		}
	}
	<-r.unblock
	return 0, io.EOF
}

// chunkReader delivers a fixed sequence of chunks, one per Read call,
// pausing delay between each - for proving the watchdog does not fire on a
// normal, merely-slow-but-alive stream.
type chunkReader struct {
	chunks [][]byte
	delay  time.Duration
	i      int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

func withWatchdogTimeouts(t *testing.T, idle, firstByte time.Duration) {
	t.Helper()
	prevIdle, prevFirstByte := streamIdleTimeout(), streamFirstByteTimeout()
	SetStreamWatchdogTimeouts(idle, firstByte, 0)
	t.Cleanup(func() { SetStreamWatchdogTimeouts(prevIdle, prevFirstByte, 0) })
}

// TestIdleWatchdogReader_FirstByteNeverArrives proves a connection that
// accepts the request but never sends anything fails fast on the (short,
// test-only) first-byte bound instead of blocking for the real default
// (240s) or the transport's absolute 15-minute backstop.
func TestIdleWatchdogReader_FirstByteNeverArrives(t *testing.T) {
	withWatchdogTimeouts(t, 2*time.Second, 100*time.Millisecond)
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })

	r := newIdleWatchdogReader(&stallingReader{unblock: unblock}, streamFirstByteTimeout(), streamIdleTimeout(), "test")

	start := time.Now()
	buf := make([]byte, 64)
	_, err := r.Read(buf)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("expected ErrStreamIdle, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Read blocked %s past the 100ms first-byte bound - the watchdog did not fire", elapsed)
	}
}

// TestIdleWatchdogReader_GoesIdleAfterFirstByte proves a connection that
// delivers a first byte and then goes silent fails fast on the (short) idle
// bound, not the longer first-byte bound.
func TestIdleWatchdogReader_GoesIdleAfterFirstByte(t *testing.T) {
	withWatchdogTimeouts(t, 100*time.Millisecond, 5*time.Second)
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })

	r := newIdleWatchdogReader(&stallingReader{first: []byte("data: hello\n"), unblock: unblock}, streamFirstByteTimeout(), streamIdleTimeout(), "test")

	buf := make([]byte, 64)
	n, err := r.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("first Read: got n=%d err=%v, want the first-byte payload with no error", n, err)
	}

	start := time.Now()
	_, err = r.Read(buf)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("expected ErrStreamIdle on the second read, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("second Read blocked %s past the 100ms idle bound - the watchdog did not fire", elapsed)
	}
}

// TestIdleWatchdogReader_FastStreamUnaffected proves a normal stream, whose
// chunks each arrive well within the idle bound, completes untouched -
// the watchdog must never wrap a healthy read in extra latency or errors.
func TestIdleWatchdogReader_FastStreamUnaffected(t *testing.T) {
	withWatchdogTimeouts(t, 2*time.Second, 2*time.Second)

	chunks := [][]byte{[]byte("chunk-one "), []byte("chunk-two "), []byte("chunk-three")}
	src := &chunkReader{chunks: chunks, delay: 10 * time.Millisecond}
	r := newIdleWatchdogReader(src, streamFirstByteTimeout(), streamIdleTimeout(), "test")

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll: unexpected error %v", err)
	}
	want := strings.Join([]string{"chunk-one ", "chunk-two ", "chunk-three"}, "")
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestIdleWatchdogReader_SlowFirstByteWithinBoundSucceeds proves the
// first-byte allowance is genuinely separate from and longer than the
// inter-chunk idle bound: a connection that delays past the idle bound but
// within the first-byte bound before its FIRST byte must still succeed.
func TestIdleWatchdogReader_SlowFirstByteWithinBoundSucceeds(t *testing.T) {
	withWatchdogTimeouts(t, 50*time.Millisecond, 2*time.Second)

	src := &chunkReader{chunks: [][]byte{[]byte("late-but-ok")}, delay: 300 * time.Millisecond}
	r := newIdleWatchdogReader(src, streamFirstByteTimeout(), streamIdleTimeout(), "test")

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("a first byte arriving at 300ms (within the 2s first-byte bound, past the 50ms idle bound) must not trip the shorter idle timeout: %v", err)
	}
	if string(got) != "late-but-ok" {
		t.Fatalf("got %q, want %q", got, "late-but-ok")
	}
}

// TestIdleWatchdogReader_ErrorDistinguishableFromDeadlineExceeded proves
// ErrStreamIdle is its own sentinel, not context.DeadlineExceeded - so
// downstream classification (transient.go's IsTransient/markTransientReadDeadline)
// can tell a dead connection apart from a caller-chosen budget running out.
func TestIdleWatchdogReader_ErrorDistinguishableFromDeadlineExceeded(t *testing.T) {
	withWatchdogTimeouts(t, 50*time.Millisecond, 50*time.Millisecond)
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })

	r := newIdleWatchdogReader(&stallingReader{unblock: unblock}, streamFirstByteTimeout(), streamIdleTimeout(), "test")
	_, err := r.Read(make([]byte, 8))

	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("expected errors.Is(err, ErrStreamIdle), got %v", err)
	}
	if err == nil {
		t.Fatal("expected non-nil error")
	}
}

// TestIdleWatchdogReader_ConcurrentReadersDoNotRace exercises two
// independent watchdog readers under -race to confirm the pump/Read
// handoff (buffered channel + startOnce) has no shared mutable state across
// instances.
func TestIdleWatchdogReader_ConcurrentReadersDoNotRace(t *testing.T) {
	withWatchdogTimeouts(t, 2*time.Second, 2*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := &chunkReader{chunks: [][]byte{[]byte("a"), []byte("b"), []byte("c")}}
			r := newIdleWatchdogReader(src, streamFirstByteTimeout(), streamIdleTimeout(), "test")
			if _, err := io.ReadAll(r); err != nil {
				t.Errorf("io.ReadAll: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestSetStreamWatchdogTimeouts_NonPositiveLeavesOtherBoundUnchanged proves
// a caller that only knows some of the three bounds never resets the others
// to zero (which would make every read fail instantly).
func TestSetStreamWatchdogTimeouts_NonPositiveLeavesOtherBoundUnchanged(t *testing.T) {
	withWatchdogTimeouts(t, 30*time.Second, 45*time.Second)
	withStreamContentIdleTimeout(t, 60*time.Second)

	SetStreamWatchdogTimeouts(10*time.Second, 0, 0)
	if got := streamIdleTimeout(); got != 10*time.Second {
		t.Fatalf("idle timeout = %s, want 10s", got)
	}
	if got := streamFirstByteTimeout(); got != 45*time.Second {
		t.Fatalf("first-byte timeout changed to %s on a non-positive update, want unchanged 45s", got)
	}
	if got := streamContentIdleTimeout(); got != 60*time.Second {
		t.Fatalf("content-idle timeout changed to %s on a non-positive update, want unchanged 60s", got)
	}

	SetStreamWatchdogTimeouts(-1, 20*time.Second, 0)
	if got := streamIdleTimeout(); got != 10*time.Second {
		t.Fatalf("idle timeout changed to %s on a non-positive update, want unchanged 10s", got)
	}
	if got := streamFirstByteTimeout(); got != 20*time.Second {
		t.Fatalf("first-byte timeout = %s, want 20s", got)
	}
	if got := streamContentIdleTimeout(); got != 60*time.Second {
		t.Fatalf("content-idle timeout changed to %s on a non-positive update, want unchanged 60s", got)
	}

	SetStreamWatchdogTimeouts(0, 0, 15*time.Second)
	if got := streamContentIdleTimeout(); got != 15*time.Second {
		t.Fatalf("content-idle timeout = %s, want 15s", got)
	}
	if got := streamIdleTimeout(); got != 10*time.Second {
		t.Fatalf("idle timeout changed to %s on a content-idle-only update, want unchanged 10s", got)
	}
	if got := streamFirstByteTimeout(); got != 20*time.Second {
		t.Fatalf("first-byte timeout changed to %s on a content-idle-only update, want unchanged 20s", got)
	}
}

// A caller may read past EOF - a bounded read followed by a connection-reuse
// drain does exactly that. The pump exits when src returns an error, so
// without a latched terminal error the second read waits out the whole idle
// bound for a result that can never arrive.
func TestIdleWatchdogReader_EOFIsRepeatableWithoutStalling(t *testing.T) {
	withWatchdogTimeouts(t, 30*time.Second, 30*time.Second)
	r := newIdleWatchdogReader(strings.NewReader("short body"), 30*time.Second, 30*time.Second, "probe")

	buf := make([]byte, 64)
	if n, err := r.Read(buf); err != nil || n == 0 {
		t.Fatalf("first read = (%d, %v), want the body", n, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.Read(buf)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("second read = %v, want io.EOF", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second read blocked after EOF; the terminal error was not latched")
	}
}
