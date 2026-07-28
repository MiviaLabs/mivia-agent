package tools

import (
	"context"
	"fmt"
	"os"
)

// requireRegularFile rejects directories and special files (FIFO, device, socket,
// symlink-to-special after Stat) so tools cannot block forever on open/read.
// Call after workspace.Resolve; returns a clear error for the model.
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

// readFileWithContext loads a regular file but returns if ctx is done.
// On cancel, a blocked ReadFile may continue in the background (abandoned);
// the tool slot is freed so the agent turn cannot hang forever.
func readFileWithContext(ctx context.Context, abs string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type outcome struct {
		data []byte
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		data, err := os.ReadFile(abs)
		ch <- outcome{data, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.data, r.err
	}
}
