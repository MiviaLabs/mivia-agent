package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write_file and delete_file are the two write-class tools that used to skip
// the per-path edit lock (edit_lock.go) taken by search_replace and
// multi_edit, so a concurrent read-modify-write span could interleave inside
// their guard+write/remove and one mutation would be silently dropped while
// both tools reported success. Both now take the same lockEditFile mutex held
// across the whole guard+write/remove span. RED before the fix (write/delete
// complete while the lock is held externally); GREEN after (each blocks until
// the lock is released and only then completes, and delete leaves the file
// gone).
func TestWriteFileAndDeleteFileSerializeOnPerPathEditLock(t *testing.T) {
	ws, reg := setupWS(t)
	abs := filepath.Join(ws.Abs, "f.txt")
	externalTouch(t, abs, "old\n")

	// Hold the per-path lock the way a concurrent search_replace/multi_edit
	// read-modify-write span would.
	unlock := lockEditFile(abs)

	writeStarted := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		close(writeStarted)
		_, err := reg.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
			"path": "f.txt", "content": "mine\n",
		}))
		writeDone <- err
	}()
	<-writeStarted

	// The write must stay blocked while the lock is held.
	select {
	case err := <-writeDone:
		t.Fatalf("write_file completed while the per-path lock was held (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write_file failed after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write_file did not complete after lock release")
	}
	if got := readFileAbs(t, abs); got != "mine\n" {
		t.Fatalf("write_file content = %q, want %q", got, "mine\n")
	}

	// Same check for delete_file: re-acquire the lock externally and hold it.
	unlock = lockEditFile(abs)
	delStarted := make(chan struct{})
	delDone := make(chan error, 1)
	go func() {
		close(delStarted)
		_, err := reg.Execute(context.Background(), "delete_file", mustJSON(t, map[string]any{
			"path": "f.txt",
		}))
		delDone <- err
	}()
	<-delStarted

	select {
	case err := <-delDone:
		t.Fatalf("delete_file completed while the per-path lock was held (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-delDone:
		if err != nil {
			t.Fatalf("delete_file failed after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete_file did not complete after lock release")
	}
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		t.Fatalf("delete_file left the file behind (lstat err=%v)", err)
	}
}
