#!/usr/bin/env python3
"""Exercise installer platform and checksum failure paths without a network."""

from __future__ import annotations

import hashlib
import os
import subprocess
import tarfile
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
INSTALL = ROOT / "scripts/install.sh"


def write_executable(path: Path, text: str) -> None:
    path.write_text(text, encoding="utf-8")
    path.chmod(0o755)


def run(env: dict[str, str], *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["sh", str(INSTALL), *args],
        cwd=ROOT,
        env=env,
        capture_output=True,
        text=True,
    )


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="mivia-installer-test-") as raw:
        temp = Path(raw)
        fake_bin = temp / "bin"
        fake_bin.mkdir()
        base_path = os.environ.get("PATH", "/usr/bin:/bin")

        write_executable(
            fake_bin / "uname",
            "#!/bin/sh\nif [ \"$1\" = -s ]; then echo Linux; else echo x86_64; fi\n",
        )
        unsupported = dict(os.environ, PATH=str(fake_bin) + ":" + base_path)
        write_executable(fake_bin / "uname", "#!/bin/sh\necho FreeBSD\n")
        result = run(unsupported, "v1.2.3")
        if result.returncode == 0 or "unsupported operating system" not in result.stderr:
            raise AssertionError(f"unsupported OS was accepted: {result.stderr}")

        write_executable(
            fake_bin / "uname",
            "#!/bin/sh\nif [ \"$1\" = -s ]; then echo Linux; else echo x86_64; fi\n",
        )
        fixture = temp / "fixture"
        fixture.mkdir()
        archive = fixture / "mivia_1.2.3_linux_amd64.tar.gz"
        binary = fixture / "mivia"
        binary.write_bytes(b"test mivia binary")
        with tarfile.open(archive, "w:gz") as output:
            output.add(binary, arcname="mivia")
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        (fixture / "checksums.txt").write_text(
            f"{digest}  {archive.name}\n", encoding="utf-8"
        )
        write_executable(
            fake_bin / "curl",
            "#!/usr/bin/env python3\n"
            "import shutil, sys\n"
            "import os\n"
            "url = next(x for x in sys.argv if x.startswith('https://'))\n"
            "out = sys.argv[sys.argv.index('-o') + 1]\n"
            "shutil.copy2(os.path.join(os.environ['FIXTURE'], url.rsplit('/', 1)[-1]), out)\n",
        )
        env = dict(unsupported, PATH=str(fake_bin) + ":" + base_path)
        env["MIVIA_INSTALL_DIR"] = str(temp / "install's bin")
        env["MIVIA_VERSION"] = "v1.2.3"
        env["FIXTURE"] = str(fixture)
        env["HOME"] = str(temp / "home")
        env["SHELL"] = "/bin/bash"
        result = run(env)
        if result.returncode != 0:
            raise AssertionError(f"valid archive was rejected: {result.stderr}")
        if (temp / "install's bin/mivia").read_bytes() != binary.read_bytes():
            raise AssertionError("installer wrote the wrong binary")
        profile = temp / "home/.bashrc"
        profile_text = profile.read_text(encoding="utf-8")
        escaped_install_dir = str(temp / "install's bin").replace("'", "'\\''")
        expected_path_line = f"export PATH='{escaped_install_dir}':\"$PATH\""
        if profile_text.count("# mivia installer: PATH") != 1 or expected_path_line not in profile_text:
            raise AssertionError("installer did not add an idempotent PATH entry")

        archive.write_bytes(archive.read_bytes())
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        (fixture / "checksums.txt").write_text(
            f"{digest}  {archive.name}\n", encoding="utf-8"
        )
        result = run(env)
        if result.returncode != 0 or profile.read_text(encoding="utf-8") != profile_text:
            raise AssertionError("installer duplicated the PATH entry")

        no_path_env = dict(env, MIVIA_NO_PATH_UPDATE="1", MIVIA_SHELL_RC=str(temp / "no-path.rc"))
        result = run(no_path_env)
        if result.returncode != 0 or (temp / "no-path.rc").exists():
            raise AssertionError("installer did not honor MIVIA_NO_PATH_UPDATE=1")

        archive.write_bytes(b"tampered archive")
        result = run(env)
        if result.returncode == 0 or "checksum verification failed" not in result.stderr:
            raise AssertionError(f"tampered archive was accepted: {result.stderr}")

    print("installer contracts: ok")


if __name__ == "__main__":
    main()
