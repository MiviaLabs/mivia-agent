#!/usr/bin/env python3
"""Shared helpers for the control-surface gate scripts.

verify_agent_config.py and verify_skill_tree.py both need the repository root
and one failure path. They live here so neither script has to import the other,
which would be a cycle.
"""

from __future__ import annotations

import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def fail(msg: str, prefix: str = "verify_agent_config") -> None:
    """Report a failure and stop.

    Take the prefix so each gate reports under its own name. Every
    verify_skill_tree failure used to print "verify_agent_config:", which sent
    an operator to a script that passes.
    """
    print(f"{prefix}: {msg}", file=sys.stderr)
    raise SystemExit(1)


def rel_to_root(path: Path) -> str:
    """Path relative to the repo root, or the plain path when it is outside.

    The skill checks also run against a fixture directory in the tests. A
    fixture lives outside ROOT, where relative_to raises ValueError.
    """
    try:
        return str(path.relative_to(ROOT))
    except ValueError:
        return str(path)
