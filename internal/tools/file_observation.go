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
	mtime time.Time
	size  int64
	// digest is the sha256 of the FULL file content, so a guard check is
	// O(file size) CPU on multi-GB targets. That is the necessary price of
	// the "any on-disk change" invariant: windowed reads already scan the
	// whole file, in-place edit targets are bounded by maxFileBytes, and
	// delete/overwrite of a huge target is exactly the case the guard must
	// not silently clobber.
	digest [sha256.Size]byte
}

var editFileObservations sync.Map // map[string]fileObservation keyed by abs path

// observeFileState captures the current on-disk state of abs. A missing file
// is not an error here: it is the legitimate pre-state of a create.
//
// The open goes through the package's hardened openRegularFile (O_NONBLOCK
// and O_NOFOLLOW on Unix, then a Stat that refuses non-regular files), so a
// FIFO or other special file swapped in after a caller pre-check cannot block
// the tool worker; mtime and size come from that open's own Stat, so the
// guard compares against what it actually opened, closing the TOCTOU window
// between a caller's Stat and this open.
func observeFileState(abs string) (fileObservation, error) {
	var obs fileObservation
	f, st, err := openRegularFile(abs)
	if err != nil {
		return obs, err
	}
	defer f.Close()
	obs.mtime = st.ModTime()
	obs.size = st.Size()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
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
// shows what they replace. The guard opens abs itself through the package's
// hardened openRegularFile, so it self-protects: a FIFO or other special file
// swapped in after any caller pre-check cannot block the tool worker, and a
// non-regular or symlink final component is refused, not followed.
func guardStaleWrite(abs string) error {
	cur, err := observeFileState(abs)
	if err != nil {
		if os.IsNotExist(err) {
			// A missing file with no prior observation is a legitimate
			// create: there is nothing to be stale against.
			if _, ok := editFileObservations.Load(abs); !ok {
				return nil
			}
			// The agent has seen this path before and it has vanished since:
			// a foreign writer removed a file the agent believes it knows.
			// Refusing with the disappearance message - rather than treating
			// the missing path as a fresh create or a bare ENOENT - keeps the
			// guard contract (never silently act on a file changed since the
			// agent last saw it) and names the re-read that clears the
			// observation via dropIfGone.
			return staleDisappearanceError(abs)
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

// staleDisappearanceError names a foreign deletion of a path the agent had
// previously observed. Both the write guard and the delete path emit it so a
// removal is never reported as a bare ENOENT the agent could chalk up to its
// own action; it points at the re-read that clears the observation.
func staleDisappearanceError(abs string) error {
	return fmt.Errorf(
		"file changed on disk since your last read (%s; it no longer exists - removed by a writer you have not seen); "+
			"re-read the path before writing or deleting",
		abs)
}

// dropIfGone drops the observation for abs when err reports the path no
// longer exists, then returns err unchanged. read_file on a missing path
// fails before the refresh call that would re-record state, so without this a
// foreign deletion would leave a stale observation behind and the
// guardStaleWrite disappearance refusal would fire for the process lifetime:
// the guard only clears when the agent re-reads the file, and a missing file
// cannot be re-read. A re-read of the gone path therefore clears the
// observation and the next write proceeds as an informed create.
func dropIfGone(abs string, err error) error {
	if err != nil && os.IsNotExist(err) {
		editFileObservations.Delete(abs)
	}
	return err
}
