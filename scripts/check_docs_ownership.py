#!/usr/bin/env python3
"""Validate docs/OWNERS.yaml and docs/**/*.md ownership (mivia).

Schema (docs/OWNERS.yaml):
  version: 1
  topics:
    name:
      path: docs/foo.md | docs/dir/
      owner: team
      description: ...

Rules:
  - OWNERS.yaml must exist and define topics with paths
  - every topic path must exist (file or directory)
  - every docs/**/*.md has an owner topic (exact path, directory prefix, or policy allowlist)
  - duplicate H1 titles across docs/ fail
  - fail if a staged/new file covers an owned topic path without being the canonical path
    (parallel overview/index under a file-owned topic, or second file with same title as canonical)
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
OWNERS = ROOT / "docs" / "OWNERS.yaml"
POLICY = ROOT / ".mivia" / "policy" / "docs-ownership.json"
H1_RE = re.compile(r"^#\s+(.+?)\s*$", re.MULTILINE)


class DocsOwnershipError(Exception):
    pass


def fail(msg: str) -> None:
    print(f"check_docs_ownership: {msg}", file=sys.stderr)
    raise SystemExit(1)


def parse_owners(text: str) -> dict[str, dict[str, str]]:
    """Minimal YAML subset: topics with path/owner/description."""
    topics: dict[str, dict[str, str]] = {}
    current: str | None = None
    in_topics = False
    for raw in text.splitlines():
        line = raw.split("#", 1)[0].rstrip()
        if not line.strip():
            continue
        if line.strip() == "topics:":
            in_topics = True
            continue
        if not in_topics:
            continue
        m_topic = re.match(r"^  ([A-Za-z0-9_.-]+):\s*$", line)
        if m_topic:
            current = m_topic.group(1)
            topics[current] = {}
            continue
        if current is None:
            continue
        m_field = re.match(r'^    (path|owner|description):\s*["\']?(.+?)["\']?\s*$', line)
        if m_field:
            topics[current][m_field.group(1)] = m_field.group(2).strip()
    return topics


def load_policy() -> dict[str, Any]:
    if not POLICY.is_file():
        return {
            "allowlistedUnownedPrefixes": ["docs/adr/"],
            "forbiddenParallelRoots": [],
        }
    try:
        return json.loads(POLICY.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        fail(f"invalid docs-ownership.json: {exc}")
        return {}


def path_exists(rel: str) -> bool:
    p = ROOT / rel
    if rel.endswith("/"):
        return p.is_dir()
    return p.is_file() or p.is_dir()


def owned_by(rel: str, topics: dict[str, dict[str, str]]) -> str | None:
    """Return topic name owning rel, if any."""
    for name, meta in topics.items():
        path = meta.get("path", "").strip()
        if not path:
            continue
        if path.endswith("/"):
            if rel.startswith(path) or rel + "/" == path:
                return name
            if rel.startswith(path.rstrip("/") + "/"):
                return name
        elif rel == path:
            return name
    return None


def extract_h1(path: Path) -> str | None:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return None
    m = H1_RE.search(text)
    if not m:
        return None
    return re.sub(r"\s+", " ", m.group(1).strip()).casefold()


def staged_docs() -> set[str]:
    proc = subprocess.run(
        ["git", "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", "docs/"],
        cwd=ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        return set()
    return {p for p in proc.stdout.split("\0") if p.endswith(".md")}


def check_parallel_topic_files(
    topics: dict[str, dict[str, str]],
    docs: list[Path],
    *,
    staged: set[str] | None,
) -> list[str]:
    """Fail when a second file covers a file-canonical OWNERS topic without update."""
    failures: list[str] = []
    by_rel = {p.relative_to(ROOT).as_posix(): p for p in docs}

    for name, meta in topics.items():
        canonical = meta.get("path", "").strip()
        if not canonical or canonical.endswith("/"):
            continue  # directory topics allow multiple files under the prefix
        if not canonical.endswith(".md"):
            continue
        parent = str(Path(canonical).parent).replace("\\", "/")
        if parent == "docs":
            # top-level single file topics (e.g. contributing.md): flag sibling
            # overview-style duplicates with same stem family only via H1 check.
            continue

        # Other OWNERS canonical file paths are not "parallel" — they are owned topics.
        owned_canonicals = {
            meta.get("path", "").strip()
            for meta in topics.values()
            if meta.get("path", "").strip().endswith(".md")
        }

        siblings = [
            rel
            for rel in by_rel
            if rel.startswith(parent + "/") and rel != canonical and rel not in owned_canonicals
        ]
        if not siblings:
            continue

        canon_h1 = extract_h1(ROOT / canonical) if (ROOT / canonical).is_file() else None
        for rel in siblings:
            if staged is not None and rel not in staged:
                # full check still runs H1 globally; parallel overview only fails
                # for staged additions when --staged, else always.
                pass
            base = Path(rel).name.casefold()
            if base in {"overview.md", "index.md", "readme.md"} and Path(canonical).name.casefold() != base:
                if staged is None or rel in staged:
                    failures.append(
                        f"topic {name!r}: second overview-style file {rel} without OWNERS "
                        f"update (canonical is {canonical})"
                    )
            if canon_h1:
                other_h1 = extract_h1(by_rel[rel])
                if other_h1 and other_h1 == canon_h1:
                    if staged is None or rel in staged:
                        failures.append(
                            f"topic {name!r}: {rel} duplicates canonical H1 of {canonical}; "
                            f"edit the owner path or update docs/OWNERS.yaml"
                        )
    return failures


def run_checks(*, staged_mode: bool = False) -> None:
    if not OWNERS.is_file():
        fail("docs/OWNERS.yaml is required")

    policy = load_policy()
    topics = parse_owners(OWNERS.read_text(encoding="utf-8"))
    if not topics:
        fail("docs/OWNERS.yaml has no topics with path entries")

    missing_paths = [meta["path"] for meta in topics.values() if "path" in meta and not path_exists(meta["path"])]
    if missing_paths:
        fail("OWNERS path missing on disk:\n  " + "\n  ".join(missing_paths))

    docs_root = ROOT / "docs"
    if not docs_root.is_dir():
        fail("docs/ directory missing")
    docs_files = sorted(p for p in docs_root.rglob("*.md") if p.is_file())
    if not docs_files:
        fail("no docs/**/*.md files found")

    allow_prefixes = policy.get("allowlistedUnownedPrefixes") or []
    if not isinstance(allow_prefixes, list):
        allow_prefixes = []
    # Directory topic paths are always allow prefixes for children.
    for meta in topics.values():
        path = meta.get("path", "")
        if path.endswith("/"):
            allow_prefixes.append(path)

    unowned: list[str] = []
    for path in docs_files:
        rel = path.relative_to(ROOT).as_posix()
        if owned_by(rel, topics):
            continue
        if any(rel.startswith(str(prefix)) for prefix in allow_prefixes):
            continue
        unowned.append(rel)
    if unowned:
        fail(
            "docs without OWNERS entry (add topic or allowlist):\n  "
            + "\n  ".join(unowned)
        )

    # duplicate H1 across docs/
    h1_map: dict[str, list[str]] = {}
    for path in docs_files:
        rel = path.relative_to(ROOT).as_posix()
        h1 = extract_h1(path)
        if not h1:
            fail(f"{rel}: missing H1 (# title)")
        h1_map.setdefault(h1, []).append(rel)
    dups = {k: v for k, v in h1_map.items() if len(v) > 1}
    if dups:
        lines = [f"{title}: {', '.join(paths)}" for title, paths in sorted(dups.items())]
        fail("duplicate H1 titles:\n  " + "\n  ".join(lines))

    forbidden = policy.get("forbiddenParallelRoots") or []
    for root in forbidden:
        if (ROOT / root).exists():
            fail(f"forbidden parallel docs root exists: {root}")

    # Path pattern bans from policy (optional).
    for pat in policy.get("forbiddenPathPatterns") or []:
        if not isinstance(pat, str):
            continue
        cre = re.compile(pat)
        for path in docs_files:
            rel = path.relative_to(ROOT).as_posix()
            if cre.search(rel):
                fail(f"forbidden docs path pattern matched: {rel} ({pat})")

    staged = staged_docs() if staged_mode else None
    parallel = check_parallel_topic_files(topics, docs_files, staged=staged)
    if parallel:
        fail("parallel topic coverage without OWNERS update:\n  " + "\n  ".join(parallel))

    print(f"check_docs_ownership: ok ({len(topics)} topics, {len(docs_files)} docs)")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="mivia docs ownership checks")
    parser.add_argument(
        "--staged",
        action="store_true",
        help="tighten parallel-topic detection to staged docs (H1 uniqueness still global)",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        run_checks(staged_mode=args.staged)
    except SystemExit as exc:
        return int(exc.code or 0)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
