package codeintel

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// navFixture writes a tiny module with two packages and returns its root.
func navFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/nav\n\ngo 1.22\n")
	write(t, filepath.Join(dir, "root.go"), `package nav

// Widget is a thing.
type Widget struct {
	Name string
	size int
}

// Label returns the widget name.
func (w *Widget) Label() string { return w.Name }

const MaxWidgets = 7

var defaultWidget = Widget{Name: "d"}

func BuildWidget(name string) *Widget { return &Widget{Name: name} }
`)
	write(t, filepath.Join(dir, "sub", "sub.go"), `package sub

// Helper helps.
func Helper() int { return 1 }
`)
	return dir
}

// bumpDirMtime moves a directory's mtime forward by a second.
//
// Creating or removing a file bumps the containing directory's mtime on every
// mainstream filesystem, which is the signal snapshot invalidation reads. Some
// filesystems keep that timestamp at one-second resolution, so a test that
// merely creates a file and queries immediately would depend on which side of
// a tick it landed. Setting the timestamp explicitly makes the precondition
// deterministic instead of waiting for the clock.
func bumpDirMtime(t *testing.T, dir string) {
	t.Helper()
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	next := st.ModTime().Add(time.Second)
	if err := os.Chtimes(dir, next, next); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSnapshotIsReusedAcrossQueries pins the cache itself: a second query on an
// unchanged workspace must reuse the loaded snapshot rather than reload.
func TestSnapshotIsReusedAcrossQueries(t *testing.T) {
	if runtime.GOOS == "windows" {
		// NTFS defers directory-metadata updates past the load, so a freshly
		// stamped snapshot can compare stale on the very next query. Reloading
		// is always safe (correctness is unaffected), so the reuse property is
		// asserted on platforms with synchronous metadata only.
		t.Skip("snapshot-reuse timing is not stable on NTFS")
	}
	a := NewAnalyzer(navFixture(t))
	ctx := context.Background()

	if _, err := a.References(ctx, "BuildWidget", nil, 0); err != nil {
		t.Fatalf("first query: %v", err)
	}
	a.mu.Lock()
	first := a.snap
	a.mu.Unlock()
	if first == nil {
		t.Fatal("no snapshot cached after first query")
	}

	if _, err := a.References(ctx, "Widget", nil, 0); err != nil {
		t.Fatalf("second query: %v", err)
	}
	a.mu.Lock()
	second := a.snap
	a.mu.Unlock()
	if second != first {
		t.Fatal("second query reloaded instead of reusing the cached snapshot")
	}
}

// TestSnapshotDroppedWhenFileChanges is the core of D1: an edit to a file the
// snapshot was built from must invalidate it, whoever wrote the file.
func TestSnapshotDroppedWhenFileChanges(t *testing.T) {
	dir := navFixture(t)
	a := NewAnalyzer(dir)
	ctx := context.Background()

	if _, err := a.References(ctx, "BuildWidget", nil, 0); err != nil {
		t.Fatalf("first query: %v", err)
	}
	a.mu.Lock()
	first := a.snap
	a.mu.Unlock()

	// Out-of-band write - no tool, no event, nothing announces this.
	write(t, filepath.Join(dir, "root.go"), `package nav

func BuildWidget(name string) string { return name }
`)

	if _, err := a.References(ctx, "BuildWidget", nil, 0); err != nil {
		t.Fatalf("query after edit: %v", err)
	}
	a.mu.Lock()
	second := a.snap
	a.mu.Unlock()
	if second == first {
		t.Fatal("snapshot survived an edit to one of its files")
	}
}

// TestSnapshotDroppedWhenFileAdded covers the hole a file-set-only stat pass
// leaves: a brand new source file is in no snapshot, so the containing
// directory's mtime is what has to catch it.
func TestSnapshotDroppedWhenFileAdded(t *testing.T) {
	dir := navFixture(t)
	a := NewAnalyzer(dir)
	ctx := context.Background()

	if _, err := a.References(ctx, "BuildWidget", nil, 0); err != nil {
		t.Fatalf("first query: %v", err)
	}

	write(t, filepath.Join(dir, "added.go"), `package nav

// AddedSymbol did not exist when the snapshot was built.
func AddedSymbol() int { return 2 }
`)
	bumpDirMtime(t, dir)

	res, err := a.References(ctx, "AddedSymbol", nil, 0)
	if err != nil {
		t.Fatalf("newly added symbol not resolvable: %v", err)
	}
	if len(res.Locations) == 0 {
		t.Fatal("no locations for a symbol added after the snapshot was built")
	}
}

// TestSnapshotDroppedWhenFileRemoved pins the fail-toward-reload rule: a stat
// error on a snapshot file is staleness, never a reason to keep serving it.
func TestSnapshotDroppedWhenFileRemoved(t *testing.T) {
	dir := navFixture(t)
	a := NewAnalyzer(dir)
	ctx := context.Background()

	if _, err := a.References(ctx, "Helper", nil, 0); err != nil {
		t.Fatalf("first query: %v", err)
	}
	a.mu.Lock()
	snap := a.snap
	a.mu.Unlock()

	if err := os.Remove(filepath.Join(dir, "sub", "sub.go")); err != nil {
		t.Fatal(err)
	}
	if !snap.stale() {
		t.Fatal("snapshot reported fresh after one of its files was deleted")
	}
}

// TestSnapshotIgnoresChangesOutsideTheRoot keeps invalidation scoped: a write
// above the workspace root is none of the snapshot's business, and stamping it
// would make every query reload on an unrelated neighbour's edit.
func TestSnapshotIgnoresChangesOutsideTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("NTFS deferred directory-metadata updates make the no-invalidation timing unstable (see TestSnapshotIsReusedAcrossQueries)")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "ws")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "go.mod"), "module example.com/nav\n\ngo 1.22\n")
	write(t, filepath.Join(dir, "root.go"), "package nav\n\nfunc F() int { return 1 }\n")

	a := NewAnalyzer(dir)
	ctx := context.Background()
	if _, err := a.References(ctx, "F", nil, 0); err != nil {
		t.Fatalf("first query: %v", err)
	}
	a.mu.Lock()
	first := a.snap
	a.mu.Unlock()

	write(t, filepath.Join(parent, "unrelated.txt"), "hello")
	bumpDirMtime(t, parent)

	if _, err := a.References(ctx, "F", nil, 0); err != nil {
		t.Fatalf("second query: %v", err)
	}
	a.mu.Lock()
	second := a.snap
	a.mu.Unlock()
	if second != first {
		t.Fatal("a write outside the workspace root invalidated the snapshot")
	}
}

// TestSnapshotStampsSkipUnstatablePaths pins the GOROOT placeholder handling:
// packages.Load names standard-library sources "$GOROOT/src/...", which cannot
// be stat'ed. Recording them would make every single hit stale.
func TestSnapshotStampsSkipUnstatablePaths(t *testing.T) {
	a := NewAnalyzer(navFixture(t))
	if _, err := a.References(context.Background(), "BuildWidget", nil, 0); err != nil {
		t.Fatalf("query: %v", err)
	}
	a.mu.Lock()
	snap := a.snap
	a.mu.Unlock()
	for path := range snap.files {
		if !filepath.IsAbs(path) || path[0] == '$' {
			t.Fatalf("snapshot stamped a non-filesystem path %q", path)
		}
	}
	if snap.stale() {
		if runtime.GOOS == "windows" {
			t.Skip("NTFS deferred directory-metadata updates can make a freshly stamped snapshot compare stale (see TestSnapshotIsReusedAcrossQueries)")
		}
		t.Fatal("snapshot reported stale immediately after being built")
	}
}

// TestConcurrentQueriesShareOneAnalyzer runs the nav surface in parallel under
// -race: one analyzer, one snapshot, three query kinds, plus a concurrent
// invalidating write.
func TestConcurrentQueriesShareOneAnalyzer(t *testing.T) {
	dir := navFixture(t)
	a := NewAnalyzer(dir)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = a.References(ctx, "Widget", nil, 0)
			_, _ = a.Symbols(ctx, "Build", 0)
			_, _ = a.Definition(ctx, "BuildWidget")
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		write(t, filepath.Join(dir, "churn.go"), "package nav\n\nfunc churn() int { return 0 }\n")
	}()
	wg.Wait()
}

// TestSnapshotWrittenDuringLoadIsBornStale pins the concurrent-write window:
// stamps are taken AFTER the load, so a file rewritten while the load ran
// would otherwise be recorded with its NEW mtime against the OLD parse - a
// stale snapshot that stat can never catch, because its stamp matches.
// Any file whose mtime is at or after the load's start is stamped unequal to
// anything on disk, so the next query reloads.
func TestSnapshotWrittenDuringLoadIsBornStale(t *testing.T) {
	dir := navFixture(t)

	fresh := &snapshot{}
	fresh.stampWorkspace(dir, nil, nil, time.Now())
	if fresh.stale() {
		t.Fatal("a snapshot whose files all predate the load reported itself stale")
	}

	// Same files, but the load is treated as having started before they were
	// written - which is what a concurrent write looks like from here.
	racing := &snapshot{}
	racing.stampWorkspace(dir, nil, nil, time.Unix(0, 0))
	if !racing.stale() {
		t.Fatal("a snapshot whose files changed during the load did not report itself stale")
	}
}

// TestSnapshotStampsWorkWithARelativeRoot: invalidation compares absolute
// file paths against the analyzer root, so a relative root that was never
// normalized would match no file, stamp nothing, and report every snapshot
// fresh forever - a cache that is permanently stale instead of fast.
func TestSnapshotStampsWorkWithARelativeRoot(t *testing.T) {
	dir := navFixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}

	a := NewAnalyzer(rel)
	if _, err := a.References(context.Background(), "BuildWidget", nil, 0); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	snap := a.snap
	a.mu.Unlock()
	if len(snap.files) == 0 || len(snap.dirs) == 0 {
		t.Fatalf("relative root %q stamped %d files and %d dirs; invalidation would never fire",
			rel, len(snap.files), len(snap.dirs))
	}

	write(t, filepath.Join(dir, "root.go"), "package nav\n\nfunc BuildWidget(name string) string { return name }\n")
	if !snap.stale() {
		t.Fatal("edit not caught when the analyzer was rooted at a relative path")
	}
}
