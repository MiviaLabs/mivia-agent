package tools

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Regression: the stale-write guard's observeFileState must not block on a
// FIFO. The guard opens its target itself on every write/edit/delete/read
// path; a bare os.Open blocks indefinitely on a FIFO swapped in between a
// caller's Stat and the guard's open, pinning the tool worker (TOCTOU DoS).
// RED before the fix (open hangs, the 2s timeout fires); GREEN after the fix
// (openRegularFile returns quickly and refuses the special file).
func TestObserveFileStateFIFONoHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix nonblock")
	}
	fifo := filepath.Join(t.TempDir(), "block.fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var obsErr error
	go func() {
		_, obsErr = observeFileState(fifo)
		close(done)
	}()
	select {
	case <-done:
		// Negative path: the FIFO must be refused as a non-regular special
		// file, never opened and never followed.
		if obsErr == nil {
			t.Fatal("observeFileState accepted a FIFO; want a refusal")
		}
		if !strings.Contains(obsErr.Error(), "not a regular file") {
			t.Fatalf("observeFileState error = %v, want non-regular/special-file refusal", obsErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observeFileState hung on FIFO")
	}
}

// Regression: the stale-write guard's digest must cover the WHOLE file. A
// prefix digest cannot see a same-size change in the tail past the prefix;
// with the mtime restored the guard would pass and the agent would silently
// overwrite foreign work in a file larger than the old 16 MiB cap. RED before
// the fix (prefix digest + mtime + size all match, guard returns nil); GREEN
// after the fix (the full-file digest differs, guard refuses).
func TestStaleWriteGuardDetectsTailChangeBeyondPrefix(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "big.txt")
	const base = 16 << 20 // 16 MiB, the size of the old hashed prefix
	const tail = 4096
	size := base + tail

	content := bytes.Repeat([]byte{'a'}, size)
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// The agent observes the file; the guard records that observation.
	obs, err := observeFileState(abs)
	if err != nil {
		t.Fatal(err)
	}
	recordFileObservation(abs, obs)
	mt := obs.mtime

	// A foreign writer changes only the tail past the old prefix (the prefix
	// bytes stay identical), keeps the size identical, and restores the mtime.
	changed := append([]byte(nil), content...)
	for i := base; i < size; i++ {
		changed[i] = 'b'
	}
	if err := os.WriteFile(abs, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(abs, mt, mt); err != nil {
		t.Fatal(err)
	}

	err = guardStaleWrite(abs)
	if err == nil {
		t.Fatal("guard passed a same-size tail change with restored mtime; want a refusal")
	}
	if !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("refusal does not explain the cause: %v", err)
	}
	// The refusal must not have mutated the foreign content.
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, changed) {
		t.Fatal("guard mutated the file on refusal")
	}
}
