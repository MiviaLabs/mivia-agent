package codeintel

import (
	"bufio"
	"context"
	"fmt"
	"go/types"
	"os"
	"strings"
)

// Definition resolves symbol to its declaration site and returns the span plus
// the declaration source, read from disk at the reported position and bounded
// to MaxDefinitionLines.
//
// Resolution is symbol-based only (plan tools/03 D4): "Name", "pkg.Name",
// "full/import/path.Name", and - unlike References - "Type.Method",
// "pkg.Type.Method" and "Type.Field", including fields promoted from embedded
// types. It returns ErrUnavailable when the workspace cannot be analyzed.
func (a *Analyzer) Definition(ctx context.Context, symbol string) (Definition, error) {
	if err := a.available(); err != nil {
		return Definition{}, err
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return Definition{}, fmt.Errorf("symbol is required")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	snap, err := a.snapshotLocked(ctx)
	if err != nil {
		return Definition{}, err
	}

	obj, pkgPath, err := resolveSymbolObject(snap, symbol)
	if err != nil {
		return Definition{}, err
	}

	path := pathOf(snap.fset, obj.Pos())
	if path == "" {
		return Definition{}, fmt.Errorf("symbol %q has no source position (declared outside the workspace)", symbol)
	}
	start, end := snap.declSpan(obj.Pos())
	kind, recv := objectKind(obj)

	source, cut, err := readLines(path, start, end, MaxDefinitionLines)
	if err != nil {
		return Definition{}, err
	}

	return Definition{
		Symbol:          symbol,
		Kind:            kind,
		Package:         pkgPath,
		Receiver:        recv,
		Path:            path,
		Line:            start,
		EndLine:         end,
		Signature:       objectSignature(obj),
		Source:          source,
		SourceTruncated: cut,
	}, nil
}

// resolveSymbolObject resolves a symbol query to a single declared object.
// It tries package scope first, then a member ("Type.Method", "Type.Field")
// of a package-scope type. Ambiguity across distinct packages is an error
// rather than an arbitrary pick, matching References.
func resolveSymbolObject(snap *snapshot, symbol string) (types.Object, string, error) {
	pkgPart, name := splitSymbol(symbol)

	var candidates []candidate
	for _, pkg := range snap.pkgs {
		if pkg.Types == nil || pkg.Types.Scope() == nil || !packageMatches(pkg, pkgPart) {
			continue
		}
		if obj := pkg.Types.Scope().Lookup(name); obj != nil {
			candidates = append(candidates, candidate{obj: obj, pkgPath: pkg.PkgPath})
		}
	}
	if len(candidates) > 0 {
		obj, err := resolveCandidate(symbol, candidates)
		if err != nil {
			return nil, "", err
		}
		return obj, pkgPathFor(candidates, obj), nil
	}

	// No package-scope match: try "<qualifier>.<Type>.<member>".
	if pkgPart == "" {
		return nil, "", fmt.Errorf("symbol %q not found in workspace packages", symbol)
	}
	typeQualifier, typeName := splitSymbol(pkgPart)
	for _, pkg := range snap.pkgs {
		if pkg.Types == nil || pkg.Types.Scope() == nil || !packageMatches(pkg, typeQualifier) {
			continue
		}
		tn, ok := pkg.Types.Scope().Lookup(typeName).(*types.TypeName)
		if !ok || tn == nil {
			continue
		}
		obj := lookupMember(tn, pkg.Types, name)
		if obj != nil {
			candidates = append(candidates, candidate{obj: obj, pkgPath: pkg.PkgPath})
		}
	}
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("symbol %q not found in workspace packages", symbol)
	}
	obj, err := resolveCandidate(symbol, candidates)
	if err != nil {
		return nil, "", err
	}
	return obj, pkgPathFor(candidates, obj), nil
}

// lookupMember finds a method or field (including promoted ones) named member
// on the named type tn, checking both T and *T so pointer-receiver methods
// resolve.
func lookupMember(tn *types.TypeName, pkg *types.Package, member string) types.Object {
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return nil
	}
	if obj, _, _ := types.LookupFieldOrMethod(named, true, pkg, member); obj != nil {
		return obj
	}
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(named), true, pkg, member)
	return obj
}

// pkgPathFor returns the import path recorded alongside obj.
func pkgPathFor(candidates []candidate, obj types.Object) string {
	for _, c := range candidates {
		if c.obj == obj {
			return c.pkgPath
		}
	}
	if obj.Pkg() != nil {
		return obj.Pkg().Path()
	}
	return ""
}

// readLines returns lines [start, end] of path (1-based, inclusive), bounded
// to at most maxLines. It reports whether the span was cut.
//
// The text comes from DISK at the reported span rather than from a retained
// AST, which is what keeps the cached load mode free to change without
// changing this output (plan tools/03 D5).
func readLines(path string, start, end, maxLines int) (string, bool, error) {
	if start <= 0 {
		return "", false, nil
	}
	if end < start {
		end = start
	}
	truncated := false
	if maxLines > 0 && end-start+1 > maxLines {
		end = start + maxLines - 1
		truncated = true
	}

	f, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("reading definition source: %w", err)
	}
	defer func() { _ = f.Close() }()

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		if line < start {
			continue
		}
		if line > end {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", false, fmt.Errorf("reading definition source: %w", err)
	}
	return b.String(), truncated, nil
}
