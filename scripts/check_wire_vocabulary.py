#!/usr/bin/env python3
"""Fail when a prose copy of the mivia.chat.v1 event vocabulary disagrees with
the recorded contract.

The vocabulary is authored once, in internal/chatsync.wireEventSpecs, and
published once, in api/contracts/chat-sessions.v1.json. A Go conformance test
holds those two equal. It cannot see a THIRD copy written in Markdown, and one
existed: docs/product/chat-sync-wire.md listed sixteen types, two of which
(mivia.chat.v1.subagent.started and mivia.chat.v1.subagent.heartbeat) are not
emitted by anything, while omitting mivia.chat.v1.subagent.progress, which is.

That is not a cosmetic doc bug. The API names each SSE frame after the event
type string, so EventSource.onmessage never fires and a browser client calls
addEventListener once per type it believes in. A client written from that doc
would have waited on two frames that never arrive and dropped one that does.

Two rules, because the drift had both shapes:

1. INVENTION -- every mivia.chat.v1.<name> token in any scanned file must be a
   recorded type. Catches a name nothing emits, anywhere in the repository.
2. OMISSION -- a doc listed in enumeratingDocs claims to enumerate the
   vocabulary, so the set of types it names must EQUAL the recorded set.
   Catches a type a reader would never learn exists.

It also checks the contract file against itself: knownTypes and the keys of
events.types must be the same set, so the artifact a web client vendors cannot
name a type in one half and not the other.

Policy: .mivia/policy/wire-vocabulary.json
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
POLICY_PATH = REPO_ROOT / ".mivia" / "policy" / "wire-vocabulary.json"

# Requires a non-empty segment after the version, so the bare prefix
# "mivia.chat.v1." and the glob "mivia.chat.v1.*" are prose, not claims.
TOKEN = re.compile(r"mivia\.chat\.v1\.[a-z][a-z0-9]*(?:\.[a-z0-9]+)*")


def load_policy(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        sys.exit(f"check_wire_vocabulary: missing policy file {path}")
    except json.JSONDecodeError as exc:
        sys.exit(f"check_wire_vocabulary: {path} is not valid JSON: {exc}")


def recorded_types(root: Path, policy: dict) -> tuple[set[str], list[str]]:
    """Return the recorded type set and any problem found inside the contract."""
    path = root / policy["contract"]
    try:
        contract = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        sys.exit(f"check_wire_vocabulary: missing contract file {path}")
    except json.JSONDecodeError as exc:
        sys.exit(f"check_wire_vocabulary: {path} is not valid JSON: {exc}")

    known = set(contract.get("knownTypes") or [])
    if not known:
        sys.exit(f"check_wire_vocabulary: {policy['contract']} records no knownTypes")

    problems: list[str] = []
    recorded_events = set((contract.get("events") or {}).get("types") or {})
    if not recorded_events:
        problems.append(f"{policy['contract']}: events.types records no event shapes")
    else:
        for extra in sorted(recorded_events - known):
            problems.append(
                f"{policy['contract']}: events.types has {extra}, which knownTypes omits"
            )
        for missing in sorted(known - recorded_events):
            problems.append(
                f"{policy['contract']}: knownTypes has {missing}, which events.types omits"
            )
    return known, problems


def scan_files(root: Path, policy: dict) -> list[Path]:
    excluded = set(policy.get("excludeDirs") or [])
    exempt = {root / rel for rel in policy.get("exemptFiles") or []}
    found: list[Path] = []
    for rel in policy["scanRoots"]:
        target = root / rel
        if target.is_file():
            candidates = [target]
        elif target.is_dir():
            candidates = sorted(target.rglob("*.md"))
        else:
            continue
        for path in candidates:
            if path in exempt or any(part in excluded for part in path.parts):
                continue
            if path.suffix == ".md":
                found.append(path)
    return found


def tokens_in(path: Path) -> set[str]:
    return set(TOKEN.findall(path.read_text(encoding="utf-8", errors="replace")))


def check_inventions(root: Path, paths: list[Path], known: set[str]) -> list[str]:
    problems: list[str] = []
    for path in paths:
        for token in sorted(tokens_in(path) - known):
            problems.append(
                f"{path.relative_to(root)}: names {token}, which the contract does not record"
            )
    return problems


def check_omissions(root: Path, policy: dict, known: set[str]) -> list[str]:
    problems: list[str] = []
    for rel in policy.get("enumeratingDocs") or []:
        path = root / rel
        if not path.is_file():
            problems.append(f"{rel}: listed in enumeratingDocs but does not exist")
            continue
        for missing in sorted(known - tokens_in(path)):
            problems.append(
                f"{rel}: enumerates the vocabulary but never names {missing}"
            )
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(REPO_ROOT), help="repository root to check")
    parser.add_argument("--policy", default=str(POLICY_PATH), help="policy file path")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    policy = load_policy(Path(args.policy))
    known, problems = recorded_types(root, policy)
    paths = scan_files(root, policy)
    problems += check_inventions(root, paths, known)
    problems += check_omissions(root, policy, known)

    if problems:
        print("check_wire_vocabulary: FAIL", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1
    print(
        f"check_wire_vocabulary: ok ({len(known)} recorded types, {len(paths)} file(s) scanned)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
