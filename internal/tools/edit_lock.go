package tools

import "sync"

// editFileLocks holds one *sync.Mutex per absolute file path currently
// guarded by an in-place edit (search_replace, multi_edit). Both tools read
// the whole file, compute a replacement in memory, and write the whole file
// back - a read-modify-write with no synchronization of its own. Two
// concurrent edits to the same file can each read the same original
// content, then race to write: the last writer wins with a version that
// never saw the other edit, silently dropping it (or, under raw concurrent
// O_TRUNC writes, corrupting the file outright). Locking the full
// read-compute-write span per path serializes them, so the second edit
// always reads the first edit's result rather than stale content.
var editFileLocks sync.Map // map[string]*sync.Mutex

// lockEditFile acquires the per-path lock for abs and returns a function
// that releases it. Shared across every in-place edit tool so they mutually
// exclude on the same file regardless of which tool is used.
func lockEditFile(abs string) func() {
	v, _ := editFileLocks.LoadOrStore(abs, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
