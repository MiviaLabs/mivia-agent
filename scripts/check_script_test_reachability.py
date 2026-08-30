#!/usr/bin/env python3
"""Fail when a scripts/test_*.py runner defines a test it can never run.

Two rot forms shipped real gaps (18 unenforced tests across the hook-bypass
guard and the commit-msg trailer validation, plus a branch that appended
gate tests below the runner):

1. An explicit call list in main() that a later test_ function was never
   added to.
2. An `if __name__ == "__main__"` guard that is not the last top-level
   statement: functions defined below it do not exist yet when main()
   runs, so even a globals()-scan runner silently skips them.

The check is AST-only (no test execution) and fail-closed: every
top-level zero-argument test_ function must be reachable, and the
__main__ guard must be the final top-level statement.
"""

from __future__ import annotations

import ast
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


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


def check_runner(path: Path) -> list[str]:
    src = path.read_text(encoding="utf-8")
    tree = ast.parse(src)
    problems: list[str] = []

    funcs = {n.name: n for n in tree.body if isinstance(n, ast.FunctionDef)}
    if "main" not in funcs:
        return problems
    tests = {
        name: n
        for name, n in funcs.items()
        if name.startswith("test_")
    }
    if not tests:
        return problems

    guards = [n for n in tree.body if is_main_guard(n)]
    if guards and tree.body[-1] is not guards[-1]:
        after = [
            n.name
            for n in tree.body
            if isinstance(n, ast.FunctionDef)
            and n.name.startswith("test_")
            and n.lineno > guards[-1].lineno
        ]
        detail = f" (defines below it: {', '.join(after)})" if after else ""
        problems.append(
            f"{path.relative_to(ROOT)}: the __main__ guard is not the last "
            f"top-level statement; definitions below it do not exist when "
            f"main() runs{detail}"
        )

    main = funcs["main"]
    main_src = ast.get_source_segment(src, main) or ""
    if "globals()" in main_src:
        return problems

    referenced = {
        n.id for n in ast.walk(main) if isinstance(n, ast.Name)
    }
    zero_arg = {
        name
        for name, n in tests.items()
        if not (n.args.args or n.args.posonlyargs or n.args.kwonlyargs)
    }
    for name in sorted(zero_arg - referenced):
        problems.append(
            f"{path.relative_to(ROOT)}: {name} is defined but unreachable "
            f"from main()'s explicit call list"
        )
    return problems


def main() -> int:
    problems: list[str] = []
    for path in sorted((ROOT / "scripts").glob("test_*.py")):
        problems.extend(check_runner(path))
    if problems:
        for p in problems:
            print(f"FAIL: {p}", file=sys.stderr)
        print(
            f"check_script_test_reachability: {len(problems)} unreachable "
            f"gate test(s); add them to main() or move the __main__ guard "
            f"to the end of the file",
            file=sys.stderr,
        )
        return 1
    print("check_script_test_reachability: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
