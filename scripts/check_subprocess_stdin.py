#!/usr/bin/env python3
"""Refuse a subprocess call that lets a search tool read stdin.

Mechanism this gate exists for: ripgrep and the grep family search STDIN when
they are given no input path and stdin is not a tty. Every non-interactive
context - CI, a git hook, an agent session - has a non-tty stdin, so such a
call either blocks forever (stdin never reaches EOF) or silently returns zero
matches (stdin is /dev/null). A gate script that does this cannot report, and
the empty-result form is worse than the hang because it looks like a pass.

This shipped twice in this repository, copy-pasted, in
scripts/validate_invariants.py and scripts/invariant_coverage.py - both on the
`make verify` chain, so `make verify` itself could wedge indefinitely. The
second site had already grown a fallback that blamed "Homebrew's ripgrep on
macOS" for the empty result, which is what a misdiagnosed mechanism looks like
once it has been rationalised.

The boundary at which the class stops being possible is the call itself: a call
that passes an explicit search path AND stdin=subprocess.DEVNULL cannot read
stdin, on any platform, under any caller.
"""

from __future__ import annotations

import ast
import json
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
POLICY = REPO / ".mivia" / "policy" / "subprocess-stdin.json"
SUBPROCESS_CALLS = {"run", "Popen", "call", "check_call", "check_output"}


def load_policy() -> dict:
    if not POLICY.is_file():
        print(f"check_subprocess_stdin: missing policy {POLICY}", file=sys.stderr)
        sys.exit(1)
    return json.loads(POLICY.read_text(encoding="utf-8"))


def is_subprocess_call(node: ast.Call) -> bool:
    """True for subprocess.run(...) / subprocess.Popen(...) and friends."""
    func = node.func
    if isinstance(func, ast.Attribute) and func.attr in SUBPROCESS_CALLS:
        return isinstance(func.value, ast.Name) and func.value.id == "subprocess"
    return False


def argv_literal(node: ast.Call) -> list[str] | None:
    """The command as a list of string literals, or None if not statically known.

    A non-literal command (a variable, a comprehension) is not analysable here.
    Reporting it would be noise, so it is skipped - stated plainly rather than
    silently, see the summary line.
    """
    if not node.args:
        return None
    first = node.args[0]
    if not isinstance(first, (ast.List, ast.Tuple)):
        return None
    out = []
    for elt in first.elts:
        if isinstance(elt, ast.Constant) and isinstance(elt.value, str):
            out.append(elt.value)
        else:
            return None
    return out


def has_stdin_kwarg(node: ast.Call) -> bool:
    return any(kw.arg == "stdin" for kw in node.keywords)


def has_path_argument(argv: list[str]) -> bool:
    """True if the command names something to read besides stdin.

    Flags carry values (rg -g '*_test.go'), so a bare "looks like not a flag"
    test would accept the pattern or a flag's value as a path. Only a trailing
    argument that is not itself a flag value counts, which in practice means
    the last element when it is not preceded by a value-taking flag. Being
    conservative here is deliberate: a false "has a path" reading would let the
    defect through, so anything ambiguous is treated as having NO path.
    """
    if len(argv) < 2:
        return False
    last = argv[-1]
    if last.startswith("-"):
        return False
    prev = argv[-2]
    # The last token is the value of a flag, not a path.
    if prev.startswith("-") and len(prev) > 1:
        return False
    return True


def main() -> int:
    policy = load_policy()
    tools = set(policy.get("stdinConsumingTools", []))
    exempt = policy.get("exempt", {})
    violations: list[str] = []
    scanned = 0
    skipped_dynamic = 0

    for path in sorted(REPO.glob("scripts/*.py")):
        rel = str(path.relative_to(REPO))
        try:
            tree = ast.parse(path.read_text(encoding="utf-8"), filename=rel)
        except SyntaxError as exc:
            print(f"check_subprocess_stdin: cannot parse {rel}: {exc}", file=sys.stderr)
            return 1
        scanned += 1
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call) or not is_subprocess_call(node):
                continue
            argv = argv_literal(node)
            if argv is None:
                skipped_dynamic += 1
                continue
            if not argv or argv[0] not in tools:
                continue
            where = f"{rel}:{node.lineno}"
            if where in exempt:
                continue
            problems = []
            if not has_path_argument(argv):
                problems.append("no explicit search path (it will read stdin)")
            if not has_stdin_kwarg(node):
                problems.append("no stdin= argument (pass stdin=subprocess.DEVNULL)")
            if problems:
                violations.append(f"  - {where}: {argv[0]}: " + "; ".join(problems))

    if violations:
        print(f"FAIL: {len(violations)} stdin-reading subprocess call(s) found:")
        print("\n".join(violations))
        print(
            "\nwhy: these tools search stdin when given no path and stdin is not a\n"
            "tty, so the call blocks forever or returns zero matches depending on\n"
            "what stdin happens to be. Pass an explicit path AND\n"
            "stdin=subprocess.DEVNULL. Policy: .mivia/policy/subprocess-stdin.json"
        )
        return 1

    note = f", {skipped_dynamic} non-literal command(s) not analysable" if skipped_dynamic else ""
    print(f"check_subprocess_stdin: ok ({scanned} script(s) scanned{note})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
