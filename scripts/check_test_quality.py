#!/usr/bin/env python3
"""Test quality gate: inspect Go test bodies for zero assertions, empty bodies,
unreviewed t.Skip additions, and tautological checks.

Modes:
  --staged                staged changes vs HEAD (pre-commit / fast loop)
  --base REF [--tip REF]  REF..TIP commit range (CI / pre-push)
  --all                   all tracked *_test.go under cmd/ and internal/
  --worktree              all *_test.go on disk under cmd/ and internal/
  --paths ...             explicit paths

Exit codes:
  0 = OK
  1 = test quality violations found
  2 = usage / environment error
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

def repo_root() -> Path:
    r = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        capture_output=True, text=True, check=False,
    )
    if r.returncode == 0 and r.stdout.strip():
        return Path(r.stdout.strip())
    return Path.cwd()


GO_INSPECTOR_SRC = r'''package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"
)

type Issue struct {
	Kind     string `json:"kind"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	FuncName string `json:"func_name"`
	Message  string `json:"message"`
}

type FuncSummary struct {
	Name           string   `json:"name"`
	Line           int      `json:"line"`
	AssertionCount int      `json:"assertion_count"`
	HasSkip        bool     `json:"has_skip"`
	SkipReasons    []string `json:"skip_reasons"`
	IsEmpty        bool     `json:"is_empty"`
}

type FileReport struct {
	File      string        `json:"file"`
	Issues    []Issue       `json:"issues"`
	Functions []FuncSummary `json:"functions"`
}

func isTestSignature(decl *ast.FuncDecl) bool {
	if decl.Name == nil {
		return false
	}
	name := decl.Name.Name
	if name == "TestMain" {
		return false
	}
	if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Fuzz") {
		return true
	}
	if decl.Type != nil && decl.Type.Params != nil {
		for _, param := range decl.Type.Params.List {
			ts := typeStr(param.Type)
			if ts == "*testing.T" || ts == "*testing.B" || ts == "*testing.F" || ts == "testing.TB" {
				return true
			}
		}
	}
	return false
}

func typeStr(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + typeStr(t.X)
	case *ast.SelectorExpr:
		return typeStr(t.X) + "." + t.Sel.Name
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

func formatNode(fset *token.FileSet, node ast.Node) string {
	if node == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

func isAssertionCall(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		sel := fun.Sel.Name
		if sel == "Errorf" || sel == "Fatalf" || sel == "Fatal" || sel == "Error" || sel == "Fail" || sel == "FailNow" {
			return true
		}
		recv := typeStr(fun.X)
		if recv == "assert" || recv == "require" || recv == "is" || recv == "check" {
			return true
		}
	case *ast.Ident:
		name := strings.ToLower(fun.Name)
		if strings.HasPrefix(name, "assert") || strings.HasPrefix(name, "require") || strings.HasPrefix(name, "check") || strings.HasPrefix(name, "verify") || strings.HasPrefix(name, "must") {
			return true
		}
	}
	for _, arg := range call.Args {
		if ident, ok := arg.(*ast.Ident); ok && (ident.Name == "t" || ident.Name == "b" || ident.Name == "f" || ident.Name == "tb") {
			return true
		}
	}
	return false
}

func isTautological(fset *token.FileSet, call *ast.CallExpr) (bool, string) {
	if call == nil {
		return false, ""
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		pkg := typeStr(sel.X)
		fn := sel.Sel.Name
		if pkg == "assert" || pkg == "require" {
			if (fn == "True" || fn == "Truef") && len(call.Args) >= 2 {
				if ident, ok := call.Args[1].(*ast.Ident); ok && ident.Name == "true" {
					return true, "assert.True(..., true) is tautological"
				}
			}
			if (fn == "False" || fn == "Falsef") && len(call.Args) >= 2 {
				if ident, ok := call.Args[1].(*ast.Ident); ok && ident.Name == "false" {
					return true, "assert.False(..., false) is tautological"
				}
			}
			if (fn == "Nil" || fn == "Nilf") && len(call.Args) >= 2 {
				if ident, ok := call.Args[1].(*ast.Ident); ok && ident.Name == "nil" {
					return true, "assert.Nil(..., nil) is tautological"
				}
			}
			if (fn == "NoError" || fn == "NoErrorf") && len(call.Args) >= 2 {
				if ident, ok := call.Args[1].(*ast.Ident); ok && ident.Name == "nil" {
					return true, "assert.NoError(..., nil) is tautological"
				}
			}
			if (fn == "Equal" || fn == "Equalf" || fn == "Same" || fn == "Samef") && len(call.Args) >= 3 {
				arg1 := formatNode(fset, call.Args[1])
				arg2 := formatNode(fset, call.Args[2])
				if arg1 != "" && arg1 == arg2 {
					return true, fmt.Sprintf("assert.%s(%s, %s) compares identical expressions", fn, arg1, arg2)
				}
			}
		}
	}
	return false, ""
}

func isSkipCall(call *ast.CallExpr) (bool, string) {
	if call == nil {
		return false, ""
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if sel.Sel.Name == "Skip" || sel.Sel.Name == "Skipf" || sel.Sel.Name == "SkipNow" {
			reason := ""
			if len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok {
					reason = strings.Trim(lit.Value, "\"")
				}
			}
			return true, reason
		}
	}
	return false, ""
}

func inspectFunc(fset *token.FileSet, filename string, decl *ast.FuncDecl) (FuncSummary, []Issue) {
	summary := FuncSummary{
		Name: decl.Name.Name,
		Line: fset.Position(decl.Pos()).Line,
	}
	var issues []Issue

	if decl.Body == nil || len(decl.Body.List) == 0 {
		summary.IsEmpty = true
		issues = append(issues, Issue{
			Kind:     "empty_test",
			File:     filename,
			Line:     summary.Line,
			FuncName: summary.Name,
			Message:  fmt.Sprintf("test function %s has an empty body", summary.Name),
		})
		return summary, issues
	}

	assertions := 0
	hasSkip := false
	var skipReasons []string

	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if isAssertionCall(node) {
				assertions++
			}
			if taut, msg := isTautological(fset, node); taut {
				issues = append(issues, Issue{
					Kind:     "tautological_assertion",
					File:     filename,
					Line:     fset.Position(node.Pos()).Line,
					FuncName: summary.Name,
					Message:  msg,
				})
			}
			if skip, reason := isSkipCall(node); skip {
				hasSkip = true
				skipReasons = append(skipReasons, reason)
				line := fset.Position(node.Pos()).Line
				if strings.TrimSpace(reason) == "" && node.Fun.(*ast.SelectorExpr).Sel.Name != "SkipNow" {
					issues = append(issues, Issue{
						Kind:     "empty_skip_reason",
						File:     filename,
						Line:     line,
						FuncName: summary.Name,
						Message:  fmt.Sprintf("%s at line %d has empty or missing reason", node.Fun.(*ast.SelectorExpr).Sel.Name, line),
					})
				}
			}
			// Check empty subtests
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" {
				for _, arg := range node.Args {
					if fnLit, ok := arg.(*ast.FuncLit); ok {
						if fnLit.Body == nil || len(fnLit.Body.List) == 0 {
							issues = append(issues, Issue{
								Kind:     "empty_subtest",
								File:     filename,
								Line:     fset.Position(fnLit.Pos()).Line,
								FuncName: summary.Name,
								Message:  fmt.Sprintf("subtest in %s has an empty body", summary.Name),
							})
						}
					}
				}
			}
		}
		return true
	})

	summary.AssertionCount = assertions
	summary.HasSkip = hasSkip
	summary.SkipReasons = skipReasons

	if strings.HasPrefix(summary.Name, "Test") && assertions == 0 && !hasSkip {
		issues = append(issues, Issue{
			Kind:     "zero_assertions",
			File:     filename,
			Line:     summary.Line,
			FuncName: summary.Name,
			Message:  fmt.Sprintf("test function %s contains zero assertions or verification statements", summary.Name),
		})
	}

	return summary, issues
}

func inspectFile(filename string) (FileReport, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		return FileReport{File: filename}, err
	}

	report := FileReport{File: filename}
	for _, decl := range node.Decls {
		fnDecl, ok := decl.(*ast.FuncDecl)
		if !ok || !isTestSignature(fnDecl) {
			continue
		}
		summary, issues := inspectFunc(fset, filename, fnDecl)
		report.Functions = append(report.Functions, summary)
		report.Issues = append(report.Issues, issues...)
	}

	return report, nil
}

func main() {
	var files []string
	for _, arg := range os.Args[1:] {
		if arg == "--" {
			continue
		}
		files = append(files, arg)
	}
	if len(files) == 0 {
		fmt.Println("[]")
		return
	}

	var reports []FileReport
	for _, file := range files {
		rep, err := inspectFile(file)
		if err != nil {
			reports = append(reports, FileReport{
				File: file,
				Issues: []Issue{{
					Kind:     "parse_error",
					File:     file,
					Line:     1,
					FuncName: "",
					Message:  fmt.Sprintf("go parse error: %v", err),
				}},
			})
			continue
		}
		reports = append(reports, rep)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(reports); err != nil {
		fmt.Fprintf(os.Stderr, "json encode error: %v\n", err)
		os.Exit(1)
	}
}
'''


def run_go_inspector(files: list[Path]) -> list[dict]:
    if not files:
        return []

    with tempfile.NamedTemporaryFile(suffix=".go", mode="w", encoding="utf-8", delete=False) as tf:
        tf.write(GO_INSPECTOR_SRC)
        tf_name = tf.name

    try:
        cmd = ["go", "run", tf_name, "--"] + [str(f.resolve()) for f in files]
        proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
        if proc.returncode != 0 and not proc.stdout.strip():
            print(f"Error running Go AST inspector: {proc.stderr}", file=sys.stderr)
            return []
        try:
            return json.loads(proc.stdout)
        except json.JSONDecodeError as exc:
            print(f"Failed to decode inspector output: {exc}\nOutput: {proc.stdout}", file=sys.stderr)
            return []
    finally:
        try:
            os.unlink(tf_name)
        except OSError:
            pass


def load_skip_policy(root: Path) -> dict:
    policy_file = root / ".mivia" / "policy" / "test-skips.json"
    if not policy_file.is_file():
        return {}
    try:
        return json.loads(policy_file.read_text(encoding="utf-8"))
    except Exception:
        return {}


def get_git_diff_added_skips(diff_args: list[str], root: Path) -> list[tuple[str, int, str]]:
    cmd = ["git", "diff", "-U0"] + diff_args + ["--", "*_test.go"]
    res = subprocess.run(cmd, cwd=root, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        return []

    added_skips: list[tuple[str, int, str]] = []
    current_file = ""
    current_line = 0

    for line in res.stdout.splitlines():
        if line.startswith("+++ b/"):
            current_file = line[6:]
        elif line.startswith("@@"):
            m = re.search(r"\+(\d+)", line)
            if m:
                current_line = int(m.group(1))
        elif line.startswith("+") and not line.startswith("+++"):
            content = line[1:].strip()
            if re.search(r"\bt\.Skip(f|Now)?\(", content):
                added_skips.append((current_file, current_line, content))
            current_line += 1
        elif not line.startswith("-"):
            current_line += 1

    return added_skips


def check_paths(target_files: list[Path], root: Path, diff_args: list[str] | None = None) -> tuple[list[str], int]:
    reports = run_go_inspector(target_files)
    violations: list[str] = []

    for rep in reports:
        for issue in (rep.get("issues") or []):
            rel_file = str(Path(issue["file"]).relative_to(root)) if Path(issue["file"]).is_absolute() and str(issue["file"]).startswith(str(root)) else issue["file"]
            violations.append(f"{rel_file}:{issue['line']}: [{issue['kind']}] {issue['message']}")

    if diff_args is not None:
        policy = load_skip_policy(root)
        known_skips = policy.get("knownSkips", {})
        added_skips = get_git_diff_added_skips(diff_args, root)
        for file_path, line, content in added_skips:
            file_entries = known_skips.get(file_path, [])
            matched = False
            for entry in file_entries:
                reason = entry.get("reason", "")
                if reason and reason in content:
                    matched = True
                    break
            if not matched:
                violations.append(
                    f"{file_path}:{line}: [unreviewed_test_skip] newly added t.Skip in diff without matching entry in .mivia/policy/test-skips.json: {content}"
                )

    total_funcs = sum(len(r.get("functions") or []) for r in reports)
    return violations, total_funcs


def main():
    root = repo_root()
    parser = argparse.ArgumentParser(description="Go test quality gate.")
    parser.add_argument("--staged", action="store_true", help="Inspect staged test files vs HEAD")
    parser.add_argument("--base", help="Base git ref")
    parser.add_argument("--tip", default="HEAD", help="Tip git ref (default HEAD)")
    parser.add_argument("--all", action="store_true", help="Inspect all tracked Go test files")
    parser.add_argument("--worktree", action="store_true", help="Inspect all Go test files on disk")
    parser.add_argument("--paths", nargs="+", help="Inspect explicit paths")
    args = parser.parse_args()

    target_files: list[Path] = []
    diff_args: list[str] | None = None

    if args.staged:
        diff_args = ["--cached"]
        r = subprocess.run(
            ["git", "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", "*_test.go"],
            cwd=root, capture_output=True, text=True, check=False,
        )
        for f in r.stdout.strip("\0").split("\0"):
            if f:
                target_files.append(root / f)
    elif args.base:
        tip = args.tip
        diff_args = [f"{args.base}..{tip}"]
        r = subprocess.run(
            ["git", "diff", "--name-only", "--diff-filter=ACMR", "-z", f"{args.base}..{tip}", "--", "*_test.go"],
            cwd=root, capture_output=True, text=True, check=False,
        )
        for f in r.stdout.strip("\0").split("\0"):
            if f:
                target_files.append(root / f)
    elif args.paths:
        for p in args.paths:
            path = Path(p)
            if not path.is_absolute():
                path = root / path
            if path.is_file():
                target_files.append(path)
            elif path.is_dir():
                target_files.extend(path.rglob("*_test.go"))
    elif args.all:
        r = subprocess.run(
            ["git", "ls-files", "-z", "*_test.go"],
            cwd=root, capture_output=True, text=True, check=False,
        )
        for f in r.stdout.strip("\0").split("\0"):
            if f:
                target_files.append(root / f)
    elif args.worktree:
        skip_dirs = {".git", "vendor", "node_modules", "testdata"}
        for p in root.rglob("*_test.go"):
            if not any(part in skip_dirs for part in p.parts):
                target_files.append(p)
    else:
        # Default: staged if git repo
        diff_args = ["--cached"]
        r = subprocess.run(
            ["git", "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", "*_test.go"],
            cwd=root, capture_output=True, text=True, check=False,
        )
        for f in r.stdout.strip("\0").split("\0"):
            if f:
                target_files.append(root / f)

    if not target_files:
        print("check_test_quality: no test files in scope")
        return

    violations, total_funcs = check_paths(target_files, root, diff_args)

    if violations:
        print(f"FAIL: {len(violations)} test quality violation(s) in {len(target_files)} file(s) ({total_funcs} functions):")
        for v in sorted(violations):
            print(f"  - {v}")
        print("Fix: ensure test functions have assertions, non-empty bodies, valid skip reasons, and no tautologies.")
        sys.exit(1)

    print(f"OK: {len(target_files)} test file(s) ({total_funcs} functions) pass quality checks")


if __name__ == "__main__":
    main()
