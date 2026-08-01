package codeintel

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// classifyUseRole determines the role of a use-site reference by examining
// the AST context around the identifier.
func classifyUseRole(id *ast.Ident, pkg *packages.Package) Role {
	if id == nil || pkg == nil {
		return RoleCaller
	}
	// Walk all syntax trees to find the parent of this identifier.
	for _, f := range pkg.Syntax {
		if f == nil {
			continue
		}
		role := findRoleInFile(f, id, pkg)
		if role != "" {
			return role
		}
	}
	return RoleCaller
}

// findRoleInFile walks a file's AST looking for an identifier at the given
// position and classifies its role.
func findRoleInFile(f *ast.File, id *ast.Ident, pkg *packages.Package) Role {
	pos := id.Pos()
	var role Role
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		// Check if this node is the direct parent of our identifier.
		switch parent := n.(type) {
		case *ast.ReturnStmt:
			for _, res := range parent.Results {
				if containsIdent(res, pos) {
					role = RoleReturn
					return false
				}
			}
		case *ast.BinaryExpr:
			if parent.Op == token.EQL || parent.Op == token.NEQ {
				if containsIdent(parent.X, pos) || containsIdent(parent.Y, pos) {
					role = RoleComparison
					return false
				}
			}
		case *ast.CallExpr:
			if isErrorsIsOrAs(parent, pkg) {
				for _, arg := range parent.Args {
					if containsIdent(arg, pos) {
						role = RoleComparison
						return false
					}
				}
			}
		}
		return true
	})
	return role
}

// isErrorsIsOrAs reports whether call is a call to the standard library's
// errors.Is or errors.As. A sentinel error passed as an argument to either is
// a comparison in every sense that matters to a caller of find_references -
// it is the idiomatic replacement for `==`/`!=` sentinel checks since Go
// 1.13 (see plan 18 §1: "Where is ErrClaimHeld checked?" is answered
// exclusively through errors.Is in this repo). The identifier is resolved
// through the package's own TypesInfo rather than by matching the literal
// name "errors", so a local variable or type named "errors" cannot spoof a
// match.
func isErrorsIsOrAs(call *ast.CallExpr, pkg *packages.Package) bool {
	if pkg == nil || pkg.TypesInfo == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Is" && sel.Sel.Name != "As") {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	pkgName, ok := pkg.TypesInfo.Uses[ident].(*types.PkgName)
	if !ok {
		return false
	}
	return pkgName.Imported().Path() == "errors"
}

// containsIdent reports whether the AST subtree at pos contains an identifier.
func containsIdent(n ast.Node, pos token.Pos) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(child ast.Node) bool {
		if found {
			return false
		}
		if id, ok := child.(*ast.Ident); ok && id.Pos() == pos {
			found = true
			return false
		}
		return true
	})
	return found
}

// findImplementations searches for concrete types that implement the given
// interface targetObj. It checks both T and *T for each named type in pkg.
// Results (and any overflow past the caller's cap) are reported through
// addLoc, which owns dedup and the truncation decision - this function does
// not stop early on a count, so it never under-reports a genuine match that
// would otherwise be silently dropped from the truncation signal.
func findImplementations(pkg *packages.Package, targetObj types.Object, fset *token.FileSet, addLoc func(string, int, string, Role)) {
	if pkg == nil || pkg.Types == nil || pkg.TypesInfo == nil || targetObj == nil {
		return
	}
	iface, ok := targetObj.Type().Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 {
		return
	}

	scope := pkg.Types.Scope()
	if scope == nil {
		return
	}

	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil || obj == targetObj {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		// Check both T and *T - types.Implements on *T catches pointer receivers.
		if types.Implements(named, iface) || types.Implements(types.NewPointer(named), iface) {
			file, line := posInfo(fset, obj)
			addLoc(file, line, obj.Name(), RoleImplementation)
		}
	}
}
