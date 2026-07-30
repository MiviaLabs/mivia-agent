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
		role := findRoleInFile(f, id)
		if role != "" {
			return role
		}
	}
	return RoleCaller
}

// findRoleInFile walks a file's AST looking for an identifier at the given
// position and classifies its role.
func findRoleInFile(f *ast.File, id *ast.Ident) Role {
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
		}
		return true
	})
	return role
}

// containsIdent reports whether the AST node at pos is an identifier.
func containsIdent(n ast.Node, pos token.Pos) bool {
	if n == nil {
		return false
	}
	if id, ok := n.(*ast.Ident); ok {
		return id.Pos() == pos
	}
	return false
}

// findImplementations searches for concrete types that implement the given
// interface targetObj. It checks both T and *T for each named type in pkg.
// Results are reported through addLoc. The fset is used for position resolution.
func findImplementations(pkg *packages.Package, targetObj types.Object, fset *token.FileSet, addLoc func(string, int, string, Role), limit int, locations *[]Location) {
	if pkg == nil || pkg.Types == nil || pkg.TypesInfo == nil || targetObj == nil || limit <= 0 {
		return
	}
	iface, ok := targetObj.Type().Underlying().(*types.Interface)
	if !ok || iface.NumExplicitMethods() == 0 {
		return
	}

	scope := pkg.Types.Scope()
	if scope == nil {
		return
	}

	for _, name := range scope.Names() {
		if len(*locations) >= limit {
			return
		}
		obj := scope.Lookup(name)
		if obj == nil || obj == targetObj {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		// Check both T and *T — types.Implements on *T catches pointer receivers.
		if types.Implements(named, iface) || types.Implements(types.NewPointer(named), iface) {
			file, line := posInfo(fset, obj)
			addLoc(file, line, obj.Name(), RoleImplementation)
		}
	}
}
