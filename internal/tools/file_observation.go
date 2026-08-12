package tools

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// The stale-write guard. An agent computes an edit from the view of a file it
// last read or wrote. If a foreign writer - an editor, a second session, a
// checkout, a hook - changed the file on disk since then, applying the edit
// silently overwrites that foreign work and the agent keeps believing it
// edited the version it saw. Every read and every write records the file's
// observed state; a write (or delete) whose target changed since that
// observation is refused with a re-read instruction.
//
// The store is keyed by absolute path and lives for the process, mirroring
// editFileLocks: sessions sharing the process share the view, which is
// exactly what makes a change by a second session detectable. The per-path
// lock in edit_lock.go serializes in-process writers against each other;
// this guard is the defense against writers that never take that lock.

type fileObservation struct {
	mtime  time.Time
	size   int64
	digest [sha256.Size]byte // sha256 of the first observeHashBytes bytes
}

// observeHashBytes caps the hashed prefix. mtime and size still cover files
// larger than the cap, and the in-place edit tools read whole files anyway;
// a bounded prefix keeps the guard cheap on multi-GB targets.
const observeHashBytes = 16 << 20 // 16 MiB

var editFileObservations sync.Map // map[string]fileObservation keyed by abs path

// observeFileState captures the current on-disk state of abs. A missing file
// is not an error here: it is the legitimate pre-state of a create.
func observeFileState(abs string) (fileObservation, error) {
	var obs fileObservation
	f, err := os.Open(abs)
	if err != nil {
		return obs, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return obs, err
	}
	obs.mtime = st.ModTime()
	obs.size = st.Size()
	h := sha256.New()
	if _, err := io.CopyN(h, f, observeHashBytes); err != nil && err != io.EOF {
		return obs, err
	}
	copy(obs.digest[:], h.Sum(nil))
	return obs, nil
}

func recordFileObservation(abs string, obs fileObservation) {
	editFileObservations.Store(abs, obs)
}

// refreshFileObservation re-records the current state after the agent has
// seen it (a read) or changed it (a write), so the guard always compares
// against the newest view. Errors are ignored: a refresh that fails leaves
// the previous observation, which only makes the next guard check stricter.
func refreshFileObservation(abs string) {
	if obs, err := observeFileState(abs); err == nil {
		recordFileObservation(abs, obs)
	}
}

func dropFileObservation(abs string) {
	editFileObservations.Delete(abs)
}

// guardStaleWrite refuses a write to abs when the file changed on disk since
// the agent last read or wrote it. With no prior observation there is nothing
// to be stale against, so the current state is recorded and the write
// proceeds - first writes are explicit by construction and the tool's diff
// shows what they replace. Callers must have already established that abs is
// a regular file, so this never opens a FIFO or other blocking special file.
func guardStaleWrite(abs string) error {
	cur, err := observeFileState(abs)
	if err != nil {
		if os.IsNotExist(err) {
			// A missing file is a legitimate create.
			return nil
		}
		return err
	}
	prev, ok := editFileObservations.Load(abs)
	if !ok {
		recordFileObservation(abs, cur)
		return nil
	}
	old := prev.(fileObservation)
	if cur == old {
		return nil
	}
	return fmt.Errorf(
		"file changed on disk since your last read (%s; mtime %s -> %s, size %d -> %d); "+
			"re-read the file before editing, or this write will overwrite changes you have not seen "+
			"(a post-write formatter such as gofmt may also have rewritten it)",
		abs, old.mtime.Format(time.RFC3339Nano), cur.mtime.Format(time.RFC3339Nano), old.size, cur.size)
}
