package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// nopCloser is a trivial io.Closer that counts its calls, for tests that
// need to assert closer.Close() runs exactly once.
type nopCloser struct {
	closes int
}

func (c *nopCloser) Close() error {
	c.closes++
	return nil
}

// blockingReader is the scanLinesWithContext analogue of list_dir_cancel_test.go's
// blockForever pattern: Read blocks on an unbuffered channel until the test
// closes it, standing in for a stalled/slow file read that never completes
// on its own.
type blockingReader struct {
	unblock chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	<-r.unblock
	return 0, io.EOF
}

// TestScanLinesWithContext_BlockingReadHonorsCancellation is the
// bufio.Scanner analogue of TestReadDirWithContext_AbandonsOnContextDeadline
// (list_dir_cancel_test.go): a bufio.Scanner's Scan() call blocks on the
// underlying Read with no way to interrupt it from outside, so a stalled
// read previously could hang grep/read_file/inspect_repository's per-file
// scan loops past caller cancellation. scanLinesWithContext races the
// Scan() loop in a background goroutine against ctx.Done().
func TestScanLinesWithContext_BlockingReadHonorsCancellation(t *testing.T) {
	blockForever := make(chan struct{})
	t.Cleanup(func() { close(blockForever) })

	sc := bufio.NewScanner(&blockingReader{unblock: blockForever})
	closer := &nopCloser{}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		err := scanLinesWithContext(ctx, sc, closer, func(line string) (bool, error) {
			return false, nil
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a context.DeadlineExceeded error once the deadline passed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scanLinesWithContext did not return within 2s of a 50ms context deadline: the caller is still hung on the blocked read")
	}
}

// TestScanLinesWithContext_AlreadyCanceledReturnsImmediately mirrors
// TestReadDirWithContext_AlreadyCanceledReturnsImmediately: a context
// canceled before the call even starts must fail closed immediately,
// without spinning up a scan at all.
func TestScanLinesWithContext_AlreadyCanceledReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sc := bufio.NewScanner(strings.NewReader("line1\nline2\n"))
	closer := &nopCloser{}

	called := false
	err := scanLinesWithContext(ctx, sc, closer, func(line string) (bool, error) {
		called = true
		return false, nil
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a context.Canceled error for an already-canceled ctx, got %v", err)
	}
	if called {
		t.Fatal("expected consume not to be invoked at all for an already-canceled ctx")
	}
}

// TestScanLinesWithContext_SuccessPassesThrough proves the happy path is
// unaffected: a small input well under one batch is delivered to consume
// once per line, in order, with correct content.
func TestScanLinesWithContext_SuccessPassesThrough(t *testing.T) {
	input := "a\nb\nc\nd\ne\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	closer := &nopCloser{}

	var got []string
	err := scanLinesWithContext(context.Background(), sc, closer, func(line string) (bool, error) {
		got = append(got, line)
		return false, nil
	})
	if err != nil {
		t.Fatalf("scanLinesWithContext: %v", err)
	}
	want := []string{"a", "b", "c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	if closer.closes != 1 {
		t.Fatalf("closer.Close() called %d times, want 1", closer.closes)
	}
}

// TestScanLinesWithContext_BatchBoundaryPreservesOrderAndCount is the
// load-bearing test for the batching change: it spans two full batches of
// scanBatchLines (256) plus a partial third batch, and asserts consume is
// called exactly once per line, in order, with no line dropped or
// duplicated at either batch boundary (256, 512) or at the end.
func TestScanLinesWithContext_BatchBoundaryPreservesOrderAndCount(t *testing.T) {
	const n = 650 // 2*256 + a partial 138-line batch
	var b strings.Builder
	want := make([]string, 0, n)
	for i := 0; i < n; i++ {
		line := fmt.Sprintf("line-%d", i)
		want = append(want, line)
		b.WriteString(line)
		b.WriteByte('\n')
	}

	sc := bufio.NewScanner(strings.NewReader(b.String()))
	closer := &nopCloser{}

	var got []string
	err := scanLinesWithContext(context.Background(), sc, closer, func(line string) (bool, error) {
		got = append(got, line)
		return false, nil
	})
	if err != nil {
		t.Fatalf("scanLinesWithContext: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestScanLinesWithContext_ConsumeErrorStopsIteration confirms an error
// returned by consume is propagated immediately and iteration stops -
// later lines must never be delivered.
func TestScanLinesWithContext_ConsumeErrorStopsIteration(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("a\nb\nc\n"))
	closer := &nopCloser{}
	boom := errors.New("boom")

	var got []string
	err := scanLinesWithContext(context.Background(), sc, closer, func(line string) (bool, error) {
		got = append(got, line)
		if line == "b" {
			return false, boom
		}
		return false, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected iteration to stop after 2 lines, got %v", got)
	}
}

// TestScanLinesWithContext_StopRequestHaltsIteration confirms consume
// returning stop=true ends iteration early with a nil error and without
// delivering further lines.
func TestScanLinesWithContext_StopRequestHaltsIteration(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("a\nb\nc\n"))
	closer := &nopCloser{}

	var got []string
	err := scanLinesWithContext(context.Background(), sc, closer, func(line string) (bool, error) {
		got = append(got, line)
		return line == "a", nil
	})
	if err != nil {
		t.Fatalf("scanLinesWithContext: %v", err)
	}
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("expected iteration to stop after 1 line, got %v", got)
	}
}
