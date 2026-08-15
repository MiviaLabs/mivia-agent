#!/usr/bin/env python3
"""Check that provider docs stay in step with the provider registry (mivia).

The single source of truth for built-in providers is the `descriptors` map in
internal/providerregistry/registry.go. This script parses that map (a small,
stable, one-block-per-entry format - the same philosophy as
check_docs_ownership.py's minimal YAML subset) and verifies that the docs that
name providers agree with it:

  1. README.md "Supported providers" table (root landing page) - the provider
     set, default model, and default API base URL must match the registry.
  2. docs/architecture/overview.md "Provider transport retry" section - every
     built-in provider must be named in the parenthesized list.

The check is hermetic: it reads only the two doc files and the registry source,
never shells out to `go` and never touches the network. `go` is not required.

Run directly:
    python3 scripts/check_provider_docs.py

Failure exits nonzero with a precise diff naming the drift, so the output is
actionable: edit the doc to match the registry (the registry is the contract).
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REGISTRY = ROOT / "internal" / "providerregistry" / "registry.go"
README = ROOT / "README.md"
ARCH_OVERVIEW = ROOT / "docs" / "architecture" / "overview.md"

# README display name -> registry key. Only branding differs; the registry key
# is the canonical identifier. Add a row here when a provider gains a README row.
DISPLAY_TO_KEY = {
    "DeepSeek": "deepseek",
    "OpenRouter": "openrouter",
    "ZAI": "zai",
    "Ollama": "ollama",
    "LLM Gateway": "llmgateway",
}

# Reverse: registry key -> the display names the docs may use for it. The arch
# overview writes z.ai (dot), the README writes ZAI (z.ai); both mean zai.
KEY_TO_DISPLAYS = {
    "deepseek": ["DeepSeek"],
    "openrouter": ["OpenRouter"],
    "zai": ["ZAI", "z.ai"],
    "ollama": ["Ollama"],
    "llmgateway": ["LLM Gateway"],
}


def normalize_display(name: str) -> str:
    """Lowercase and strip non-alphanumerics so 'z.ai' == 'ZAI' == 'zai'."""
    return re.sub(r"[^a-z0-9]+", "", name.casefold())


def display_to_key(display: str) -> str | None:
    """Resolve a doc-side provider display name to a registry key."""
    norm = normalize_display(display)
    for key, displays in KEY_TO_DISPLAYS.items():
        if any(normalize_display(d) == norm for d in displays):
            return key
    # Also accept the bare registry key itself (e.g. 'zai' in docs).
    if norm in {normalize_display(k) for k in DISPLAY_TO_KEY.values()}:
        for key in DISPLAY_TO_KEY.values():
            if normalize_display(key) == norm:
                return key
    return None


class ProviderDocsError(Exception):
    pass


def fail(msg: str) -> None:
    print(f"check_provider_docs: {msg}", file=sys.stderr)
    raise SystemExit(1)


def parse_registry(text: str) -> dict[str, dict[str, str]]:
    """Parse the descriptors map into {key: {DefaultModel, DefaultURL}}.

    Format (stable, one block per entry):
        "deepseek": {
            Name: "deepseek", DefaultModel: "deepseek-v4-flash",
            DefaultURL: "https://api.deepseek.com/v1", DefaultAPIKeyEnv: "DEEPSEEK_API_KEY",
        },
    Only the two fields the docs surface are captured; the parser is strict
    enough to reject a structurally surprising file loudly.
    """
    block_re = re.compile(
        r'^\s*"([a-z0-9_-]+)"\s*:\s*\{\s*(.*?)\s*\},?\s*$',
        re.MULTILINE | re.DOTALL,
    )
    field_re = re.compile(
        r'\b(DefaultModel|DefaultURL)\s*:\s*"([^"]*)"',
    )
    out: dict[str, dict[str, str]] = {}
    for key, body in block_re.findall(text):
        fields = dict(field_re.findall(body))
        if "DefaultModel" not in fields or "DefaultURL" not in fields:
            raise ProviderDocsError(
                f"registry.go descriptor {key!r} missing DefaultModel/DefaultURL"
            )
        out[key] = fields
    if not out:
        raise ProviderDocsError("no provider descriptors parsed from registry.go")
    return out


def parse_readme_table(text: str) -> list[dict[str, str]]:
    """Parse the README "Supported providers" table rows.

    Returns [{provider, model, url, default}] where provider is the display
    name from the first column and default marks the registry-default row.
    """
    section_re = re.compile(r"^##\s+Supported providers\s*$", re.MULTILINE)
    m = section_re.search(text)
    if not m:
        raise ProviderDocsError("README.md has no '## Supported providers' section")
    # The table is the first markdown table after the heading.
    table_re = re.compile(r"^\|(.+)\|\s*$", re.MULTILINE)
    rows: list[dict[str, str]] = []
    after = text[m.end() :]
    in_table = False
    for line in after.splitlines():
        if not line.lstrip().startswith("|"):
            if in_table:
                break
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if not in_table:
            in_table = True
            continue  # header row
        if set(cells) <= {"-", ":", "-:", ":-", ":-:"} or all(set(c) <= {"-", ":"} for c in cells):
            continue  # separator row
        if len(cells) < 3:
            continue
        provider = cells[0]
        default = "(default)" in provider
        provider = provider.replace("(default)", "").strip()
        # Drop a trailing parenthetical (e.g. "ZAI (z.ai)") so the provider
        # name matches DISPLAY_TO_KEY; the parenthetical is branding detail.
        provider = re.sub(r"\s*\([^)]*\)\s*$", "", provider).strip()
        rows.append(
            {
                "provider": provider,
                "model": cells[1].strip("` "),
                "url": cells[2].strip("` "),
                "default": default,
            }
        )
    return rows


def parse_arch_overview(text: str) -> list[str]:
    """Parse the provider list in the architecture overview retry section."""
    m = re.search(
        r"Every built-in provider \(([^)]+)\) is an `OpenAICompat`",
        text,
    )
    if not m:
        raise ProviderDocsError(
            "docs/architecture/overview.md has no 'Every built-in provider (...)' list"
        )
    return [p.strip() for p in m.group(1).split(",")]


def main(argv: list[str] | None = None) -> int:
    if argv is None:
        argv = sys.argv[1:]
    # --fixture REGISTRY README ARCH: check synthetic text instead of the real
    # files. Used by the self-tests to prove each drift class is detected
    # without mutating the repository.
    if argv and argv[0] == "--fixture":
        if len(argv) != 4:
            print("check_provider_docs: --fixture needs REGISTRY README ARCH", file=sys.stderr)
            return 1
        try:
            registry = parse_registry(argv[1])
            rows = parse_readme_table(argv[2])
            arch_names = parse_arch_overview(argv[3])
        except ProviderDocsError as exc:
            print(f"check_provider_docs: {exc}", file=sys.stderr)
            return 1
        problems: list[str] = []
        expected_keys = sorted(registry)
        row_keys = []
        for row in rows:
            display = row["provider"]
            key = display_to_key(display)
            if key is None:
                problems.append(
                    f"README provider {display!r} has no registry descriptor "
                    f"(known display names: {', '.join(sorted(DISPLAY_TO_KEY))})"
                )
                continue
            row_keys.append(key)
            desc = registry[key]
            if row["model"] != desc["DefaultModel"]:
                problems.append(
                    f"README {display!r} default model {row['model']!r} != registry "
                    f"{desc['DefaultModel']!r}"
                )
            if row["url"] != desc["DefaultURL"]:
                problems.append(
                    f"README {display!r} default URL {row['url']!r} != registry "
                    f"{desc['DefaultURL']!r}"
                )
        if sorted(row_keys) != expected_keys:
            problems.append(
                f"README provider set {sorted(row_keys)} != registry {expected_keys}"
            )
        arch_keys = sorted({display_to_key(n) or n for n in arch_names})
        if arch_keys != expected_keys:
            problems.append(
                f"architecture overview provider list {arch_keys} != registry {expected_keys}"
            )
        if problems:
            print("check_provider_docs: provider docs drift:\n  " + "\n  ".join(problems), file=sys.stderr)
            return 1
        print(f"check_provider_docs: ok ({len(registry)} providers)")
        return 0

    try:
        registry_text = REGISTRY.read_text(encoding="utf-8")
        readme_text = README.read_text(encoding="utf-8")
        arch_text = ARCH_OVERVIEW.read_text(encoding="utf-8")
    except OSError as exc:
        fail(f"could not read input: {exc}")
        return 1

    try:
        registry = parse_registry(registry_text)
        rows = parse_readme_table(readme_text)
        arch_names = parse_arch_overview(arch_text)
    except ProviderDocsError as exc:
        fail(str(exc))
        return 1

    problems: list[str] = []

    # 1. README table: set, model, URL must match the registry.
    expected_keys = sorted(registry)
    row_keys = []
    for row in rows:
        display = row["provider"]
        key = display_to_key(display)
        if key is None:
            problems.append(
                f"README provider {display!r} has no registry descriptor "
                f"(known display names: {', '.join(sorted(DISPLAY_TO_KEY))})"
            )
            continue
        row_keys.append(key)
        desc = registry[key]
        if row["model"] != desc["DefaultModel"]:
            problems.append(
                f"README {display!r} default model {row['model']!r} != registry "
                f"{desc['DefaultModel']!r}"
            )
        if row["url"] != desc["DefaultURL"]:
            problems.append(
                f"README {display!r} default URL {row['url']!r} != registry "
                f"{desc['DefaultURL']!r}"
            )

    if sorted(row_keys) != expected_keys:
        problems.append(
            f"README provider set {sorted(row_keys)} != registry {expected_keys}"
        )

    # 2. Architecture overview must name every provider.
    arch_keys = sorted({display_to_key(n) or n for n in arch_names})
    if arch_keys != expected_keys:
        problems.append(
            f"architecture overview provider list {arch_keys} != registry {expected_keys}"
        )

    if problems:
        fail("provider docs drift from internal/providerregistry/registry.go:\n  "
             + "\n  ".join(problems))
    print(
        f"check_provider_docs: ok ({len(registry)} providers: "
        f"{', '.join(expected_keys)})"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
