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
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
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
		if sel == "Run" {
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

func isTautological(call *ast.CallExpr) (bool, string) {
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
			if taut, msg := isTautological(node); taut {
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
		case *ast.IfStmt:
			assertions++
		case *ast.DeferStmt:
			assertions++
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: inspect <files...>")
		os.Exit(2)
	}

	fset := token.NewFileSet()
	var reports []FileReport

	for _, file := range os.Args[1:] {
		if !strings.HasSuffix(file, "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			reports = append(reports, FileReport{
				File: file,
				Issues: []Issue{{
					Kind:    "parse_error",
					File:    file,
					Line:    1,
					Message: fmt.Sprintf("failed to parse: %v", err),
				}},
			})
			continue
		}

		rep := FileReport{File: file}
		for _, decl := range node.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && isTestSignature(fn) {
				summary, issues := inspectFunc(fset, file, fn)
				rep.Functions = append(rep.Functions, summary)
				rep.Issues = append(rep.Issues, issues...)
			}
		}
		reports = append(reports, rep)
	}

	data, err := json.Marshal(reports)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(string(data))
}
'''


def run_go_inspector(files: list[Path]) -> list[dict]:
    if not files:
        return []
    with tempfile.NamedTemporaryFile(suffix=".go", mode="w", delete=False) as tf:
        tf.write(GO_INSPECTOR_SRC)
        tf_path = tf.name

    try:
        file_strs = [str(f.resolve()) for f in files if str(f).endswith("_test.go") and f.is_file()]
        if not file_strs:
            return []

        results = []
        batch_size = 100
        for i in range(0, len(file_strs), batch_size):
            batch = file_strs[i : i + batch_size]
            cmd = ["go", "run", tf_path, "--", *batch]
            r = subprocess.run(cmd, capture_output=True, text=True, check=False)
            if r.returncode != 0:
                print(f"check_test_quality: go inspector error: {r.stderr}", file=sys.stderr)
                sys.exit(2)
            results.extend(json.loads(r.stdout))
        return results
    finally:
        if os.path.exists(tf_path):
            os.remove(tf_path)


def load_skip_policy(root: Path | None = None) -> dict:
    if root is None:
        root = repo_root()
    policy_path = root / ".mivia" / "policy" / "test-skips.json"
    if not policy_path.is_file():
        return {"knownSkips": {}}
    try:
        return json.loads(policy_path.read_text(encoding="utf-8"))
    except Exception as e:
        print(f"check_test_quality: invalid policy {policy_path}: {e}", file=sys.stderr)
        sys.exit(2)


def get_git_diff_added_skips(diff_args: list[str], root: Path | None = None) -> list[tuple[str, int, str]]:
    """Returns list of (file, line, content) for added t.Skip calls in git diff."""
    if root is None:
        root = repo_root()
    r = subprocess.run(
        ["git", "diff", *diff_args, "--unified=0", "--", "*_test.go"],
        cwd=root,
        capture_output=True,
        text=True,
        check=False,
    )
    if r.returncode != 0:
        return []

    added_skips = []
    current_file = ""
    current_line = 0

    for line in r.stdout.splitlines():
        if line.startswith("+++ b/"):
            current_file = line[6:]
        elif line.startswith("@@"):
            m = re.search(r"\+(\d+)", line)
            if m:
                current_line = int(m.group(1))
        elif line.startswith("+") and not line.startswith("+++"):
            content = line[1:]
            if re.search(r"\bt\.Skip(f|Now)?\(", content):
                added_skips.append((current_file, current_line, content.strip()))
            current_line += 1
    return added_skips


def run_checks(
    target_files: list[Path],
    diff_args: list[str] | None = None,
    root: Path | None = None,
) -> tuple[list[str], int]:
    """Inspects target test files and returns (violations, total_functions)."""
    if root is None:
        root = repo_root()
    if not target_files:
        return [], 0

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
            if not file_entries:
                violations.append(
                    f"{file_path}:{line}: [unreviewed_test_skip] newly added t.Skip in diff without entry in .mivia/policy/test-skips.json: {content}"
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
        diff_args = [f"{args.base}..{args.tip}"]
        r = subprocess.run(
            ["git", "diff", *diff_args, "--name-only", "--diff-filter=ACMR", "-z", "--", "*_test.go"],
            cwd=root, capture_output=True, text=True, check=False,
        )
        for f in r.stdout.strip("\0").split("\0"):
            if f:
                target_files.append(root / f)
    elif args.paths:
        target_files = [Path(p) if Path(p).is_absolute() else root / p for p in args.paths if p.endswith("_test.go")]
    elif args.all:
        r = subprocess.run(
            ["git", "ls-files", "-z", "--", "cmd/**/*_test.go", "internal/**/*_test.go", "pkg/**/*_test.go"],
            cwd=root, capture_output=True, text=True, check=False,
        )
        for f in r.stdout.strip("\0").split("\0"):
            if f:
                target_files.append(root / f)
        if not target_files:
            target_files = [p for p in root.rglob("*_test.go") if ".git" not in p.parts]
    elif args.worktree:
        target_files = [p for p in root.rglob("*_test.go") if ".git" not in p.parts]
    else:
        target_files = [p for p in root.rglob("*_test.go") if ".git" not in p.parts]

    if not target_files:
        print("check_test_quality: no test files in scope")
        return

    violations, total_funcs = run_checks(target_files, diff_args, root)

    if violations:
        print(f"check_test_quality: {len(violations)} test quality violation(s) found:", file=sys.stderr)
        for v in violations:
            print(f"  {v}", file=sys.stderr)
        sys.exit(1)

    print(f"check_test_quality: OK ({len(target_files)} files, {total_funcs} test functions checked)")



if __name__ == "__main__":
    main()
