#!/usr/bin/env python3
"""Enforce the internal import-edge policy (anti-spaghetti for agents).

An edge is a (from, to) pair of module packages where `from`'s non-test
`Imports` list names `to`. Edges are deduplicated per package pair (a
package importing another through several files still counts once).

Rules, in order:
  1. `deny` wins unconditionally: a deny-listed edge fails even when the
     same pair also appears in `allow` (resilience against a stale or
     hand-edited baseline row).
  2. Every remaining edge must appear in `allow[from]`; an edge missing
     from `allow` fails ("declare the import before the code lands",
     mirroring `mivia-ai-sdk`'s `scripts/check_deps.py`).
  3. The total edge count must not exceed `edgeCap`, even if every edge
     is individually allowed.

Policy: .mivia/policy/import-layers.json
Pattern: .mivia/policy/go-structure.json (JSON policy + baseline/grandfather)

Modes:
  (default)   check the current tree against the committed policy
  --generate  regenerate the `allow` map from the current tree in place,
              preserving `description`, `edgeCap`, and `deny`

Exit codes:
  0 = OK
  1 = policy violation (edge outside allow, deny hit, or cap exceeded)
  2 = usage / malformed policy error (fail closed, never a silent pass)
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
POLICY_PATH = ROOT / ".mivia" / "policy" / "import-layers.json"
GO_MOD_PATH = ROOT / "go.mod"

MODULE_RE = re.compile(r"^module\s+(\S+)\s*$", re.MULTILINE)


class PolicyError(Exception):
    """Malformed policy JSON. Callers must fail closed on this, not pass."""


def fail(msg: str, *, code: int = 1) -> None:
    print(f"check_import_layers: {msg}", file=sys.stderr)
    raise SystemExit(code)


def module_path() -> str:
    if not GO_MOD_PATH.is_file():
        fail(f"missing {GO_MOD_PATH.relative_to(ROOT)}", code=2)
    text = GO_MOD_PATH.read_text(encoding="utf-8")
    m = MODULE_RE.search(text)
    if not m:
        fail("go.mod has no module directive", code=2)
    return m.group(1)


def load_policy(path: Path = POLICY_PATH) -> dict[str, Any]:
    """Load and validate the policy shape. Raises PolicyError on any
    structural problem so callers fail closed rather than silently
    passing with a partial/defaulted policy."""
    if not path.is_file():
        raise PolicyError(f"missing policy {path}")
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise PolicyError(f"invalid JSON in {path}: {exc}") from exc

    if not isinstance(raw, dict):
        raise PolicyError(f"{path}: top level must be an object")

    if "edgeCap" not in raw:
        raise PolicyError(f"{path}: missing required field 'edgeCap'")
    edge_cap = raw["edgeCap"]
    if isinstance(edge_cap, bool) or not isinstance(edge_cap, int):
        raise PolicyError(f"{path}: 'edgeCap' must be an integer, got {edge_cap!r}")

    if "deny" not in raw:
        raise PolicyError(f"{path}: missing required field 'deny'")
    deny_raw = raw["deny"]
    if not isinstance(deny_raw, list):
        raise PolicyError(f"{path}: 'deny' must be a list, got {type(deny_raw).__name__}")
    deny: set[tuple[str, str]] = set()
    for i, entry in enumerate(deny_raw):
        if not isinstance(entry, dict):
            raise PolicyError(f"{path}: deny[{i}] must be an object")
        frm, to = entry.get("from"), entry.get("to")
        if not isinstance(frm, str) or not isinstance(to, str):
            raise PolicyError(f"{path}: deny[{i}] must have string 'from' and 'to'")
        if not isinstance(entry.get("reason"), str) or not entry.get("reason"):
            raise PolicyError(f"{path}: deny[{i}] ({frm} -> {to}) needs a non-empty 'reason'")
        deny.add((frm, to))

    if "allow" not in raw:
        raise PolicyError(f"{path}: missing required field 'allow'")
    allow_raw = raw["allow"]
    if not isinstance(allow_raw, dict):
        raise PolicyError(f"{path}: 'allow' must be an object, got {type(allow_raw).__name__}")
    allow: dict[str, set[str]] = {}
    for pkg, targets in allow_raw.items():
        if not isinstance(targets, list) or not all(isinstance(t, str) for t in targets):
            raise PolicyError(f"{path}: allow[{pkg!r}] must be a list of strings")
        allow[pkg] = set(targets)

    if "description" not in raw or not isinstance(raw["description"], str):
        raise PolicyError(f"{path}: missing or non-string 'description'")

    return {
        "description": raw["description"],
        "edgeCap": edge_cap,
        "deny": deny,
        "allow": allow,
        "raw": raw,
    }


def _go_list_json(root: Path) -> list[dict[str, Any]]:
    r = subprocess.run(
        ["go", "list", "-json", "./..."],
        cwd=root,
        capture_output=True,
        text=True,
        check=False,
    )
    if r.returncode != 0:
        fail(f"go list -json ./... failed:\n{r.stderr}", code=2)
    decoder = json.JSONDecoder()
    text = r.stdout
    pkgs: list[dict[str, Any]] = []
    idx = 0
    length = len(text)
    while idx < length:
        stripped = text[idx:].lstrip()
        if not stripped:
            break
        idx = length - len(stripped)
        obj, end = decoder.raw_decode(text, idx)
        pkgs.append(obj)
        idx = end
    return pkgs


def compute_edges(root: Path = ROOT) -> set[tuple[str, str]]:
    """Return the deduplicated (from, to) package-pair edge set for the
    whole module: both sides package-path-relative to the module root,
    non-test `Imports` field only. Matches the SDK convergence plan's
    counting method exactly (`go list -json ./...`, both sides
    module-prefixed, deduplicated pairs, cmd/* included)."""
    module = module_path()
    prefix = module + "/"
    edges: set[tuple[str, str]] = set()
    for pkg in _go_list_json(root):
        imp_path = pkg.get("ImportPath", "")
        if imp_path != module and not imp_path.startswith(prefix):
            continue
        frm = imp_path[len(prefix):] if imp_path.startswith(prefix) else ""
        for imp in pkg.get("Imports") or []:
            if imp != module and not imp.startswith(prefix):
                continue
            to = imp[len(prefix):] if imp.startswith(prefix) else ""
            if to == frm:
                continue
            edges.add((frm, to))
    return edges


def check(edges: set[tuple[str, str]], policy: dict[str, Any]) -> list[str]:
    """Return policy violation strings; empty means the gate passes."""
    problems: list[str] = []
    deny: set[tuple[str, str]] = policy["deny"]
    allow: dict[str, set[str]] = policy["allow"]

    for frm, to in sorted(edges):
        if (frm, to) in deny:
            problems.append(f"{frm} -> {to}: denied edge present (deny wins over allow)")
            continue
        if to not in allow.get(frm, set()):
            problems.append(
                f"{frm} -> {to}: not declared in .mivia/policy/import-layers.json 'allow'; "
                "declare it before the code lands"
            )

    edge_cap = policy["edgeCap"]
    if len(edges) > edge_cap:
        problems.append(
            f"edge count {len(edges)} exceeds edgeCap {edge_cap} "
            "(cap is enforced even if every edge is individually allowed)"
        )
    return problems


def run_generate(path: Path = POLICY_PATH) -> None:
    """Regenerate 'allow' from the current tree in place, preserving
    'description', 'edgeCap', and 'deny'. Used to freeze the starting
    baseline and, per the plan, to re-freeze it after Item 4 lands."""
    if not path.is_file():
        fail(f"cannot --generate: missing policy {path}", code=2)
    raw = json.loads(path.read_text(encoding="utf-8"))
    edges = compute_edges()
    allow: dict[str, list[str]] = {}
    for frm, to in edges:
        allow.setdefault(frm, []).append(to)
    for targets in allow.values():
        targets.sort()
    raw["allow"] = {pkg: allow[pkg] for pkg in sorted(allow)}
    path.write_text(json.dumps(raw, indent=2, sort_keys=False) + "\n", encoding="utf-8")
    print(f"check_import_layers: regenerated allow map ({len(edges)} edges, {len(allow)} packages)")


def run_check() -> int:
    try:
        policy = load_policy()
    except PolicyError as exc:
        fail(str(exc), code=2)
        return 2  # unreachable; fail() raises
    edges = compute_edges()
    problems = check(edges, policy)
    if problems:
        print("\n".join(problems), file=sys.stderr)
        print(
            f"\ncheck_import_layers: {len(problems)} violation(s) against "
            f"{POLICY_PATH.relative_to(ROOT)}.",
            file=sys.stderr,
        )
        return 1
    print(f"check_import_layers: ok ({len(edges)} edges, cap {policy['edgeCap']})")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--generate",
        action="store_true",
        help="regenerate the 'allow' map from the current tree in place",
    )
    args = ap.parse_args()
    if args.generate:
        run_generate()
        return 0
    return run_check()


if __name__ == "__main__":
    sys.exit(main())
