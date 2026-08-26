package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
)

// readDirWithContext lists a directory's entries, honoring ctx: os.ReadDir
// itself accepts no context and cannot be interrupted, so a stalled syscall
// (stale NFS/FUSE handle, a degraded mount) previously hung list_dir forever
// regardless of any timeout wrapped around it from the outside. Races the
// read in a goroutine against ctx.Done(), the same escape mechanism
// readFileWithContext (above) and walkFilteredFiles (search.go) already use.
func readDirWithContext(ctx context.Context, abs string) ([]os.DirEntry, error) {
	return readDirWithContextFn(ctx, abs, os.ReadDir)
}

// readDirWithContextFn is readDirWithContext with the underlying read
// injectable for testing: a real stuck syscall cannot be forced portably in
// a test, so tests substitute a slow fake here instead.
func readDirWithContextFn(ctx context.Context, abs string, readDir func(string) ([]os.DirEntry, error)) ([]os.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type outcome struct {
		entries []os.DirEntry
		err     error
	}
	ch := make(chan outcome, 1)
	go func() {
		entries, err := readDir(abs)
		ch <- outcome{entries, err}
	}()
	select {
	case <-ctx.Done():
		// Abandon the in-flight read; its goroutine finishes independently
		// and is discarded (os.DirEntry values carry no fd to leak).
		return nil, ctx.Err()
	case r := <-ch:
		return r.entries, r.err
	}
}

// requireRegularFile rejects directories and special files (FIFO, device, socket,
// symlink-to-special after Stat) so tools cannot block forever on open/read.
// Prefer openRegularFile for TOCTOU-safe open+fstat; this Stat helper remains
// for cheap pre-checks (size budget) when a following openRegularFile is used.
func requireRegularFile(abs string) (os.FileInfo, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("path is a directory; use list_dir")
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file (mode %s); refusing special files that can block", st.Mode().Type())
	}
	return st, nil
}

// readFileWithContext loads a regular file via non-blocking open + fstat (Unix)
// so FIFO TOCTOU cannot pin the tool worker. Honors ctx: if canceled during
// ReadAll, returns ctx.Err() and abandons the read goroutine.
func readFileWithContext(ctx context.Context, abs string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, _, err := openRegularFile(abs)
	if err != nil {
		return nil, err
	}
	type outcome struct {
		data []byte
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		data, err := io.ReadAll(f)
		_ = f.Close()
		ch <- outcome{data, err}
	}()
	select {
	case <-ctx.Done():
		// Abandon in-flight ReadAll; fd closed by that goroutine when done.
		return nil, ctx.Err()
	case r := <-ch:
		return r.data, r.err
	}
}

// scanBatchLines caps how many already-scanned lines are batched into one
// channel send inside scanLinesWithContext, matching the polling
// granularity the per-256-line ctx.Err() checks this helper replaces used
// to accept. Batching the channel-transport avoids a goroutine-sync/
// scheduler handoff on every single line; consume is still invoked once
// PER LINE, in order, so this only changes how promptly a caller notices
// cancellation while draining an already-buffered (in-memory, not I/O)
// batch - not the bound on an in-flight blocking Scan() call, which stays
// "at most one Scan() call's worth of blocking" either way.
const scanBatchLines = 256

// scanLinesWithContext runs sc's Scan()/Text() loop in a single background
// producer goroutine - bufio.Scanner is not safe for concurrent use, so the
// producer is its only caller - and streams lines to consume in batches of
// up to scanBatchLines per channel send. consume runs once per line, in
// order, synchronously on the CALLER's own goroutine, so any state it
// mutates needs no locking. closer is closed exactly once, guarded by a
// sync.Once shared with the producer: on the ctx-already-canceled fast
// path the caller closes it directly (no producer ever starts, so there is
// nothing else to close it); otherwise the producer closes it itself once
// its Scan() loop fully ends (EOF or scan error), explicitly BEFORE
// sending the final batch so the close happens-before the caller observes
// completion - closing a file out from under an in-flight blocking Read in
// another goroutine is a close-during-read + fd-reuse hazard, and closing
// only in a goroutine-exit defer (which is not ordered relative to the
// channel send) would let the caller return from this function before the
// close is visible, which is both a race and a resource-lifetime surprise
// for callers. On the caller-abandons-early path (ctx canceled mid-scan),
// the producer's deferred close still runs once the producer notices `stop`
// and returns.
func scanLinesWithContext(ctx context.Context, sc *bufio.Scanner, closer io.Closer, consume func(line string) (stop bool, err error)) error {
	var closeOnce sync.Once
	doClose := func() { closeOnce.Do(func() { _ = closer.Close() }) }

	if err := ctx.Err(); err != nil {
		// Fail closed immediately, matching readDirWithContext's fast path:
		// do not spin up a goroutine (and therefore never call Scan) for a
		// context that is already dead. No producer will ever run, so this
		// is the one path where the caller - not the producer - closes.
		doClose()
		return err
	}

	type batch struct {
		lines []string
		err   error // sc.Err() or nil, set only on the final batch
		done  bool  // true on the final batch (EOF or scan error)
	}
	batchCh := make(chan batch, 1)
	stop := make(chan struct{})
	var stopOnce sync.Once
	closeStop := func() { stopOnce.Do(func() { close(stop) }) }

	go func() {
		// Guards the abandonment path: if the caller gives up (ctx done)
		// before the scan loop ends normally, this still closes on exit.
		// A no-op if doClose already ran on the normal-completion path
		// below, via the shared sync.Once.
		defer doClose()
		buf := make([]string, 0, scanBatchLines)
		flush := func(final bool, errv error) bool {
			b := batch{lines: buf, done: final, err: errv}
			buf = make([]string, 0, scanBatchLines)
			select {
			case batchCh <- b:
				return true
			case <-stop:
				return false
			}
		}
		for sc.Scan() {
			buf = append(buf, sc.Text())
			if len(buf) >= scanBatchLines {
				if !flush(false, nil) {
					return
				}
			}
		}
		scanErr := sc.Err()
		// Close before signaling completion so the close happens-before
		// the caller can observe it (channel send/receive is a Go memory
		// model synchronization point) - see the doc comment above.
		doClose()
		flush(true, scanErr)
	}()

	defer closeStop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b := <-batchCh:
			for _, line := range b.lines {
				stopReq, err := consume(line)
				if err != nil {
					return err
				}
				if stopReq {
					return nil
				}
			}
			if b.done {
				return b.err
			}
		}
	}
}
