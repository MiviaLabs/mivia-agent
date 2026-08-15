#!/usr/bin/env python3
"""Self-tests for scripts/check_provider_docs.py.

Pure text assertions so the check stays fast and hermetic (no `go`, no
network). Covers the real repo files (must pass today) and synthetic drifted
fixtures (must fail), so a doc drift is caught by the gate AND by this test.
"""

from __future__ import annotations

import subprocess
import sys
import tempfile
from pathlib import Path

import check_provider_docs as cp


ROOT = Path(__file__).resolve().parents[1]

GOOD_REGISTRY = '''\
var descriptors = map[string]Descriptor{
\t"deepseek": {
\t\tName: "deepseek", DefaultModel: "deepseek-v4-flash",
\t\tDefaultURL: "https://api.deepseek.com/v1", DefaultAPIKeyEnv: "DEEPSEEK_API_KEY",
\t},
\t"openrouter": {
\t\tName: "openrouter", DefaultModel: "openai/gpt-4o-mini",
\t\tDefaultURL: "https://openrouter.ai/api/v1", DefaultAPIKeyEnv: "OPENROUTER_API_KEY",
\t},
\t"zai": {
\t\tName: "zai", DefaultModel: "glm-5.2",
\t\tDefaultURL: "https://api.z.ai/api/paas/v4", DefaultAPIKeyEnv: "ZAI_API_KEY",
\t},
\t"ollama": {
\t\tName: "ollama", DefaultModel: "gpt-oss:120b",
\t\tDefaultURL: "https://ollama.com/v1", DefaultAPIKeyEnv: "OLLAMA_API_KEY",
\t},
\t"llmgateway": {
\t\tName: "llmgateway", DefaultModel: "deepseek-v4-pro",
\t\tDefaultURL: "https://api.llmgateway.io/v1", DefaultAPIKeyEnv: "LLMGATEWAY_API_KEY",
\t},
}
'''

GOOD_README = '''\
## Supported providers

| Provider | Default model | Default API base URL |
|----------|---------------|-----------------------|
| DeepSeek (default) | `deepseek-v4-flash` | `https://api.deepseek.com/v1` |
| OpenRouter | `openai/gpt-4o-mini` | `https://openrouter.ai/api/v1` |
| ZAI (z.ai) | `glm-5.2` | `https://api.z.ai/api/paas/v4` |
| Ollama | `gpt-oss:120b` | `https://ollama.com/v1` |
| LLM Gateway | `deepseek-v4-pro` | `https://api.llmgateway.io/v1` |
'''

GOOD_ARCH = "Every built-in provider (DeepSeek, OpenRouter, ZAI, Ollama, LLM Gateway) is an `OpenAICompat` client."


def expect_ok(registry: str, readme: str, arch: str) -> None:
    reg = cp.parse_registry(registry)
    rows = cp.parse_readme_table(readme)
    names = cp.parse_arch_overview(arch)
    expected = sorted(reg)
    row_keys = []
    problems: list[str] = []
    for row in rows:
        key = cp.display_to_key(row["provider"])
        assert key is not None, f"unknown display {row['provider']!r}"
        row_keys.append(key)
        if row["model"] != reg[key]["DefaultModel"]:
            problems.append("model mismatch")
        if row["url"] != reg[key]["DefaultURL"]:
            problems.append("url mismatch")
    if sorted(row_keys) != expected:
        problems.append("set mismatch")
    if sorted({cp.display_to_key(n) or n for n in names}) != expected:
        problems.append("arch list mismatch")
    assert not problems, problems


def expect_fail(registry: str, readme: str, arch: str) -> str:
    """Return the failure text when the check rejects the inputs."""
    proc = subprocess.run(
        [sys.executable, str(ROOT / "scripts" / "check_provider_docs.py"), "--fixture",
         registry, readme, arch],
        capture_output=True, text=True, check=False,
    )
    assert proc.returncode != 0, "expected non-zero exit"
    return proc.stderr


def test_real_repo_files_pass() -> None:
    """The committed README/overview must agree with the committed registry."""
    reg_text = (ROOT / "internal/providerregistry/registry.go").read_text(encoding="utf-8")
    readme = (ROOT / "README.md").read_text(encoding="utf-8")
    arch = (ROOT / "docs/architecture/overview.md").read_text(encoding="utf-8")
    expect_ok(reg_text, readme, arch)


def test_parser_roundtrip() -> None:
    reg = cp.parse_registry(GOOD_REGISTRY)
    assert reg["deepseek"]["DefaultModel"] == "deepseek-v4-flash"
    assert reg["openrouter"]["DefaultURL"] == "https://openrouter.ai/api/v1"
    rows = cp.parse_readme_table(GOOD_README)
    assert rows[0]["provider"] == "DeepSeek" and rows[0]["default"] is True
    assert rows[1]["provider"] == "OpenRouter" and rows[1]["default"] is False
    assert cp.parse_arch_overview(GOOD_ARCH) == ["DeepSeek", "OpenRouter", "ZAI", "Ollama", "LLM Gateway"]


def test_detects_model_drift() -> None:
    err = expect_fail(
        GOOD_REGISTRY,
        GOOD_README.replace("deepseek-v4-flash", "deepseek-v4-pro"),
        GOOD_ARCH,
    )
    assert "default model" in err


def test_detects_url_drift() -> None:
    err = expect_fail(
        GOOD_REGISTRY,
        GOOD_README.replace("https://api.deepseek.com/v1", "https://wrong.example/v1"),
        GOOD_ARCH,
    )
    assert "default URL" in err


def test_detects_provider_set_drift() -> None:
    err = expect_fail(
        GOOD_REGISTRY,
        GOOD_README + "| NotAProvider | `x` | `https://x.example/v1` |\n",
        GOOD_ARCH,
    )
    assert "no registry descriptor" in err


def test_detects_arch_list_drift() -> None:
    err = expect_fail(
        GOOD_REGISTRY,
        GOOD_README,
        "Every built-in provider (DeepSeek) is an `OpenAICompat` client.",
    )
    assert "architecture overview provider list" in err


def test_detects_missing_section() -> None:
    err = expect_fail(
        GOOD_REGISTRY,
        "no table here",
        GOOD_ARCH,
    )
    assert "Supported providers" in err


def test_detects_missing_registry_fields() -> None:
    err = expect_fail(
        'var descriptors = map[string]Descriptor{\n\t"x": { Name: "x" },\n}\n',
        GOOD_README,
        GOOD_ARCH,
    )
    assert "missing DefaultModel/DefaultURL" in err


def main() -> int:
    tests = [
        v
        for k, v in sorted(globals().items())
        if k.startswith("test_") and callable(v)
    ]
    failures = 0
    for t in tests:
        try:
            t()
        except AssertionError as exc:
            failures += 1
            print(f"FAIL {t.__name__}: {exc}")
        except Exception as exc:  # noqa: BLE001 - test harness reports all
            failures += 1
            print(f"ERROR {t.__name__}: {exc!r}")
    if failures:
        print(f"{failures} test(s) failed")
        return 1
    print(f"test_check_provider_docs: ok ({len(tests)} tests)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
