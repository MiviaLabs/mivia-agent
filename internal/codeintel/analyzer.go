package codeintel

import (
	"context"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Analyzer resolves symbol references by type-checking a workspace.
type Analyzer struct {
	root string
}

// NewAnalyzer returns an Analyzer rooted at dir.
func NewAnalyzer(dir string) *Analyzer {
	return &Analyzer{root: dir}
}

// loadResult holds the intermediate state after loading packages.
type loadResult struct {
	pkgs      []*packages.Package
	pkgErrors int
	targetObj types.Object
	fset      *token.FileSet
}

// References returns classified references to symbol, capped at limit.
// It returns ErrUnavailable when the workspace cannot be analyzed.
func (a *Analyzer) References(ctx context.Context, symbol string, roles []Role, limit int) (Result, error) {
	if a.root == "" {
		return Result{}, ErrUnavailable
	}
	goModPath := filepath.Join(a.root, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		return Result{}, fmt.Errorf("analysis unavailable: %w", ErrUnavailable)
	}
	if limit <= 0 {
		limit = 50
	}

	lr, err := a.loadPackages(ctx, symbol)
	if err != nil {
		return Result{}, err
	}
	if lr.targetObj == nil {
		return Result{}, fmt.Errorf("symbol %q not found in workspace packages", symbol)
	}

	roleFilter := makeRoleFilter(roles)
	locations := a.collectLocations(lr, roleFilter, limit)

	return Result{
		Symbol:    symbol,
		Locations: locations,
		Complete:  lr.pkgErrors == 0,
		Errors:    lr.pkgErrors,
		Truncated: len(locations) >= limit,
	}, nil
}

// loadPackages loads all packages in the workspace and finds the target symbol.
func (a *Analyzer) loadPackages(ctx context.Context, symbol string) (loadResult, error) {
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   a.root,
		Env:   analyzerEnv(),
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return loadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return loadResult{}, err
	}

	var pkgErrors int
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			pkgErrors++
		}
	}

	pkgPart, name := splitSymbol(symbol)

	var targetObj types.Object
	var fset *token.FileSet
	for _, pkg := range pkgs {
		if pkg.Types == nil || pkg.Types.Scope() == nil {
			continue
		}
		if pkg.Fset != nil && fset == nil {
			fset = pkg.Fset
		}
		if pkgPart != "" && pkg.Types.Name() != pkgPart && !strings.HasSuffix(pkg.PkgPath, "/"+pkgPart) {
			continue
		}
		if obj := pkg.Types.Scope().Lookup(name); obj != nil {
			targetObj = obj
			if fset == nil && pkg.Fset != nil {
				fset = pkg.Fset
			}
		}
	}
	return loadResult{pkgs: pkgs, pkgErrors: pkgErrors, targetObj: targetObj, fset: fset}, nil
}

// makeRoleFilter builds a role filter map from the roles slice.
func makeRoleFilter(roles []Role) map[Role]bool {
	filter := make(map[Role]bool)
	for _, r := range roles {
		filter[r] = true
	}
	return filter
}

// collectLocations scans all packages for definitions and uses of targetObj.
func (a *Analyzer) collectLocations(lr loadResult, roleFilter map[Role]bool, limit int) []Location {
	noFilter := len(roleFilter) == 0
	var locations []Location
	seen := make(map[string]bool)

	addLoc := func(path string, line int, symName string, role Role) {
		key := fmt.Sprintf("%s:%d:%s", path, line, role)
		if seen[key] {
			return
		}
		seen[key] = true
		locations = append(locations, Location{
			Path: path, Line: line, Symbol: symName, Role: role,
		})
	}

	for _, pkg := range lr.pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		// Definitions
		for id, obj := range pkg.TypesInfo.Defs {
			if obj == nil || !sameObject(obj, lr.targetObj) {
				continue
			}
			if noFilter || roleFilter[RoleDefinition] {
				if len(locations) < limit {
					file, line := posInfo(lr.fset, id)
					addLoc(file, line, obj.Name(), RoleDefinition)
				}
			}
		}
		// Uses
		for id, obj := range pkg.TypesInfo.Uses {
			if obj == nil || !sameObject(obj, lr.targetObj) {
				continue
			}
			role := classifyUseRole(id, pkg)
			if noFilter || roleFilter[role] {
				if len(locations) < limit {
					file, line := posInfo(lr.fset, id)
					addLoc(file, line, obj.Name(), role)
				}
			}
		}
	}

	// If the target is an interface type declaration, find concrete implementations.
	if noFilter || roleFilter[RoleImplementation] {
		if typeName, ok := lr.targetObj.(*types.TypeName); ok {
			if iface, ok := typeName.Type().Underlying().(*types.Interface); ok && iface.NumExplicitMethods() > 0 {
				for _, pkg := range lr.pkgs {
					if len(locations) >= limit {
						break
					}
					findImplementations(pkg, lr.targetObj, lr.fset, addLoc, limit, &locations)
				}
			}
		}
	}

	return locations
}

// analyzerEnv returns environment that blocks network access.
func analyzerEnv() []string {
	env := os.Environ()
	var filtered []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		switch key {
		case "PATH", "HOME", "USER", "TMPDIR",
			"GOROOT", "GOCACHE", "GOMODCACHE",
			"GOFLAGS", "GOOS", "GOARCH", "GOVERSION":
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

// splitSymbol parses "contentref.Reference" or "Reference" into (pkgPart, name).
func splitSymbol(symbol string) (pkgPart, name string) {
	if lastDot := strings.LastIndex(symbol, "."); lastDot >= 0 {
		return symbol[:lastDot], symbol[lastDot+1:]
	}
	return "", symbol
}

// sameObject reports whether two type objects refer to the same symbol.
func sameObject(a, b types.Object) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Pkg() == nil || b.Pkg() == nil {
		return a.Name() == b.Name()
	}
	return a.Pkg().Path() == b.Pkg().Path() && a.Name() == b.Name()
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
