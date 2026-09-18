#!/usr/bin/env python3
"""Measure duplicate and near-duplicate Go Test functions across the tree.

A "wrapper" program: the actual AST analysis runs as generated Go source
(go/parser) executed with `go run`. Three hashes per Test function body:

- exact:  formatted body text - byte-identical copies only.
- loose:  shape stream with every identifier, selector tail, and literal
          elided - bodies that differ only in names and literals.
- surface: the sorted set of the function's assertion calls with arguments
          elided the same way - functions that assert the same facts but are
          WRITTEN differently, which the body hashes cannot see.

This is measurement, not a gate: table-driven tests legitimately share
shapes, so the tool prints a report and never exits non-zero on findings.
Usage: check_test_duplication.py [--root DIR] [--details] [--json]
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
import tempfile
from pathlib import Path

GO_ANALYSER_CODE = r'''package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var roundName = regexp.MustCompile(`coverage|pass[0-9]|round[0-9]|audit|wave`)

var assertionSel = map[string]bool{
	"Error": true, "Errorf": true, "Fatal": true, "Fatalf": true,
	"Fail": true, "FailNow": true, "NoError": true, "NoErrorf": true,
	"Equal": true, "NotEqual": true, "EqualError": true, "ErrorIs": true,
	"ErrorContains": true, "Nil": true, "NotNil": true, "True": true,
	"False": true, "Len": true, "Contains": true, "Same": true,
	"Empty": true, "Greater": true, "Less": true, "GreaterOrEqual": true,
	"LessOrEqual": true, "Truef": true, "Falsef": true, "Nilf": true,
}

type elider struct{ buf *bytes.Buffer }

func (e *elider) Visit(n ast.Node) ast.Visitor {
	switch v := n.(type) {
	case *ast.Ident:
		e.buf.WriteString("_ID\n")
		return nil
	case *ast.BasicLit:
		e.buf.WriteString("_LIT\n")
		return nil
	case *ast.SelectorExpr:
		ast.Walk(e, v.X)
		e.buf.WriteString("_SEL\n")
		return nil
	}
	fmt.Fprintf(e.buf, "%T\n", n)
	return e
}

func hashStream(nodes []ast.Node, fset *token.FileSet) string {
	var buf bytes.Buffer
	for _, n := range nodes {
		if err := printer.Fprint(&buf, fset, n); err == nil {
			buf.WriteByte('\n')
		}
	}
	sum := sha256.Sum256(buf.Bytes())
	return fmt.Sprintf("%x", sum[:8])
}

func hashElided(n ast.Node) string {
	var buf bytes.Buffer
	ast.Walk(&elider{buf: &buf}, n)
	sum := sha256.Sum256(buf.Bytes())
	return fmt.Sprintf("%x", sum[:8])
}

func assertionCalls(body *ast.BlockStmt) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !assertionSel[sel.Sel.Name] {
			return true
		}
		var shape bytes.Buffer
		shape.WriteString(sel.Sel.Name)
		shape.WriteString("/")
		ast.Walk(&elider{buf: &shape}, call)
		out = append(out, shape.String())
		return true
	})
	sort.Strings(out)
	return out
}

// surfaceKey collapses a function to its assertion surface, but only when
// the surface carries real content: below MIN_SURFACE_ASSERTS the surface
// is one or two generic calls ("Fatal/...", "Equal/...") and thousands of
// legitimately different one-liner tests would group together.
const MIN_SURFACE_ASSERTS = 3

func surfaceKey(calls []string) string {
	if len(calls) < MIN_SURFACE_ASSERTS {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(calls, "\n")))
	return fmt.Sprintf("%x", sum[:8])
}

type FuncRec struct {
	Pkg     string `json:"pkg"`
	File    string `json:"file"`
	Name    string `json:"name"`
	Line    int    `json:"line"`
	Exact   string `json:"exact"`
	Loose   string `json:"loose"`
	Surface string `json:"surface"`
	Asserts int    `json:"asserts"`
}

type PkgStat struct {
	Pkg            string `json:"pkg"`
	RoundFiles     int    `json:"round_files"`
	RoundFuncs     int    `json:"round_funcs"`
	DupExactFuncs  int    `json:"dup_exact_funcs"`
	DupLooseFuncs  int    `json:"dup_loose_funcs"`
	DupSurfaceFuncs int   `json:"dup_surface_funcs"`
}

func main() {
	args := os.Args[1:]
	details := false
	var root string
	if len(args) > 0 {
		root = args[0]
	} else {
		root = "."
	}
	if len(args) > 1 && args[1] == "-details" {
		details = true
	}

	fset := token.NewFileSet()
	var recs []FuncRec
	pkgFiles := map[string]int{}

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "testdata", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasSuffix(base, "_test.go") {
			return nil
		}
		stem := strings.TrimSuffix(base, "_test.go")
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(rel, ".mivia"+string(filepath.Separator)+"worktrees") ||
			strings.Contains(rel, ".claude"+string(filepath.Separator)+"worktrees") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		if roundName.MatchString(stem) {
			pkgFiles[filepath.Dir(path)]++
		}
		// surface groups run across ALL files in the package, not only
		// round-named ones: differently-written duplicates hide anywhere.
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
				continue
			}
			calls := assertionCalls(fd.Body)
			var exactNodes []ast.Node
			exactNodes = append(exactNodes, fd.Body)
			recs = append(recs, FuncRec{
				Pkg:     filepath.Dir(path),
				File:    rel,
				Name:    fd.Name.Name,
				Line:    fset.Position(fd.Pos()).Line,
				Exact:   hashStream(exactNodes, fset),
				Loose:   hashElided(fd.Body),
				Surface: surfaceKey(calls),
				Asserts: len(calls),
			})
		}
		return nil
	})
	_ = pkgFiles

	if details {
		out, _ := json.MarshalIndent(recs, "", "  ")
		fmt.Println(string(out))
		return
	}
	byPkg := map[string][]FuncRec{}
	for _, r := range recs {
		byPkg[r.Pkg] = append(byPkg[r.Pkg], r)
	}
	var stats []PkgStat
	for pkg, rs := range byPkg {
		s := PkgStat{Pkg: pkg}
		count := func(key func(FuncRec) string, dst *int) {
			groups := map[string][]FuncRec{}
			for _, r := range rs {
				groups[key(r)] = append(groups[key(r)], r)
			}
			for _, g := range groups {
				if len(g) > 1 {
					*dst += len(g)
				}
			}
		}
		count(func(r FuncRec) string { return r.Exact }, &s.DupExactFuncs)
		count(func(r FuncRec) string { return r.Loose }, &s.DupLooseFuncs)
		count(func(r FuncRec) string {
			if r.Surface == "" {
				return r.Name // unique per function: never groups
			}
			return r.Surface
		}, &s.DupSurfaceFuncs)
		stats = append(stats, s)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].DupSurfaceFuncs > stats[j].DupSurfaceFuncs })
	out, _ := json.MarshalIndent(map[string]any{"packages": stats}, "", "  ")
	fmt.Println(string(out))
}

func asserterLines(calls []string) []ast.Node {
	var out []ast.Node
	for _, c := range calls {
		out = append(out, fakeNode(c))
	}
	return out
}

type fakeNode string

func (f fakeNode) Pos() token.Pos { return token.NoPos }
func (f fakeNode) End() token.Pos { return token.NoPos }
'''


def run_analyser(root: Path, details: bool) -> str:
    with tempfile.NamedTemporaryFile("w", suffix=".go", delete=False) as tf:
        tf.write(GO_ANALYSER_CODE)
        name = tf.name
    try:
        args = ["go", "run", name, str(root)]
        if details:
            args.append("-details")
        res = subprocess.run(args, capture_output=True, text=True, check=False)
        if res.returncode != 0:
            print(res.stderr, file=sys.stderr)
            raise SystemExit(f"check_test_duplication: go run failed (exit {res.returncode})")
        return res.stdout
    finally:
        try:
            Path(name).unlink()
        except OSError:
            pass


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--root", default=".")
    ap.add_argument("--details", action="store_true")
    ap.add_argument("--json", action="store_true", help="raw JSON output")
    args = ap.parse_args()
    out = run_analyser(Path(args.root).resolve(), args.details)
    if args.json or args.details:
        sys.stdout.write(out)
        return 0
    data = json.loads(out)
    dup = [p for p in data["packages"] if p["dup_exact_funcs"] or p["dup_loose_funcs"] or p["dup_surface_funcs"]]
    for p in dup:
        print(
            f"{p['pkg']}: exact={p['dup_exact_funcs']} loose={p['dup_loose_funcs']} "
            f"surface={p['dup_surface_funcs']}"
        )
    print(f"check_test_duplication: {len(data['packages'])} package(s) scanned, "
          f"{len(dup)} with shared shapes; report only, not a gate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
