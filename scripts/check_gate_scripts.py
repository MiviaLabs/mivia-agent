#!/usr/bin/env python3
"""Hold every invoked gate script to being runnable and self-naming.

A script becomes a gate the moment the Makefile or a Git hook invokes it. Two
defects at that boundary make a gate report success it never produced, and
both shipped in this repository:

1. verify_skill_tree.py defined its checks and no entry point. `python3
   scripts/verify_skill_tree.py` executed the imports, printed nothing and
   exited 0. An operator sent there by a failure saw a script that passes.

2. verify_common.fail hard-coded one gate's name, so every verify_skill_tree
   failure printed "verify_agent_config:" and sent that operator to a third
   script that also passes.

Both are the class this repository keeps paying for: a check that reports on a
proxy rather than the property it asserts. The invocation list is derived from
the Makefile and the hooks, never hand-maintained, because a hand-maintained
list of a discovered set is the same defect one level up.
"""

from __future__ import annotations

import ast
import functools
import re
from pathlib import Path

from verify_common import ROOT, rel_to_root
from verify_common import fail as _fail

fail = functools.partial(_fail, prefix="check_gate_scripts")


# Files that invoke gate scripts. A script named here is a gate; a script
# nobody invokes is a helper module and is not held to these rules.
INVOKER_GLOBS = ("Makefile", ".githooks/*", "scripts/git-hooks/*")

# Every tree the sweeps walk. scripts/*.py alone missed scripts/git-hooks/.
SCRIPT_GLOBS = ("scripts/*.py", "scripts/git-hooks/*")

# `python3 <path>`, with or without a "$VAR"/ or $VAR/ prefix. The old pattern
# accepted only $STAGED_ROOT and only scripts/<name>.py, so the three Python
# gates under scripts/git-hooks/ (file-size-check, check-commit-subject,
# strip_coauthor.py - two of them extension-less and hyphenated) and every
# "$ROOT/..." invocation were invisible. One of those, file-size-check, is
# invoked by both pre-commit and pre-push and could lose its entry point with
# this gate green: the exact defect this file exists to stop.
# Two shapes count as invoking a gate: through an interpreter, and directly
# by path (the hooks rely on the shebang). Both may carry a "$VAR"/, $VAR/ or
# ${VAR}/ root prefix, and the interpreter may carry flags.
_ROOT_PREFIX = r"(?:[\"']?\$\{?[A-Za-z_][A-Za-z0-9_]*\}?[\"']?/)?"
_SEG = r"[A-Za-z0-9_][A-Za-z0-9_.-]*"
_PATH = r"[\"']?(scripts/" + _SEG + r"(?:/" + _SEG + r")*)"

INVOCATION = re.compile(
    r"(?:python3?|\$[A-Za-z_]+|[\"']?\$\{?PYTHON\}?[\"']?)"
    r"(?:\s+-[A-Za-z]+)*\s+" + _ROOT_PREFIX + _PATH
)

# A gate invoked straight by path, e.g. `"$ROOT"/scripts/git-hooks/pre-commit`.
# A command position: start of line (optional make @/- prefix), or after a
# shell separator. A bare "(?<=[\s"'])" matched ANY scripts/... token,
# including one sitting in a comment or a Makefile prose line - a comment
# naming a deleted script then failed the gate with a false "does not exist".
_COMMAND_START = r"(?:^[\t ]*[@-]*|[;&|(]\s*|&&\s*|\bthen\s+|\bdo\s+|\bexec\s+)"
DIRECT_INVOCATION = re.compile(_COMMAND_START + r"[\"']?" + _ROOT_PREFIX + _PATH, re.M)

# A file is a Python gate because it runs under python3, not because its name
# ends in .py: two of the hook-directory gates carry no extension.
PY_SHEBANG = re.compile(r"^#!.*\bpython3?\b")

GUARD = re.compile(r"^if __name__ == [\"']__main__[\"']:", re.M)
IMPORTS_COMMON = re.compile(r"^(from verify_common import|import verify_common)", re.M)
# `from verify_common import ROOT` reaches no fail(), and a module that defines
# its own fail() borrows nothing. Match the imported NAME, not the bare word.
def borrows_common_fail(body: str) -> bool:
    """True when this module actually reaches verify_common.fail.

    Parsed with ast, not regex. A line-anchored regex only captured the
    opening line of a `from verify_common import (...)` block, so the
    parenthesized multi-line form was invisible - the exact shape this
    file's own scripts use nowhere yet, but a future gate could. The same
    regex also matched a docstring or comment that happened to start a line
    with "from verify_common import fail", which is not an import at all.
    """
    try:
        tree = ast.parse(body)
    except SyntaxError:
        return False
    for node in ast.walk(tree):
        if isinstance(node, ast.ImportFrom) and node.module == "verify_common":
            if any(alias.name == "fail" for alias in node.names):
                return True
        if isinstance(node, ast.Attribute) and node.attr == "fail":
            target = node.value
            if isinstance(target, ast.Name) and target.id == "verify_common":
                return True
    return False
DEFINES_MAIN = re.compile(r"^def main\(", re.M)

# The one module allowed to take verify_common.fail's default prefix, because
# the default names it. Derived from the default itself, not chosen by hand.
DEFAULT_PREFIX_OWNER = "verify_agent_config"


def invoked_scripts(root: Path) -> dict[str, set[str]]:
    """Every scripts/*.py the Makefile or a hook invokes, and who invokes it."""
    found: dict[str, set[str]] = {}
    for glob_pattern in INVOKER_GLOBS:
        for invoker in sorted(root.glob(glob_pattern)):
            if not invoker.is_file():
                continue
            body = invoker.read_text(encoding="utf-8", errors="replace")
            no_comments = "\n".join(
                line for line in body.split("\n") if not line.lstrip().startswith("#")
            )
            for pattern in (INVOCATION, DIRECT_INVOCATION):
                for match in pattern.finditer(no_comments):
                    path = match.group(1).rstrip("\"'")
                    found.setdefault(path, set()).add(rel_to_root(invoker))
    return found


def is_python_gate(path: Path, body: str) -> bool:
    """True when this file runs under python3: by suffix or by shebang."""
    if path.suffix == ".py":
        return True
    return bool(PY_SHEBANG.match(body.split("\n", 1)[0]))


def python_gate_files(root: Path):
    """Every Python file the gate sweeps: scripts/*.py plus the hook gates.

    Selection is by shebang, not by extension: scripts/git-hooks/file-size-check
    and check-commit-subject run under python3 and carry no .py suffix.
    """
    for pattern in SCRIPT_GLOBS:
        for path in root.glob(pattern):
            if not path.is_file():
                continue
            if path.suffix == ".py":
                yield path
                continue
            try:
                head = path.read_text(encoding="utf-8", errors="replace").split("\n", 1)[0]
            except OSError:
                continue
            if PY_SHEBANG.match(head):
                yield path


def check_gate_scripts(root: Path) -> None:
    """Every invoked script must exist and must run. Takes root so a test can
    exercise this against a fixture."""
    invoked = invoked_scripts(root)
    if not invoked:
        fail(
            "no gate invocations found in the Makefile or the hooks. The "
            "invocation pattern stopped matching, so this gate checks nothing."
        )
    for rel in sorted(invoked):
        callers = ", ".join(sorted(invoked[rel]))
        path = root / rel
        if not path.is_file():
            fail(f"{callers} invokes {rel}, which does not exist.")
        body = path.read_text(encoding="utf-8")
        # The guard rule is a Python rule. Direct-path invocation also finds
        # the bash hooks under scripts/git-hooks, which have no __main__ guard
        # by construction; holding them to it would be a false fire.
        if not is_python_gate(path, body):
            continue
        if not GUARD.search(body):
            fail(
                f"{rel} has no `if __name__ == \"__main__\":` guard, so the "
                f"invocation in {callers} runs the imports, prints nothing and "
                f"exits 0. A gate that cannot report reads as a pass."
            )

    # The invoked set is not the whole set. verify_skill_tree.py is imported by
    # verify_agent_config.py rather than invoked, and it is the script whose
    # missing entry point prompted this gate. Keying only on invocation would
    # have missed it, which is the proxy-for-the-property defect this gate
    # exists to stop. A main() with no guard is dead code in every case.
    for path in sorted(python_gate_files(root)):
        body = path.read_text(encoding="utf-8")
        if DEFINES_MAIN.search(body) and not GUARD.search(body):
            fail(
                f"{rel_to_root(path)} defines main() with no "
                f"`if __name__ == \"__main__\":` guard, so running it prints "
                f"nothing and exits 0."
            )

    # A gate that borrows the shared failure path must report under its own
    # name, or its failures send an operator to a script that passes.
    for path in sorted(python_gate_files(root)):
        body = path.read_text(encoding="utf-8")
        if not IMPORTS_COMMON.search(body):
            continue
        stem = path.stem
        if stem == DEFAULT_PREFIX_OWNER:
            continue
        # Key on reaching the function, not on how it was imported. The rule
        # once required the bare `from verify_common import fail` form, which
        # no file in the tree uses, and `import verify_common` bypassed it
        # entirely. `binds` is matched on the partial application itself, not
        # as a loose substring: the string prefix="<stem>" in a comment or a
        # docstring used to satisfy it.
        if not borrows_common_fail(body):
            continue
        if not re.search(
            rf"functools\.partial\(\s*[A-Za-z_.]*fail\s*,\s*prefix=[\"']{re.escape(stem)}[\"']",
            body,
        ):
            fail(
                f"{rel_to_root(path)} imports verify_common.fail without "
                f"binding prefix=\"{stem}\", so its failures name a different "
                f"script and send an operator to a gate that passes."
            )


def main() -> None:
    check_gate_scripts(ROOT)
    print("check_gate_scripts: ok")


if __name__ == "__main__":
    main()
