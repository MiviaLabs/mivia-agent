#!/usr/bin/env python3
"""Fail when a scripts/test_*.py runner defines a test it can never run.

Gate tests that are defined but never executed are unenforced claims. Two
forms shipped real gaps (18 unenforced tests across the hook-bypass guard
and the commit-msg trailer validation):

1. An explicit call list in main() that a later test was never added to.
2. A test defined below the `if __name__ == "__main__"` guard: it does not
   exist yet when main() runs, so even a discovery scan cannot see it.

A test counts as reachable when EITHER a globals() scan picks it up OR its
name is referenced somewhere outside its own body (a call in main(), or a
module-level registry of callables). Two subtleties the first version of
this gate missed, both proved by planted cases in the contract tests:

- A scan only covers ZERO-parameter tests. Runners filter with
  `inspect.signature(v).parameters == {}`, and a parameterized test called
  with no arguments would raise anyway. Defaulted parameters count as
  parameters: `parameters == {}` is false for them, so the scan skips them
  silently. Such tests must be wired explicitly.
- A scan that filters by NAME (`k not in SKIP`) can hide tests, so the
  mere presence of a scan is not a blanket exemption.

AST-only: no test executes here.
"""

from __future__ import annotations

import argparse
import ast
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
FuncDef = (ast.FunctionDef, ast.AsyncFunctionDef)


def is_main_guard(node: ast.stmt) -> bool:
    if not isinstance(node, ast.If):
        return False
    t = node.test
    return (
        isinstance(t, ast.Compare)
        and isinstance(t.left, ast.Name)
        and t.left.id == "__name__"
        and any(
            isinstance(c, ast.Constant) and c.value == "__main__"
            for c in t.comparators
        )
    )


def test_definitions(tree: ast.Module) -> list[ast.FunctionDef]:
    """Top-level test functions, including ones inside a module-level `if`
    (they land in globals() at runtime just the same)."""
    found: list[ast.FunctionDef] = []
    for node in tree.body:
        if isinstance(node, FuncDef) and node.name.startswith("test_"):
            found.append(node)
        elif isinstance(node, ast.If):
            for inner in ast.walk(node):
                if isinstance(inner, FuncDef) and inner.name.startswith("test_"):
                    found.append(inner)
    return found


def takes_parameters(fn: ast.FunctionDef) -> bool:
    a = fn.args
    return bool(a.args or a.posonlyargs or a.kwonlyargs or a.vararg or a.kwarg)


def referenced_names(tree: ast.Module, tests: list[ast.FunctionDef]) -> set[str]:
    """Every test_ name loaded anywhere outside the test bodies themselves:
    a call in main(), or a module-level `TESTS = [test_a, test_b]`."""
    own_bodies = {id(n) for fn in tests for n in ast.walk(fn)}
    return {
        n.id
        for n in ast.walk(tree)
        if isinstance(n, ast.Name)
        and n.id.startswith("test_")
        and id(n) not in own_bodies
    }


def runner_body(tree: ast.Module) -> list[ast.stmt] | None:
    """main()'s body, or the __main__ guard's body when there is no main()
    (some runners inline the scan there). None when the file is not a
    runner at all."""
    for node in tree.body:
        if isinstance(node, FuncDef) and node.name == "main":
            return node.body
    for node in tree.body:
        if is_main_guard(node):
            return node.body
    return None


def has_name_filter(body: list[ast.stmt]) -> bool:
    """A membership test inside the scan's condition can exclude tests by
    name, so the scan is not proof of coverage."""
    for node in body:
        for inner in ast.walk(node):
            if isinstance(inner, ast.Compare) and any(
                isinstance(op, (ast.In, ast.NotIn)) for op in inner.ops
            ):
                return True
    return False


def check_runner(path: Path, root: Path) -> list[str]:
    rel = path.relative_to(root)
    src = path.read_text(encoding="utf-8")
    try:
        tree = ast.parse(src)
    except SyntaxError as exc:
        return [f"{rel}: cannot parse ({exc})"]

    problems: list[str] = []
    tests = test_definitions(tree)
    if not tests:
        return problems

    guards = [n for n in tree.body if is_main_guard(n)]
    if guards:
        last = guards[-1]
        below = [t.name for t in tests if t.lineno > last.lineno]
        for name in below:
            problems.append(
                f"{rel}: {name} is defined below the __main__ guard, so it "
                f"does not exist when the runner executes"
            )

    body = runner_body(tree)
    if body is None:
        return problems

    scan_src = "\n".join(ast.dump(n) for n in body)
    has_scan = "globals" in scan_src
    if has_scan and has_name_filter(body):
        problems.append(
            f"{rel}: the globals() scan carries a name filter, which can "
            f"exclude tests silently; select tests by the test_ prefix only"
        )

    referenced = referenced_names(tree, tests)
    for fn in tests:
        if fn.name in referenced:
            continue
        if has_scan and not takes_parameters(fn):
            continue
        why = (
            "takes parameters, so the globals() scan skips it"
            if has_scan
            else "is not referenced by the runner"
        )
        problems.append(f"{rel}: {fn.name} {why} and is never executed")
    return problems


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=str(REPO_ROOT))
    args = ap.parse_args()
    root = Path(args.root).resolve()

    problems: list[str] = []
    for path in sorted((root / "scripts").glob("test_*.py")):
        problems.extend(check_runner(path, root))
    if problems:
        for p in problems:
            print(f"FAIL: {p}", file=sys.stderr)
        print(
            f"check_script_test_reachability: {len(problems)} unreachable "
            f"gate test(s)",
            file=sys.stderr,
        )
        return 1
    print("check_script_test_reachability: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
