//go:build windows

package chatsync

import "os"

// truncateOutboxFile cuts the events file to size through a second handle.
//
// The events file is opened O_APPEND, and Go's Windows open strips
// FILE_WRITE_DATA from an append handle, leaving only FILE_APPEND_DATA.
// SetEndOfFile, which backs (*os.File).Truncate, needs FILE_WRITE_DATA, so
// truncating the append handle fails with ERROR_ACCESS_DENIED and strands
// the outbox rollback. Go shares the file for read and write, so a plain
// O_RDWR handle on the same path can do the cut. The append handle keeps
// writing at end-of-file, so it needs no repositioning afterwards.
func truncateOutboxFile(f *os.File, size int64) error {
	rw, err := os.OpenFile(f.Name(), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer rw.Close()
	return rw.Truncate(size)
}
