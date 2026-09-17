#!/usr/bin/env python3
"""Contract tests for the architecture-review structural corpus.

Gate for `.agents/quality/evals/architecture-review/scenarios/`, the
anchor-pinned structural regression corpus for the architecture-review
skill (not a behavioral eval; no model runs). This script checks:
- corpus schema: id, title, section, verdict, anchors, design present,
  non-empty, ids unique, verdict in the PASS/BLOCK/PARTIAL/NOT_RUN set;
- anchor verbatim pinning: every anchor is a substring of exactly one
  physical line of `.agents/skills/architecture-review/SKILL.md`;
- section existence: each section equals a `## heading` or a numbered
  step title parsed from that SKILL.md;
- coverage: the five pinned checks (baseline pinning, seam width,
  half-applied pattern, sibling conformance, never-invent-Low) each kept
  by at least one scenario anchor, and every verdict value used;
- portability: the semgrep portability pattern matches no title or
  design text;
- drift-token ban: the semgrep drift-marker token set (plus DRIFT)
  matches no scenario file text.

Fails closed: a missing SKILL.md, a missing scenarios directory, or zero
scenario files is a failure, never a skip.
"""

from __future__ import annotations

import importlib.util
import re
import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SKILL_MD = ROOT / ".agents" / "skills" / "architecture-review" / "SKILL.md"
SCENARIOS_DIR = (
    ROOT / ".agents" / "quality" / "evals" / "architecture-review" / "scenarios"
)
SEMGREP_RULES = ROOT / "semgrep" / "agent-standards.yml"
SEMGREP_TEST = ROOT / "scripts" / "test_semgrep_rules.py"

DRIFT_RULE_ID = "mivia.generic.no-unresolved-drift-markers"
# The semgrep rule holds the committed-code token set; the corpus rule
# additionally bans the word DRIFT, so the mirror appends it.
DRIFT_TOKEN_EXTRA = "DRIFT"

VERDICTS = ("PASS", "BLOCK", "PARTIAL", "NOT_RUN")

REQUIRED_CHECK_ANCHORS = (
    ("baseline pinning", "Pin a version-control baseline to an immutable revision identifier"),
    ("seam width", "The wider the contract or seam, the weaker the abstraction."),
    ("half-applied pattern", "half-applied pattern is a finding, not a style preference"),
    ("sibling conformance", "A contract honored by one implementation and"),
    ("never-invent-Low", "Never invent a Low finding about style or naming on otherwise"),
)

HEADING_RE = re.compile(r"^##\s+(.+?)\s*$")
STEP_RE = re.compile(r"^\d+\.\s+\*\*(.+?)\*\*")


def load_semgrep_test():
    """Load scripts/test_semgrep_rules.py as a module to reuse its helpers."""
    spec = importlib.util.spec_from_file_location("test_semgrep_rules", SEMGREP_TEST)
    assert spec is not None and spec.loader is not None, "cannot load test_semgrep_rules"
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def drift_pattern(block: str) -> re.Pattern[str]:
    """Mirror the drift-marker rule's token alternation, plus DRIFT."""
    match = re.search(r"pattern-regex:\s*'\(\?i\)\\b\(([^)]+)\)\\b'", block)
    assert match, f"{DRIFT_RULE_ID} missing token alternation"
    tokens = match.group(1).split("|") + [DRIFT_TOKEN_EXTRA]
    return re.compile(r"(?i)\b(" + "|".join(tokens) + r")\b")


def skill_sections(lines: list[str]) -> set[str]:
    """`## X` heading texts plus `N. **X.**` step titles, markers stripped."""
    sections: set[str] = set()
    for line in lines:
        heading = HEADING_RE.match(line)
        if heading:
            sections.add(heading.group(1).strip())
            continue
        step = STEP_RE.match(line)
        if step:
            title = step.group(1).strip()
            sections.add(title[:-1] if title.endswith(".") else title)
    return sections


def check_scenario(
    path: Path,
    lines: list[str],
    sections: set[str],
    portability: re.Pattern[str],
    drift: re.Pattern[str],
    seen_ids: set[str],
) -> tuple[str, set[str]]:
    """Validate one scenario file; return its verdict and anchor set."""
    rel = path.relative_to(ROOT).as_posix()
    raw = path.read_text(encoding="utf-8")
    data = tomllib.loads(raw)

    for key in ("id", "title", "section", "verdict", "anchors", "design"):
        assert key in data, f"{rel}: missing key {key!r}"
    for key in ("id", "title", "section", "design"):
        value = data[key]
        assert isinstance(value, str) and value.strip(), f"{rel}: empty {key}"
    assert data["id"] not in seen_ids, f"{rel}: duplicate id {data['id']!r}"
    seen_ids.add(data["id"])

    assert data["verdict"] in VERDICTS, (
        f"{rel}: verdict {data['verdict']!r} not in {VERDICTS}"
    )
    assert data["section"] in sections, (
        f"{rel}: section {data['section']!r} is not a SKILL.md heading or step title"
    )

    anchors = data["anchors"]
    assert isinstance(anchors, list) and anchors, f"{rel}: anchors must be a non-empty list"
    for anchor in anchors:
        assert isinstance(anchor, str) and anchor.strip(), f"{rel}: empty anchor"
        hits = sum(1 for line in lines if anchor in line)
        assert hits == 1, (
            f"{rel}: anchor {anchor!r} must be a substring of exactly one "
            f"SKILL.md line (found {hits})"
        )

    for field in ("title", "design"):
        match = portability.search(data[field])
        assert match is None, f"{rel}: {field} is not portable: {match.group(0)!r}"
    marker = drift.search(raw)
    assert marker is None, f"{rel}: drift token {marker.group(0)!r} in scenario file"
    return data["verdict"], set(anchors)


def main() -> None:
    assert SKILL_MD.is_file(), f"missing {SKILL_MD.relative_to(ROOT).as_posix()}"
    assert SCENARIOS_DIR.is_dir(), (
        f"missing {SCENARIOS_DIR.relative_to(ROOT).as_posix()}"
    )
    files = sorted(SCENARIOS_DIR.glob("*.toml"))
    assert files, "scenario corpus is empty; this gate fails closed, it never skips"

    semgrep_test = load_semgrep_test()
    rules_text = SEMGREP_RULES.read_text(encoding="utf-8")
    portability = semgrep_test.portability_pattern(rules_text)
    drift = drift_pattern(semgrep_test.rule_block(rules_text, DRIFT_RULE_ID))

    lines = SKILL_MD.read_text(encoding="utf-8").splitlines()
    sections = skill_sections(lines)
    assert sections, "no headings or step titles parsed from SKILL.md"

    seen_ids: set[str] = set()
    verdicts_seen: set[str] = set()
    all_anchors: set[str] = set()
    for path in files:
        verdict, anchors = check_scenario(
            path, lines, sections, portability, drift, seen_ids
        )
        verdicts_seen.add(verdict)
        all_anchors.update(anchors)

    for name, anchor in REQUIRED_CHECK_ANCHORS:
        assert anchor in all_anchors, (
            f"pinned check {name!r} has no scenario anchor {anchor!r}"
        )
    for verdict in VERDICTS:
        assert verdict in verdicts_seen, f"verdict {verdict} used by no scenario"

    print(f"test_architecture_review_evals: ok ({len(files)} scenarios)")


if __name__ == "__main__":
    main()
