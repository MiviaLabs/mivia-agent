package codeintel

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// astFileFor returns the parsed file that contains pos, or nil when the
// snapshot has no syntax for it (a dependency loaded from export data).
// The index is built once per snapshot; the caller holds the analyzer mutex,
// which is what makes the lazy build safe.
func (s *snapshot) astFileFor(pos token.Pos) *ast.File {
	if s.fset == nil || pos == token.NoPos {
		return nil
	}
	tf := s.fset.File(pos)
	if tf == nil {
		return nil
	}
	if s.astByPath == nil {
		s.astByPath = make(map[string]*ast.File, 1024)
		for _, pkg := range s.pkgs {
			for _, f := range pkg.Syntax {
				if f == nil {
					continue
				}
				ff := s.fset.File(f.Pos())
				if ff == nil {
					continue
				}
				if _, seen := s.astByPath[ff.Name()]; !seen {
					s.astByPath[ff.Name()] = f
				}
			}
		}
	}
	return s.astByPath[tf.Name()]
}

// declSpan returns the [start, end] line span of the declaration that declares
// the identifier at pos. It falls back to a single-line span when no syntax is
// available, so every caller always gets a usable span.
func (s *snapshot) declSpan(pos token.Pos) (int, int) {
	line := lineOf(s.fset, pos)
	f := s.astFileFor(pos)
	if f == nil {
		return line, line
	}
	for _, decl := range f.Decls {
		if pos < decl.Pos() || pos > decl.End() {
			continue
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			return lineOf(s.fset, d.Pos()), lineOf(s.fset, d.End())
		case *ast.GenDecl:
			// A struct field or interface method lives INSIDE a type
			// declaration; reporting the enclosing spec's span for it would
			// answer "where is Widget" when the question was "where is
			// Widget.Name".
			if start, end := fieldSpan(s.fset, f, pos); start > 0 {
				return start, end
			}
			for _, spec := range d.Specs {
				if pos >= spec.Pos() && pos <= spec.End() {
					return lineOf(s.fset, spec.Pos()), lineOf(s.fset, spec.End())
				}
			}
			return lineOf(s.fset, d.Pos()), lineOf(s.fset, d.End())
		}
	}
	// A struct field or interface method: narrow to the smallest enclosing
	// field, otherwise the whole type declaration would be reported.
	if start, end := fieldSpan(s.fset, f, pos); start > 0 {
		return start, end
	}
	return line, line
}

// fieldSpan finds the smallest *ast.Field enclosing pos.
func fieldSpan(fset *token.FileSet, f *ast.File, pos token.Pos) (int, int) {
	var best *ast.Field
	ast.Inspect(f, func(n ast.Node) bool {
		field, ok := n.(*ast.Field)
		if !ok || field == nil {
			return true
		}
		if pos < field.Pos() || pos > field.End() {
			return true
		}
		if best == nil || field.Pos() >= best.Pos() {
			best = field
		}
		return true
	})
	if best == nil {
		return 0, 0
	}
	return lineOf(fset, best.Pos()), lineOf(fset, best.End())
}

// lineOf returns the 1-based line for pos, or 0 when it is unknown.
func lineOf(fset *token.FileSet, pos token.Pos) int {
	if fset == nil || pos == token.NoPos {
		return 0
	}
	f := fset.File(pos)
	if f == nil {
		return 0
	}
	return f.Line(pos)
}

// pathOf returns the file path for pos, or "" when it is unknown.
func pathOf(fset *token.FileSet, pos token.Pos) string {
	if fset == nil || pos == token.NoPos {
		return ""
	}
	f := fset.File(pos)
	if f == nil {
		return ""
	}
	return f.Name()
}

// objectKind classifies a type-checked object into the model-facing kinds.
func objectKind(obj types.Object) (SymbolKind, string) {
	switch o := obj.(type) {
	case *types.Func:
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			return KindMethod, receiverName(sig.Recv().Type())
		}
		return KindFunc, ""
	case *types.TypeName:
		return KindType, ""
	case *types.Const:
		return KindConst, ""
	case *types.Var:
		if o.IsField() {
			return KindField, ""
		}
		return KindVar, ""
	}
	return KindVar, ""
}

// receiverName renders a receiver type as it appears in source ("Widget",
// "*Widget"), without the package qualifier.
func receiverName(t types.Type) string {
	ptr := ""
	if p, ok := t.(*types.Pointer); ok {
		ptr = "*"
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return ptr + t.String()
	}
	return ptr + named.Obj().Name()
}

// objectSignature renders a one-line signature for a type-checked object,
// qualifying foreign packages by name only.
func objectSignature(obj types.Object) string {
	qualifier := func(p *types.Package) string {
		if p == nil || obj.Pkg() == nil {
			return ""
		}
		if p == obj.Pkg() {
			return ""
		}
		return p.Name()
	}
	return collapseLine(types.ObjectString(obj, qualifier))
}

// collapseLine folds a rendered declaration into a single bounded line, so a
// struct type's signature cannot carry its whole body into the result.
func collapseLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const maxSignatureBytes = 200
	if len(s) <= maxSignatureBytes {
		return s
	}
	return strings.TrimSpace(s[:maxSignatureBytes]) + " …"
}
