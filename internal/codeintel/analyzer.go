package codeintel

import (
	"context"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"
)

// Analyzer resolves symbol references by type-checking a workspace.
//
// One Analyzer owns one cached snapshot (see cache.go) shared by every query.
// mu guards the WHOLE query, not merely the snapshot pointer: go/types
// performs lazy resolution on the objects a query reaches, so two queries
// walking the same shared *packages.Package concurrently would race. Queries
// are therefore serialized - which costs nothing next to what the cache saves,
// since before this each query paid a full ~2.4s packages.Load of its own.
type Analyzer struct {
	root string

	mu   sync.Mutex
	snap *snapshot
}

// NewAnalyzer returns an Analyzer rooted at dir.
//
// The root is resolved to an absolute path. Snapshot invalidation decides
// whether a file belongs to the workspace by comparing it against this root,
// and packages.Load reports absolute paths - so a relative root would match
// nothing, stamp nothing, and silently turn the cache into a permanent stale
// answer rather than a fast one.
func NewAnalyzer(dir string) *Analyzer {
	if dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	return &Analyzer{root: dir}
}

// available reports whether the workspace can be analyzed at all, returning
// the same ErrUnavailable-wrapped error every nav query surfaces to the model.
func (a *Analyzer) available() error {
	if a.root == "" {
		return ErrUnavailable
	}
	if _, err := os.Stat(filepath.Join(a.root, "go.mod")); err != nil {
		return fmt.Errorf("analysis unavailable: %w", ErrUnavailable)
	}
	return nil
}

// loadResult holds the intermediate state after loading packages.
type loadResult struct {
	pkgs      []*packages.Package
	pkgErrors int
	targetObj types.Object
	fset      *token.FileSet
}

// candidate is a package-scope object found while resolving a symbol query,
// paired with the import path of the package that declares it.
type candidate struct {
	obj     types.Object
	pkgPath string
}

// References returns classified references to symbol, capped at limit.
// It returns ErrUnavailable when the workspace cannot be analyzed.
func (a *Analyzer) References(ctx context.Context, symbol string, roles []Role, limit int) (Result, error) {
	if err := a.available(); err != nil {
		return Result{}, err
	}
	if limit <= 0 {
		limit = 50
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	lr, err := a.loadPackages(ctx, symbol)
	if err != nil {
		return Result{}, err
	}

	roleFilter := makeRoleFilter(roles)
	locations, truncated := a.collectLocations(ctx, lr, roleFilter, limit)

	return Result{
		Symbol:    symbol,
		Locations: locations,
		Complete:  lr.pkgErrors == 0,
		Errors:    lr.pkgErrors,
		Truncated: truncated,
	}, nil
}

// loadPackages resolves the target symbol against the cached workspace
// snapshot, loading (or reloading, when the snapshot went stale) as needed.
// It returns an error when the symbol is not found, and a distinct
// ambiguity error when the query matches package-scope declarations in more
// than one distinct package - silently picking one would violate the
// "resolved symbols or nothing" invariant.
//
// The caller must hold a.mu.
func (a *Analyzer) loadPackages(ctx context.Context, symbol string) (loadResult, error) {
	snap, err := a.snapshotLocked(ctx)
	if err != nil {
		return loadResult{}, err
	}
	pkgs, pkgErrors := snap.pkgs, snap.pkgErrors

	pkgPart, name := splitSymbol(symbol)

	var candidates []candidate
	fset := snap.fset
	for _, pkg := range pkgs {
		if pkg.Types == nil || pkg.Types.Scope() == nil {
			continue
		}
		if fset == nil && pkg.Fset != nil {
			fset = pkg.Fset
		}
		if !packageMatches(pkg, pkgPart) {
			continue
		}
		if obj := pkg.Types.Scope().Lookup(name); obj != nil {
			candidates = append(candidates, candidate{obj: obj, pkgPath: pkg.PkgPath})
		}
	}

	targetObj, err := resolveCandidate(symbol, candidates)
	if err != nil {
		return loadResult{}, err
	}

	return loadResult{pkgs: pkgs, pkgErrors: pkgErrors, targetObj: targetObj, fset: fset}, nil
}

// packageMatches reports whether pkg satisfies the qualifier part of a symbol
// query. An empty qualifier matches every package.
func packageMatches(pkg *packages.Package, pkgPart string) bool {
	if pkgPart == "" {
		return true
	}
	if strings.Contains(pkgPart, ".") {
		// Fully-qualified import path (e.g. "github.com/org/mod/pkg"):
		// match directly against the full pkg.PkgPath without a "/" prefix.
		return pkg.PkgPath == pkgPart || strings.HasSuffix(pkg.PkgPath, pkgPart)
	}
	// Short package name (e.g. "tools"): match by name or path suffix.
	if pkg.Types != nil && pkg.Types.Name() == pkgPart {
		return true
	}
	return strings.HasSuffix(pkg.PkgPath, "/"+pkgPart)
}

// resolveCandidate picks the unique target object among candidates found for
// a symbol query. It errors when nothing matched, and errors distinctly when
// candidates span more than one distinct package - a bare name (or a
// qualifier suffix shared by more than one package) can match unrelated
// symbols, and the analyzer must report that rather than silently pick one.
// Multiple candidates within the SAME package (e.g. a production package and
// its "[p.test]" test-augmented variant, loaded separately under Tests:true)
// are not ambiguous: they are the same declaration seen through different
// packages.Package instances, so any one of them is a valid representative.
func resolveCandidate(symbol string, candidates []candidate) (types.Object, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("symbol %q not found in workspace packages", symbol)
	}
	seen := make(map[string]bool, len(candidates))
	var distinctPaths []string
	for _, c := range candidates {
		if !seen[c.pkgPath] {
			seen[c.pkgPath] = true
			distinctPaths = append(distinctPaths, c.pkgPath)
		}
	}
	if len(distinctPaths) > 1 {
		sort.Strings(distinctPaths)
		return nil, fmt.Errorf("symbol %q is ambiguous: matches in %d packages (%s); qualify with the package name (e.g. pkgname.%s) or full import path", symbol, len(distinctPaths), strings.Join(distinctPaths, ", "), candidates[0].obj.Name())
	}
	return candidates[0].obj, nil
}

// makeRoleFilter builds a role filter map from the roles slice.
func makeRoleFilter(roles []Role) map[Role]bool {
	filter := make(map[Role]bool)
	for _, r := range roles {
		filter[r] = true
	}
	return filter
}

// roleRank orders roles for the deterministic location sort. Definitions
// rank first so a capped query can never lose the symbol's definition - the
// invariant the pre-fix in-loop limiter maintained by scanning
// TypesInfo.Defs before Uses (pinned by
// TestReferencesResolvesFullyQualifiedPath). The remaining roles have a
// fixed relative order purely so the sort is total; the exact order among
// them carries no semantics. Unknown roles rank last so an unforeseen role
// can never jump ahead of a definition.
func roleRank(r Role) int {
	switch r {
	case RoleDefinition:
		return 0
	case RoleImplementation:
		return 1
	case RoleCaller:
		return 2
	case RoleReturn:
		return 3
	case RoleComparison:
		return 4
	}
	return 5
}

// collectLocations scans all packages for definitions and uses of targetObj.
// It returns the capped location list and whether at least one genuine match
// existed beyond the cap. All matches are collected and deduplicated first,
// sorted deterministically (see sortAndCapLocations), and only then capped,
// so an identical query always returns the identical subset. Definitions
// sort first (roleRank) because a capped query must never lose the symbol's
// definition: the pre-fix in-loop limiter guaranteed that by scanning
// TypesInfo.Defs before Uses, and TestReferencesResolvesFullyQualifiedPath
// pins it. Truncated is only ever true when a match was actually dropped -
// reaching exactly limit distinct matches with nothing left over reports
// Truncated=false, since nothing was in fact cut.
func (a *Analyzer) collectLocations(ctx context.Context, lr loadResult, roleFilter map[Role]bool, limit int) ([]Location, bool) {
	noFilter := len(roleFilter) == 0
	col := &locationCollector{seen: make(map[string]bool)}

	for _, pkg := range lr.pkgs {
		if !collectPkgLocations(ctx, pkg, lr, noFilter, roleFilter, col.addLoc) {
			// ctx was cancelled mid-scan: return the partial, uncapped result,
			// matching the pre-split early return.
			return col.locations, false
		}
	}

	// If the target is an interface type declaration, find concrete implementations.
	if noFilter || roleFilter[RoleImplementation] {
		if typeName, ok := lr.targetObj.(*types.TypeName); ok {
			if iface, ok := typeName.Type().Underlying().(*types.Interface); ok && iface.NumMethods() > 0 {
				for _, pkg := range lr.pkgs {
					findImplementations(pkg, lr.targetObj, lr.fset, col.addLoc)
				}
			}
		}
	}

	return sortAndCapLocations(col.locations, limit)
}

// locationCollector accumulates candidate locations for one query. All
// matches are deduplicated at insertion time by (path, line, role); the cap
// is applied by the caller only after deterministic sorting, so the iteration
// order of the unordered TypesInfo.Defs/Uses maps can never decide which
// subset survives an identical query.
type locationCollector struct {
	locations []Location
	seen      map[string]bool
}

// addLoc reports one candidate location. It dedups by (path, line, role)
// first - a duplicate is not a new match - and appends unconditionally.
func (c *locationCollector) addLoc(path string, line int, symName string, role Role) {
	key := fmt.Sprintf("%s:%d:%s", path, line, role)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.locations = append(c.locations, Location{
		Path: path, Line: line, Symbol: symName, Role: role,
	})
}

// collectPkgLocations scans one package for definitions and uses of
// lr.targetObj, reporting matches through addLoc. It returns false when ctx
// was cancelled before the package was fully scanned.
func collectPkgLocations(ctx context.Context, pkg *packages.Package, lr loadResult, noFilter bool, roleFilter map[Role]bool, addLoc func(string, int, string, Role)) bool {
	if pkg.TypesInfo == nil {
		return true
	}
	for id, obj := range pkg.TypesInfo.Defs {
		if err := ctx.Err(); err != nil {
			return false
		}
		if obj == nil || !sameObject(obj, lr.targetObj) {
			continue
		}
		if noFilter || roleFilter[RoleDefinition] {
			file, line := posInfo(lr.fset, id)
			addLoc(file, line, obj.Name(), RoleDefinition)
		}
	}
	for id, obj := range pkg.TypesInfo.Uses {
		if err := ctx.Err(); err != nil {
			return false
		}
		if obj == nil || !sameObject(obj, lr.targetObj) {
			continue
		}
		role := classifyUseRole(id, pkg)
		if noFilter || roleFilter[role] {
			file, line := posInfo(lr.fset, id)
			addLoc(file, line, obj.Name(), role)
		}
	}
	return true
}

// sortAndCapLocations sorts locations deterministically - definitions before
// uses (roleRank), then by (path, line, role, symbol) - and applies the cap
// once, after every package has been scanned. It reports whether a genuine
// match was dropped.
func sortAndCapLocations(locations []Location, limit int) ([]Location, bool) {
	sort.Slice(locations, func(i, j int) bool {
		a, b := locations[i], locations[j]
		if ra, rb := roleRank(a.Role), roleRank(b.Role); ra != rb {
			return ra < rb
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Symbol < b.Symbol
	})
	if limit > 0 && len(locations) > limit {
		return locations[:limit], true
	}
	return locations, false
}

// analyzerEnv returns the environment for the go commands the analyzer spawns,
// filtered to the variables those commands need and hardened so they cannot
// reach the network (GOPROXY=off, no proxy or GOPRIVATE inheritance).
//
// LocalAppData matters on Windows: when GOCACHE is not set in the parent
// environment (which go test deliberately omits), cmd/go locates its build
// cache as %LocalAppData%\go-build. Without the variable the go command dies
// with "build cache is required, but could not be located" and every
// packages.Load returns zero packages - which the analyzer would report as
// "symbol not found". Unix needs no equivalent: cmd/go falls back to
// $HOME/.cache/go-build, and HOME is passed through.
//
// Windows env names are case-insensitive and the variable is spelled
// LOCALAPPDATA; match with EqualFold so either spelling survives the filter.
func analyzerEnv() []string {
	env := os.Environ()
	var filtered []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		matched := false
		for _, want := range []string{
			"PATH", "HOME", "USER", "TMPDIR", "TMP", "TEMP", "PATHEXT", "USERPROFILE",
			"LOCALAPPDATA", "GOROOT", "GOCACHE", "GOMODCACHE", "GOENV", "GOWORK",
			"GOFLAGS", "GOOS", "GOARCH", "GOVERSION",
		} {
			if strings.EqualFold(key, want) {
				matched = true
				break
			}
		}
		if matched {
			filtered = append(filtered, e)
		}
	}
	filtered = append(filtered,
		"GOPROXY=off",
		"GONOSUMCHECK=*",
		"GONOSUMDB=*",
		"GOFLAGS=-mod=readonly",
	)
	return filtered
}

// splitSymbol parses "sdkadapter.Mint" or "Mint" into (pkgPart, name).
func splitSymbol(symbol string) (pkgPart, name string) {
	if lastDot := strings.LastIndex(symbol, "."); lastDot >= 0 {
		return symbol[:lastDot], symbol[lastDot+1:]
	}
	return "", symbol
}

// sameObject reports whether two type objects refer to the same symbol.
//
// Both objects must be package-scope declarations (top-level funcs, types,
// vars, and consts) before Name()+Pkg() are compared. Struct fields, methods,
// and local variables/parameters share Name() and Pkg() with an unrelated
// package-level declaration of the same name (e.g. a package-level
// `type Name string` and a struct field `Name string` both have Name()=="Name"
// and the same Pkg()) - without this guard, a use of the field would be
// misreported as a use of the type. Package-level declarations across
// packages.Load's separately-typechecked test variants (e.g. "p" and
// "p [p.test]") still compare equal here, which is required for
// TestReferencesDedupsTestVariants.
func sameObject(a, b types.Object) bool {
	if a == nil || b == nil {
		return false
	}
	if !isPackageScopeObject(a) || !isPackageScopeObject(b) {
		return false
	}
	if a.Pkg() == nil || b.Pkg() == nil {
		return a.Name() == b.Name()
	}
	return a.Pkg().Path() == b.Pkg().Path() && a.Name() == b.Name()
}

// isPackageScopeObject reports whether obj is declared directly in its
// package's scope, as opposed to being a struct field, interface method,
// function receiver/parameter/result, or a local variable - all of which
// have Parent() == nil or a function-local scope rather than the package
// scope, even though they may share Name() and Pkg() with an unrelated
// top-level declaration.
func isPackageScopeObject(obj types.Object) bool {
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	return obj.Parent() == pkg.Scope()
}

// posInfo extracts file path and line number from an ast.Node position.
func posInfo(fset *token.FileSet, n interface{ Pos() token.Pos }) (string, int) {
	if fset == nil {
		return "", 0
	}
	pos := n.Pos()
	if pos == token.NoPos {
		return "", 0
	}
	file := fset.File(pos)
	if file == nil {
		return "", 0
	}
	return file.Name(), file.Line(pos)
}
