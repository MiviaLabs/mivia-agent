package tools

import (
	"context"
	"fmt"
	"io"
	"os"
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
