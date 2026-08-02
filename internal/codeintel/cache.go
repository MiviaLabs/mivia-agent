package codeintel

import (
	"context"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

// fileStamp is the identity a snapshot recorded for one source file. mtime is
// kept at nanosecond resolution where the filesystem provides it; size is the
// second half of the pair because a same-nanosecond rewrite of a different
// length is otherwise indistinguishable.
type fileStamp struct {
	modNano int64
	size    int64
}

// snapshot is one cached packages.Load result plus everything needed to decide,
// on the next query, whether the workspace still matches what was loaded.
//
// Invalidation is stat-on-hit over the whole recorded set (plan tools/03 D1):
// no bus events, no dirty set, no writer instrumentation. That is the only
// scheme that is complete over every write path - the three file-writer tools,
// run_command (gofmt, sed, git checkout), and edits made outside the agent
// entirely - because it asks the filesystem instead of trusting a producer to
// announce itself. Measured on this repo: 1023 stats in ~1.5ms against a
// ~2.4s packages.Load, so the check costs ~0.06% of what it saves.
type snapshot struct {
	pkgs      []*packages.Package
	fset      *token.FileSet
	pkgErrors int

	// files maps absolute path -> stamp for every parsed file under the
	// workspace root, plus go.mod/go.sum. Files outside the root (GOROOT and
	// module-cache sources, which packages.Load names with unexpanded
	// "$GOROOT/..." placeholders that cannot be stat'ed at all) are
	// deliberately excluded: they are not what an agent edits between calls.
	files map[string]fileStamp

	// dirs maps directory -> mtime for every directory that contributed a
	// file, plus the root. A file ADDED to or REMOVED from a package is not
	// in files, so mtime on the containing directory is what catches it -
	// without this, a newly written source file would stay invisible to
	// symbol queries for the life of the process.
	dirs map[string]int64

	// root bounds the directory walk in stampDir: nothing above the workspace
	// root is recorded, so an unrelated write in a parent directory cannot
	// invalidate the snapshot.
	root string

	// astByPath indexes the loaded syntax by file path, built lazily by
	// astFileFor under the analyzer mutex.
	astByPath map[string]*ast.File

	builtAt time.Time
	loadDur time.Duration
}

// snapshotLocked returns a valid snapshot, reloading when the cached one is
// stale or absent. The caller must hold a.mu.
func (a *Analyzer) snapshotLocked(ctx context.Context) (*snapshot, error) {
	if a.snap != nil && !a.snap.stale() {
		return a.snap, nil
	}
	// Drop before reloading: a failed reload must never leave the stale
	// snapshot installed (fail toward reload, never toward stale).
	a.snap = nil

	start := time.Now()
	cfg := &packages.Config{
		Context: ctx,
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:     a.root,
		Env:     analyzerEnv(),
		Tests:   true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var pkgErrors int
	var fset *token.FileSet
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			pkgErrors++
		}
		if fset == nil && pkg.Fset != nil {
			fset = pkg.Fset
		}
	}

	snap := &snapshot{
		pkgs:      pkgs,
		fset:      fset,
		pkgErrors: pkgErrors,
		builtAt:   time.Now(),
		loadDur:   time.Since(start),
	}
	snap.stampWorkspace(a.root, fset, pkgs, start)
	a.snap = snap
	return snap, nil
}

// stampWorkspace records mtime+size for every file the load parsed under root,
// the module files, and the mtime of every directory that contributed one.
func (s *snapshot) stampWorkspace(root string, fset *token.FileSet, pkgs []*packages.Package, loadStart time.Time) {
	s.root = root
	s.files = make(map[string]fileStamp, 1024)
	s.dirs = make(map[string]int64, 128)
	startNano := loadStart.UnixNano()

	record := func(path string) {
		if !underRoot(root, path) {
			return
		}
		if _, seen := s.files[path]; seen {
			return
		}
		st, err := os.Stat(path)
		if err == nil && st.ModTime().UnixNano() >= startNano {
			// Written while the load was running: the parse may predate the
			// write, so this snapshot cannot be trusted even though its stamp
			// would match. Record a stamp no real file can equal, which makes
			// the very next query reload. Stamping AFTER a load is otherwise
			// a silent staleness window - a concurrent write_file lands
			// between parse and stat, and the snapshot pins the new mtime
			// against the old content.
			s.files[path] = fileStamp{modNano: -1, size: -1}
			s.stampDir(filepath.Dir(path))
			return
		}
		if err != nil {
			// Unstat-able now means unstat-able on the next hit too, which
			// would invalidate on every query. Record it as absent instead:
			// a zero stamp compares unequal to any real file, so if the path
			// later appears the snapshot is correctly dropped.
			s.files[path] = fileStamp{}
			s.stampDir(filepath.Dir(path))
			return
		}
		s.files[path] = fileStamp{modNano: st.ModTime().UnixNano(), size: st.Size()}
		s.stampDir(filepath.Dir(path))
	}

	if fset != nil {
		fset.Iterate(func(f *token.File) bool {
			record(f.Name())
			return true
		})
	}
	// packages.Load reports non-Go inputs (and files it declined to parse)
	// outside the FileSet; they still belong to the build.
	for _, pkg := range pkgs {
		for _, f := range pkg.GoFiles {
			record(f)
		}
		for _, f := range pkg.CompiledGoFiles {
			record(f)
		}
		for _, f := range pkg.OtherFiles {
			record(f)
		}
	}
	record(filepath.Join(root, "go.mod"))
	record(filepath.Join(root, "go.sum"))
	s.stampDir(root)
}

// stampDir records a directory's mtime once, along with every parent up to
// (and including) the workspace root, so a new package directory is caught as
// well as a new file. The walk stops at the root: a write in a parent of the
// workspace is none of this snapshot's business.
func (s *snapshot) stampDir(dir string) {
	for underRoot(s.root, dir) {
		if _, seen := s.dirs[dir]; seen {
			return
		}
		if st, err := os.Stat(dir); err != nil {
			s.dirs[dir] = 0
		} else {
			s.dirs[dir] = st.ModTime().UnixNano()
		}
		if dir == s.root {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// stale reports whether anything the snapshot was built from has changed.
// Any stat error is staleness: dropping and reloading is always safe, while
// reporting a position from a file that may no longer exist is not.
func (s *snapshot) stale() bool {
	for path, want := range s.files {
		st, err := os.Stat(path)
		if err != nil {
			if want == (fileStamp{}) {
				// Was already absent when stamped and still is.
				continue
			}
			return true
		}
		if want.modNano != st.ModTime().UnixNano() || want.size != st.Size() {
			return true
		}
	}
	for dir, want := range s.dirs {
		st, err := os.Stat(dir)
		if err != nil {
			if want == 0 {
				continue
			}
			return true
		}
		if st.ModTime().UnixNano() != want {
			return true
		}
	}
	return false
}

// underRoot reports whether path is inside root. Paths packages.Load reports
// with an unexpanded "$GOROOT" (or any other) prefix are outside by
// construction and are filtered here rather than at every call site.
func underRoot(root, path string) bool {
	if root == "" || path == "" || strings.HasPrefix(path, "$") {
		return false
	}
	if !filepath.IsAbs(path) {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}
