#!/usr/bin/env python3
"""Check the public release and installer contracts."""

from __future__ import annotations

import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def require(text: str, fragment: str, path: Path) -> None:
    if fragment not in text:
        raise AssertionError(f"{path}: missing {fragment!r}")


def main() -> None:
    workflow_path = ROOT / ".github/workflows/release.yml"
    release_path = ROOT / "scripts/release.sh"
    install_path = ROOT / "scripts/install.sh"
    powershell_path = ROOT / "scripts/install.ps1"
    powershell_test_path = ROOT / "scripts/test_installers.ps1"
    ci_path = ROOT / ".github/workflows/ci.yml"
    workflow = workflow_path.read_text(encoding="utf-8")
    release = release_path.read_text(encoding="utf-8")
    install = install_path.read_text(encoding="utf-8")
    powershell = powershell_path.read_text(encoding="utf-8")
    ci = ci_path.read_text(encoding="utf-8")

    for command, path in (("bash", release_path), ("sh", install_path)):
        result = subprocess.run([command, "-n", str(path)], capture_output=True, text=True)
        if result.returncode != 0:
            raise AssertionError(f"{path}: shell syntax failed: {result.stderr}")

    for fragment in (
        "goos: linux",
        "goos: darwin",
        "goos: windows",
        "merge-multiple: true",
        "actions/attest-build-provenance@96b4a1ef7235a096b17240c259729fdd70c83d45",
        "mivia-version.txt",
        "scripts/install.ps1",
        "persist-credentials: false",
        "github.sha",
        "--verify-tag",
        '--repo "${GITHUB_REPOSITORY}"',
        "needs: validate",
        "sha256sum -c checksums.txt",
        "test \"$(find dist -maxdepth 1 -type f",
    ):
        require(workflow, fragment, workflow_path)

    for fragment in (
        "archive_name()",
        "mivia_%s_%s_%s.tar.gz",
        "mivia_%s_%s_%s.zip",
        "expected=6",
        "git status --porcelain",
        "refs/tags/${requested}^{commit}",
        "go run -trimpath",
        "checksums.txt",
        "mivia-version.txt",
        "install.ps1",
    ):
        require(release, fragment, release_path)

    for fragment in (
        "MIVIA_VERSION",
        "sha256sum",
        "checksums.txt",
        "--proto '=https'",
        "XDG_BIN_DIR",
        "unsupported operating system",
        "unsupported architecture",
    ):
        require(install, fragment, install_path)

    for fragment in (
        "ValidatePattern",
        "PROCESSOR_ARCHITECTURE",
        "Get-FileHash -Algorithm SHA256",
        "Expand-Archive",
        "unsupported architecture",
    ):
        require(powershell, fragment, powershell_path)

    if not powershell_test_path.is_file():
        raise AssertionError(f"{powershell_test_path}: missing PowerShell installer test")
    macos_ci = ci.split("  verify-macos:", 1)[1].split("\n  verify-windows:", 1)[0]
    for fragment in (
        "branches: [master]",
        "if: github.event_name == 'pull_request'",
        "if: github.event_name == 'push'",
        "cancel-in-progress: ${{ github.event_name == 'pull_request' }}",
        "go test ./... -count=1",
        "make race",
        "CGO_ENABLED=0 go build -trimpath -o \"$output\" ./cmd/mivia",
        "scripts/test_installers.ps1",
    ):
        require(ci, fragment, ci_path)
    for fragment in (
        "Prepare isolated test home",
        'ci_root="$(mktemp -d /private/tmp/mivia-ci.XXXXXX)"',
        'echo "HOME=$test_home" >> "$GITHUB_ENV"',
        'echo "TMPDIR=$test_tmp" >> "$GITHUB_ENV"',
        'echo "GOPATH=$(go env GOPATH)" >> "$GITHUB_ENV"',
        ': > "$test_home/.mivia/.env"',
        'ln -s "$module_cache" "$test_home/go/pkg/mod"',
    ):
        require(macos_ci, fragment, ci_path)

    result = subprocess.run(
        ["bash", str(release_path)], cwd=ROOT, capture_output=True, text=True
    )
    if result.returncode == 0:
        raise AssertionError("release.sh must reject a missing release tag")
    if "version tag" not in result.stderr:
        raise AssertionError(f"unexpected missing-tag error: {result.stderr}")

    print("release contracts: ok")


if __name__ == "__main__":
    main()
