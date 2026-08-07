package codeintel

import (
	"context"
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// DefaultSymbolLimit is the result cap applied when a caller asks for none.
const DefaultSymbolLimit = 50

// Symbols searches the workspace for declarations whose name starts with
// prefix (case-insensitive; an empty prefix matches everything). It reports
// package-scope declarations and their methods - the surface an agent
// navigates by. Struct fields are deliberately not part of workspace search:
// they are an outline concern, and including them turns a prefix query on a
// large module into thousands of hits that crowd out the declarations asked
// for.
//
// It returns ErrUnavailable when the workspace cannot be analyzed.
func (a *Analyzer) Symbols(ctx context.Context, prefix string, limit int) (SymbolResult, error) {
	if err := a.available(); err != nil {
		return SymbolResult{}, err
	}
	if limit <= 0 {
		limit = DefaultSymbolLimit
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	snap, err := a.snapshotLocked(ctx)
	if err != nil {
		return SymbolResult{}, err
	}

	lower := strings.ToLower(prefix)
	var found []Symbol
	seen := make(map[string]bool)

	add := func(obj types.Object, pkgPath string) {
		if obj == nil || obj.Pkg() == nil {
			return
		}
		if !strings.HasPrefix(strings.ToLower(obj.Name()), lower) {
			return
		}
		sym := symbolFor(snap, obj, pkgPath)
		if sym.Path == "" {
			return
		}
		key := fmt.Sprintf("%s:%d:%s", sym.Path, sym.Line, sym.Name)
		if seen[key] {
			// packages.Load reports a package and its test-augmented variant
			// separately; the same declaration must not be listed twice.
			return
		}
		seen[key] = true
		found = append(found, sym)
	}

	for _, pkg := range snap.pkgs {
		if err := ctx.Err(); err != nil {
			return SymbolResult{}, err
		}
		if pkg.Types == nil || pkg.Types.Scope() == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			add(obj, pkg.PkgPath)
			if tn, ok := obj.(*types.TypeName); ok {
				addMethods(tn, pkg.PkgPath, add)
			}
		}
	}

	sortSymbols(found)
	truncated := false
	if len(found) > limit {
		found, truncated = found[:limit], true
	}
	return SymbolResult{
		Symbols:   found,
		Complete:  snap.pkgErrors == 0,
		Errors:    snap.pkgErrors,
		Truncated: truncated,
	}, nil
}

// addMethods reports the methods declared on a named type.
func addMethods(tn *types.TypeName, pkgPath string, add func(types.Object, string)) {
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return
	}
	for i := 0; i < named.NumMethods(); i++ {
		add(named.Method(i), pkgPath)
	}
	if iface, ok := named.Underlying().(*types.Interface); ok {
		for i := 0; i < iface.NumMethods(); i++ {
			add(iface.Method(i), pkgPath)
		}
	}
}

// symbolFor builds a Symbol from a type-checked object.
func symbolFor(snap *snapshot, obj types.Object, pkgPath string) Symbol {
	kind, recv := objectKind(obj)
	start, end := snap.declSpan(obj.Pos())
	return Symbol{
		Name:      obj.Name(),
		Kind:      kind,
		Receiver:  recv,
		Package:   pkgPath,
		Path:      pathOf(snap.fset, obj.Pos()),
		Line:      start,
		EndLine:   end,
		Exported:  obj.Exported(),
		Signature: objectSignature(obj),
	}
}

// sortSymbols orders results deterministically: package, then file, then
// position. Two runs against an unchanged workspace return the same list in
// the same order, which is what makes truncation at a limit meaningful.
func sortSymbols(syms []Symbol) {
	sort.SliceStable(syms, func(i, j int) bool {
		a, b := syms[i], syms[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	})
}
