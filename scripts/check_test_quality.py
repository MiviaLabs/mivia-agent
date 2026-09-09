#!/usr/bin/env python3
"""AST-based inspection of Go test files for test quality and anti-fake-test prevention.

Mechanizes the checks in verify-code-change/SKILL.md and test-review/SKILL.md:
1. Empty test bodies (no statements).
2. Zero assertions in Test* functions.
3. Tautological assertions (e.g. assert.True(t, true), assert.Equal(t, a, a), if got == got).
4. Unreviewed t.Skip additions in git diffs (must match .mivia/policy/test-skips.json).
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

GO_INSPECTOR_CODE = r'''package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
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

func isAssertionCall(call *ast.CallExpr, tbNames map[string]bool) bool {
	if call == nil {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		sel := fun.Sel.Name
		recv := typeStr(fun.X)
		if sel == "Errorf" || sel == "Fatalf" || sel == "Fatal" || sel == "Error" || sel == "Fail" || sel == "FailNow" {
			return tbNames[recv]
		}
		if recv == "assert" || recv == "require" || recv == "is" || recv == "check" || recv == "testutil" || recv == "checks" {
			return true
		}
	case *ast.Ident:
		name := strings.ToLower(fun.Name)
		if strings.HasPrefix(name, "assert") || strings.HasPrefix(name, "require") || strings.HasPrefix(name, "check") || strings.HasPrefix(name, "verify") || strings.HasPrefix(name, "expect") {
			for _, arg := range call.Args {
				if ident, ok := arg.(*ast.Ident); ok && tbNames[ident.Name] {
					return true
				}
			}
			return false
		}
	}
	return false
}

func isTautologicalCondition(fset *token.FileSet, ifStmt *ast.IfStmt) (bool, string) {
	if ifStmt == nil || ifStmt.Cond == nil {
		return false, ""
	}
	if ident, ok := ifStmt.Cond.(*ast.Ident); ok && ident.Name == "false" {
		return true, "if false condition is dead code/tautology"
	}
	if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok {
		xStr := formatNode(fset, bin.X)
		yStr := formatNode(fset, bin.Y)
		if xStr != "" && xStr == yStr {
			return true, fmt.Sprintf("tautological condition %s %s %s compares identical operands", xStr, bin.Op.String(), yStr)
		}
	}
	return false, ""
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

func isSkipCall(call *ast.CallExpr, tbNames map[string]bool) (bool, string) {
	if call == nil {
		return false, ""
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if sel.Sel.Name == "Skip" || sel.Sel.Name == "Skipf" || sel.Sel.Name == "SkipNow" {
			recv := typeStr(sel.X)
			if tbNames[recv] {
				reason := ""
				if len(call.Args) > 0 {
					if lit, ok := call.Args[0].(*ast.BasicLit); ok {
						reason = strings.Trim(lit.Value, "\"")
					}
				}
				return true, reason
			}
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
			Message:  fmt.Sprintf("test function %s has empty body", summary.Name),
		})
		return summary, issues
	}

	tbNames := map[string]bool{"t": true, "tb": true, "b": true}
	if decl.Type.Params != nil {
		for _, field := range decl.Type.Params.List {
			ts := typeStr(field.Type)
			if strings.Contains(ts, "testing.T") || strings.Contains(ts, "testing.TB") || strings.Contains(ts, "testing.B") || strings.Contains(ts, "testing.F") {
				for _, name := range field.Names {
					tbNames[name.Name] = true
				}
			}
		}
	}

	// Only top-level t.Skip calls directly on the test function body exempt the test from zero assertions
	for _, stmt := range decl.Body.List {
		if exprStmt, ok := stmt.(*ast.ExprStmt); ok {
			if call, ok := exprStmt.X.(*ast.CallExpr); ok {
				if isSkip, reason := isSkipCall(call, tbNames); isSkip {
					summary.HasSkip = true
					if reason != "" {
						summary.SkipReasons = append(summary.SkipReasons, reason)
					}
				}
			}
		}
	}

	assertions := 0
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if fnLit, ok := n.(*ast.FuncLit); ok && fnLit.Type.Params != nil {
			for _, field := range fnLit.Type.Params.List {
				ts := typeStr(field.Type)
				if strings.Contains(ts, "testing.T") || strings.Contains(ts, "testing.TB") || strings.Contains(ts, "testing.B") || strings.Contains(ts, "testing.F") {
					for _, name := range field.Names {
						tbNames[name.Name] = true
					}
				}
			}
		}
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if isTaut, reason := isTautologicalCondition(fset, ifStmt); isTaut {
				issues = append(issues, Issue{
					Kind:     "tautological_assertion",
					File:     filename,
					Line:     fset.Position(ifStmt.Pos()).Line,
					FuncName: summary.Name,
					Message:  reason,
				})
			}
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if isTaut, reason := isTautological(fset, call); isTaut {
				issues = append(issues, Issue{
					Kind:     "tautological_assertion",
					File:     filename,
					Line:     fset.Position(call.Pos()).Line,
					FuncName: summary.Name,
					Message:  reason,
				})
			}
			if isAssertionCall(call, tbNames) {
				assertions++
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" {
				if len(call.Args) == 2 {
					if fnLit, ok := call.Args[1].(*ast.FuncLit); ok {
						if fnLit.Body == nil || len(fnLit.Body.List) == 0 {
							issues = append(issues, Issue{
								Kind:     "empty_subtest",
								File:     filename,
								Line:     fset.Position(call.Pos()).Line,
								FuncName: summary.Name,
								Message:  "t.Run has empty subtest body",
							})
						}
					}
				}
			}
		}
		return true
	})

	summary.AssertionCount = assertions
	if assertions == 0 && !summary.HasSkip && strings.HasPrefix(summary.Name, "Test") {
		issues = append(issues, Issue{
			Kind:     "zero_assertions",
			File:     filename,
			Line:     summary.Line,
			FuncName: summary.Name,
			Message:  fmt.Sprintf("test function %s has zero assertions", summary.Name),
		})
	}

	return summary, issues
}

func main() {
	args := os.Args[1:]
	for len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}

	if len(args) == 0 {
		fmt.Println("[]")
		return
	}

	fset := token.NewFileSet()
	var reports []FileReport

	for _, filename := range args {
		if !strings.HasSuffix(filename, "_test.go") {
			continue
		}

		absPath, err := filepath.Abs(filename)
		if err != nil {
			absPath = filename
		}

		fileNode, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		rep := FileReport{File: absPath}
		for _, decl := range fileNode.Decls {
			if fnDecl, ok := decl.(*ast.FuncDecl); ok && isTestSignature(fnDecl) {
				summary, issues := inspectFunc(fset, absPath, fnDecl)
				rep.Functions = append(rep.Functions, summary)
				rep.Issues = append(rep.Issues, issues...)
			}
		}
		reports = append(reports, rep)
	}

	out, err := json.Marshal(reports)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal reports: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
'''


def run_go_inspector(files: list[Path]) -> list[dict]:
    if not files:
        return []

    with tempfile.NamedTemporaryFile("w", suffix=".go", delete=False) as tf:
        tf.write(GO_INSPECTOR_CODE)
        tf_name = tf.name

    try:
        cmd = ["go", "run", tf_name, "--"] + [str(f) for f in files]
        res = subprocess.run(cmd, capture_output=True, text=True, check=False)
        if res.returncode != 0:
            print(f"check_test_quality: inspector error: {res.stderr}", file=sys.stderr)
            return []
        out = res.stdout.strip()
        if not out:
            return []
        # `or []` is load-bearing: the Go inspector marshals a nil slice as the
        # JSON literal `null`, which json.loads turns into None, and the caller
        # iterates the result. Any diff that touches Go source without touching
        # a test file produced no reports and therefore crashed this gate with
        # a TypeError instead of passing.
        return json.loads(out) or []
    except Exception as e:
        print(f"check_test_quality: error running go inspector: {e}", file=sys.stderr)
        return []
    finally:
        try:
            os.unlink(tf_name)
        except OSError:
            pass


EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"  # git's empty tree


def committed_range(diff_args: list[str] | None) -> tuple[str, str] | None:
    """Return (base, tip) for a COMMITTED range spelled exactly "A..B".

    Uncommitted shapes (--cached, ["HEAD"]) return None: there the change
    is one unit and load_skip_policy's base-policy comparison applies -
    that is the ENFORCEMENT point, and it cannot be laundered because the
    change has no history yet. A committed range is a review aid instead
    (see check_paths).

    Anything else - three-dot ranges, "A..B..C", empty sides - raises, so
    an unrecognized shape fails loudly instead of silently degrading to a
    wrong base and a wall of bogus violations.
    """
    if not diff_args or len(diff_args) != 1 or ".." not in diff_args[0]:
        return None
    spec = diff_args[0]
    if "..." in spec or spec.count("..") != 1:
        raise ValueError(
            f"unsupported range {spec!r}: use exactly BASE..TIP (two dots)"
        )
    base, _, tip = spec.partition("..")
    if not base or not tip:
        raise ValueError(f"unsupported range {spec!r}: both ends are required")
    return base, tip


def policy_at(root: Path, ref: str) -> dict:
    """The skip policy as of ref. A missing or unparseable file yields {},
    i.e. no entry is allowlisted - fail closed."""
    r = subprocess.run(
        ["git", "show", f"{ref}:.mivia/policy/test-skips.json"],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if r.returncode != 0 or not r.stdout.strip():
        return {}
    try:
        return json.loads(r.stdout)
    except Exception:
        return {}


def load_skip_policy(root: Path, diff_args: list[str] | None = None) -> dict:
    policy_file = root / ".mivia" / "policy" / "test-skips.json"
    if not policy_file.is_file():
        return {}

    raw_text = ""
    is_modified_in_diff = False
    if diff_args is not None:
        r = subprocess.run(
            ["git", "diff", *diff_args, "--name-only", "--", ".mivia/policy/test-skips.json"],
            cwd=root, capture_output=True, text=True, check=False,
        )
        if r.returncode == 0 and r.stdout.strip():
            is_modified_in_diff = True
            # Only uncommitted shapes reach here (committed ranges returned
            # above), so the pre-change state is always HEAD.
            base_ref = "HEAD"

            show_res = subprocess.run(
                ["git", "show", f"{base_ref}:.mivia/policy/test-skips.json"],
                cwd=root, capture_output=True, text=True, check=False,
            )
            print(
                "check_test_quality: .mivia/policy/test-skips.json is modified in this diff; "
                "evaluating skips against base policy to prevent same-commit bypass",
                file=sys.stderr,
            )
            if show_res.returncode == 0 and show_res.stdout.strip():
                raw_text = show_res.stdout
            else:
                # File does not exist at base ref or is empty: base policy has zero allowlisted skips
                return {}

    if not is_modified_in_diff:
        raw_text = policy_file.read_text(encoding="utf-8")

    try:
        return json.loads(raw_text)
    except Exception:
        return {}


def get_git_diff_added_skips(diff_args: list[str], root: Path) -> list[tuple[str, int, str]]:
    cmd = ["git", "-c", "core.quotepath=false", "diff", "-U0"] + diff_args + ["--", "*_test.go"]
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
            if re.search(r"\b\w+\.Skip(f|Now)?\(", content):
                added_skips.append((current_file, current_line, content))
            current_line += 1
        elif line.startswith("\\"):
            # "\ No newline at end of file" is a marker, not a context line
            continue
        elif not line.startswith("-"):
            current_line += 1

    return added_skips


def get_git_diff_deleted_tests(diff_args: list[str], root: Path) -> list[tuple[str, str]]:
    cmd = ["git", "-c", "core.quotepath=false", "diff", "-U0"] + diff_args + ["--", "*_test.go"]
    res = subprocess.run(cmd, cwd=root, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        return []

    deleted_tests: list[tuple[str, str]] = []
    current_file = ""

    for line in res.stdout.splitlines():
        if line.startswith("--- a/"):
            current_file = line[6:]
        elif line.startswith("-") and not line.startswith("---"):
            m = re.search(r"^-\s*func\s+(Test\w+)\s*\(", line)
            if m:
                deleted_tests.append((current_file, m.group(1)))

    return deleted_tests


def unit_violations(root: Path, diff_args: list[str], policy: dict) -> list[str]:
    """Evaluate ONE change unit (a staged set, a dirty tree, or a single
    commit) against the policy as it stood BEFORE that unit. An entry added
    inside the unit is invisible here, which is exactly the two-commit
    discipline: the reviewed entry must land first."""
    out: list[str] = []
    known_skips = policy.get("knownSkips", {})
    for file_path, line, content in get_git_diff_added_skips(diff_args, root):
        entries = known_skips.get(file_path, [])
        if not any(e.get("reason") and e["reason"] in content for e in entries):
            out.append(
                f"{file_path}:{line}: [unreviewed_test_skip] newly added skip in diff without matching entry in .mivia/policy/test-skips.json: {content}"
            )
    allowed = set(policy.get("allowedDeletions", []))
    for file_path, test_name in get_git_diff_deleted_tests(diff_args, root):
        if test_name not in allowed:
            out.append(
                f"{file_path}: [deleted_test_function] test function {test_name} was deleted in diff without allowlist in .mivia/policy/test-skips.json (allowedDeletions)"
            )
    return out


def check_paths(target_files: list[Path], root: Path, diff_args: list[str] | None = None) -> tuple[list[str], int]:
    reports = run_go_inspector(target_files)
    violations: list[str] = []

    # One policy per mode. A committed range is a REVIEW of a branch's end
    # state, so it reads the policy at TIP. Every uncommitted shape is the
    # ENFORCEMENT point and reads the policy as of HEAD, so a change cannot
    # approve itself. (Range mode does not re-litigate that: history can be
    # squashed or rewritten, so which commit a change landed in is not a
    # stable property to gate on - and the six ways a same-commit probe was
    # evaded are recorded in this file's git history.)
    rng = committed_range(diff_args) if diff_args is not None else None
    policy = policy_at(root, rng[1]) if rng is not None else load_skip_policy(root, diff_args)
    known_zero_assertions = set(policy.get("knownZeroAssertions", []))

    for rep in reports:
        for issue in (rep.get("issues") or []):
            if issue.get("kind") == "zero_assertions" and issue.get("func_name") in known_zero_assertions:
                continue
            rel_file = str(Path(issue["file"]).relative_to(root)) if Path(issue["file"]).is_absolute() and str(issue["file"]).startswith(str(root)) else issue["file"]
            violations.append(f"{rel_file}:{issue['line']}: [{issue['kind']}] {issue['message']}")

    if diff_args is not None:
        violations.extend(unit_violations(root, diff_args, policy))

    total_funcs = sum(len(r.get("functions") or []) for r in reports)
    return violations, total_funcs


def main() -> int:
    try:
        return run_main()
    except ValueError as exc:
        print(f"check_test_quality: {exc}", file=sys.stderr)
        return 2


def diff_names(root: Path, *rev_args: str) -> list[str]:
    """`git diff --name-only <rev_args>`, raising when git itself fails.

    An unreadable ref must not degrade into an empty file list: that reads
    as "nothing to check" and greens the gate. `make` supplies --base from
    a `git merge-base` substitution, so a missing upstream or an unrelated
    history would otherwise pass silently rather than report it could not
    evaluate anything.
    """
    res = subprocess.run(
        ["git", "-c", "core.quotepath=false", "diff", "--name-only", *rev_args],
        cwd=root, capture_output=True, text=True, check=False,
    )
    if res.returncode != 0:
        detail = res.stderr.strip().splitlines()
        raise ValueError(
            f"git diff --name-only {' '.join(rev_args)} failed "
            f"(exit {res.returncode}): {detail[0] if detail else 'no output'}"
        )
    return [f for f in res.stdout.splitlines() if f.strip()]


def run_main() -> int:
    parser = argparse.ArgumentParser(description="Check Go test quality (anti-fake-test enforcement).")
    parser.add_argument("--staged", action="store_true", help="Inspect staged _test.go files vs HEAD")
    parser.add_argument("--diff", action="store_true", help="Inspect modified _test.go files vs HEAD")
    parser.add_argument("--base", help="Base git ref for diff inspection")
    parser.add_argument("--tip", default="HEAD", help="Tip git ref for diff inspection")
    parser.add_argument("--all", action="store_true", help="Inspect all _test.go files in the repository")
    parser.add_argument("--worktree", action="store_true", help="Inspect all unstaged modified _test.go files")
    parser.add_argument("--paths", nargs="+", help="Explicit _test.go files to inspect")
    args = parser.parse_args()

    r = subprocess.run(["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True, check=False)
    if r.returncode != 0:
        root = Path.cwd()
    else:
        root = Path(r.stdout.strip())

    diff_args = None
    target_files: list[Path] = []

    if args.paths:
        target_files = [Path(p) for p in args.paths]
    elif args.staged:
        diff_args = ["--cached"]
        target_files = [root / f for f in diff_names(root, "--cached") if f.endswith("_test.go")]
        if not target_files:
            # On clean checkout (e.g. CI checkout), inspect HEAD~1..HEAD if available
            rev_check = subprocess.run(["git", "rev-parse", "HEAD~1"], cwd=root, capture_output=True, text=True, check=False)
            if rev_check.returncode == 0:
                diff_args = ["HEAD~1..HEAD"]
                target_files = [root / f for f in diff_names(root, "HEAD~1..HEAD") if f.endswith("_test.go")]
    elif args.diff:
        # Check uncommitted diff vs HEAD first
        target_files = [root / f for f in diff_names(root, "HEAD") if f.endswith("_test.go")]
        diff_args = ["HEAD"]
        if not target_files:
            # On clean working tree, inspect HEAD~1..HEAD if available
            rev_check = subprocess.run(["git", "rev-parse", "HEAD~1"], cwd=root, capture_output=True, text=True, check=False)
            if rev_check.returncode == 0:
                diff_args = ["HEAD~1..HEAD"]
                target_files = [root / f for f in diff_names(root, "HEAD~1..HEAD") if f.endswith("_test.go")]
    elif args.base:
        spec = f"{args.base}..{args.tip}"
        # Parse BEFORE touching git so a malformed range names itself
        # rather than surfacing as a confusing revision error.
        committed_range([spec])
        diff_args = [spec]
        target_files = [root / f for f in diff_names(root, spec) if f.endswith("_test.go")]
    elif args.worktree:
        target_files = [root / f for f in diff_names(root, "HEAD") if f.endswith("_test.go")]
    elif args.all:
        skip_dirs = {".git", "node_modules", "vendor", "testdata"}
        for p in root.rglob("*_test.go"):
            if not (skip_dirs & set(p.relative_to(root).parts)):
                target_files.append(p)
    else:
        # Default behavior: if staged files exist, check staged; otherwise check all
        staged = [root / f for f in diff_names(root, "--cached", "--", "*_test.go")]
        if staged:
            diff_args = ["--cached"]
            target_files = staged
        else:
            skip_dirs = {".git", "node_modules", "vendor", "testdata"}
            for p in root.rglob("*_test.go"):
                if not (skip_dirs & set(p.relative_to(root).parts)):
                    target_files.append(p)

    if not target_files:
        print("check_test_quality: no test files in scope; skipping")
        return 0

    violations, total_funcs = check_paths(target_files, root, diff_args)
    if violations:
        print(f"FAIL: {len(violations)} test quality violation(s) found:")
        for v in violations:
            print(f"  - {v}")
        return 1

    print(f"OK: {len(target_files)} test file(s) ({total_funcs} functions) pass quality checks")
    return 0


if __name__ == "__main__":
    sys.exit(main())
