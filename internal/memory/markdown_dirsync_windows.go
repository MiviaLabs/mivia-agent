//go:build windows

package memory

// syncMemoryDir is a no-op on Windows. There is no directory fsync there:
// os.Open gives a read-only directory handle and FlushFileBuffers on it
// fails with ERROR_ACCESS_DENIED. The durability the POSIX path buys with
// it is already provided by MoveFileEx, whose metadata update NTFS orders
// in its own log, so skipping the call loses no guarantee.
func syncMemoryDir(_ string) error { return nil }
