package codeintel

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"strings"
)

// FileOutline lists the declarations in a single file, in source order.
//
// This path parses ONE file with the standard parser (plan tools/03 D2): no
// type checking, no workspace load, no cache. It answers "what is in this
// file" for the price of reading it, and keeps working when the workspace
// snapshot is cold or the module does not build at all. The trade is that it
// reports no import paths and no resolved types - only what the syntax says.
func FileOutline(path string) (SymbolResult, error) {
	// A file this backend cannot read gets the same explicit "no analyzer for
	// this language" answer the workspace path gives, rather than a parser
	// error phrased in one language's grammar - which reads as a broken file
	// instead of an unsupported one.
	if !strings.EqualFold(filepath.Ext(path), ".go") {
		return SymbolResult{}, fmt.Errorf("analysis unavailable: no outline backend for %s files: %w",
			outlineExtLabel(path), ErrUnavailable)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return SymbolResult{}, fmt.Errorf("parsing %s: %w", path, err)
	}

	var syms []Symbol
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			syms = append(syms, funcSymbol(fset, path, d))
		case *ast.GenDecl:
			syms = append(syms, genDeclSymbols(fset, path, d)...)
		}
	}
	return SymbolResult{Symbols: syms, Complete: true}, nil
}

// outlineExtLabel names the rejected file type for the unavailable message.
func outlineExtLabel(path string) string {
	if ext := filepath.Ext(path); ext != "" {
		return ext
	}
	return "extensionless"
}

// funcSymbol builds the outline entry for a function or method declaration.
func funcSymbol(fset *token.FileSet, path string, d *ast.FuncDecl) Symbol {
	kind, recv := KindFunc, ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = KindMethod
		recv = exprText(fset, d.Recv.List[0].Type)
	}
	// Render the signature without the body: a declaration's shape is the
	// outline's product, its implementation is not.
	head := *d
	head.Body = nil
	head.Doc = nil
	return Symbol{
		Name:      d.Name.Name,
		Kind:      kind,
		Receiver:  recv,
		Path:      path,
		Line:      lineOf(fset, d.Pos()),
		EndLine:   lineOf(fset, d.End()),
		Exported:  d.Name.IsExported(),
		Signature: collapseLine(nodeText(fset, &head)),
	}
}

// genDeclSymbols builds outline entries for a const/var/type declaration
// group, including the fields of each struct type.
func genDeclSymbols(fset *token.FileSet, path string, d *ast.GenDecl) []Symbol {
	var out []Symbol
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			out = append(out, Symbol{
				Name:      s.Name.Name,
				Kind:      KindType,
				Path:      path,
				Line:      lineOf(fset, s.Pos()),
				EndLine:   lineOf(fset, s.End()),
				Exported:  s.Name.IsExported(),
				Signature: collapseLine("type " + nodeText(fset, s)),
			})
			out = append(out, structFieldSymbols(fset, path, s)...)
		case *ast.ValueSpec:
			kind := KindVar
			if d.Tok == token.CONST {
				kind = KindConst
			}
			for _, name := range s.Names {
				out = append(out, Symbol{
					Name:      name.Name,
					Kind:      kind,
					Path:      path,
					Line:      lineOf(fset, s.Pos()),
					EndLine:   lineOf(fset, s.End()),
					Exported:  name.IsExported(),
					Signature: collapseLine(string(kind) + " " + nodeText(fset, s)),
				})
			}
		}
	}
	return out
}

// structFieldSymbols lists the fields of a struct type declaration. Embedded
// fields are reported under the embedded type's name, which is how they are
// referenced.
func structFieldSymbols(fset *token.FileSet, path string, s *ast.TypeSpec) []Symbol {
	st, ok := s.Type.(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil
	}
	var out []Symbol
	for _, field := range st.Fields.List {
		typeText := exprText(fset, field.Type)
		names := field.Names
		if len(names) == 0 {
			// Embedded field: its name is the (possibly qualified) type name.
			out = append(out, Symbol{
				Name:      embeddedName(typeText),
				Kind:      KindField,
				Receiver:  s.Name.Name,
				Path:      path,
				Line:      lineOf(fset, field.Pos()),
				EndLine:   lineOf(fset, field.End()),
				Exported:  ast.IsExported(embeddedName(typeText)),
				Signature: collapseLine(typeText),
			})
			continue
		}
		for _, name := range names {
			out = append(out, Symbol{
				Name:      name.Name,
				Kind:      KindField,
				Receiver:  s.Name.Name,
				Path:      path,
				Line:      lineOf(fset, field.Pos()),
				EndLine:   lineOf(fset, field.End()),
				Exported:  name.IsExported(),
				Signature: collapseLine(name.Name + " " + typeText),
			})
		}
	}
	return out
}

// embeddedName strips pointer and package qualifiers from an embedded field's
// type text to recover the field name Go gives it.
func embeddedName(typeText string) string {
	name := strings.TrimPrefix(typeText, "*")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.Index(name, "["); i >= 0 {
		name = name[:i]
	}
	return name
}

// nodeText renders an AST node back to source text.
func nodeText(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

// exprText renders an expression (a type, a receiver) as source text.
func exprText(fset *token.FileSet, e ast.Expr) string {
	return collapseLine(nodeText(fset, e))
}
