# Release and installation

This document describes the public release process for `mivia`.

## Supported targets

Each release provides these targets:

- Linux amd64 and arm64.
- macOS amd64 and arm64.
- Windows amd64 and arm64.

Unix targets use `.tar.gz` archives. Windows targets use `.zip` archives. Each archive contains the `mivia` binary, `README.md`, and `LICENSE`.

## Create a release

Use a semantic version tag. The tag must point to the release commit.

```bash
git tag -a v0.1.0 -m "mivia v0.1.0"
git push origin v0.1.0
```

The release workflow validates the tag identity, builds all six targets, checks each embedded version, creates archives, creates `checksums.txt`, and publishes the GitHub Release. It also publishes both installer scripts. A stable release publishes `mivia-version.txt`. The workflow creates GitHub build provenance attestations for each archive.

The workflow does not publish a release for an invalid tag. It does not create a missing tag. Release tags must not be changed after publication.

## Install Linux or macOS

Install the latest stable release:

```bash
curl -fsSL https://raw.githubusercontent.com/MiviaLabs/mivia-agent/master/scripts/install.sh | bash
```

Open a new shell, or source the profile that the installer reports. Then run `mivia --version`.

The installer resolves one stable release from `mivia-version.txt`. It then downloads the archive and checksum from that exact tag. It fails clearly before the first stable release exists. It never selects a pre-release by default.

For a pinned release, download the script from the same tag. Inspect the file before you run it:

```bash
curl --fail --silent --show-error --location \
  https://raw.githubusercontent.com/MiviaLabs/mivia-agent/v0.1.0/scripts/install.sh \
  -o /tmp/mivia-install.sh
sed -n '1,240p' /tmp/mivia-install.sh
sh /tmp/mivia-install.sh v0.1.0
```

The installer detects the operating system and CPU architecture. It installs to `$XDG_BIN_DIR`, or `$HOME/.local/bin` by default. Set `MIVIA_INSTALL_DIR` or pass a directory argument to select another path.

The install directory precedence is: a directory argument, `MIVIA_INSTALL_DIR`, `XDG_BIN_DIR`, then `$HOME/.local/bin`. If the directory is not on `PATH`, the installer updates the selected user shell profile. It does not use `sudo`. Open a new shell, or source the reported profile, before you run `mivia`. Set `MIVIA_NO_PATH_UPDATE=1` to skip this change. The installer then prints the command to run manually.

The installer downloads only the selected version. It downloads `checksums.txt` over HTTPS. It compares the downloaded archive with its SHA-256 entry before extraction. It does not require `sudo`.

## Install Windows

Install the latest stable release:

```powershell
irm https://raw.githubusercontent.com/MiviaLabs/mivia-agent/master/scripts/install.ps1 | iex
mivia --version
```

For a pinned release, use the version parameter after you inspect the script:

```powershell
$version = 'v0.1.0'
Invoke-WebRequest -UseBasicParsing `
  "https://raw.githubusercontent.com/MiviaLabs/mivia-agent/$version/scripts/install.ps1" `
  -OutFile .\mivia-install.ps1
Get-Content .\mivia-install.ps1
.\mivia-install.ps1 -Version $version
```

The installer supports amd64 and arm64. It installs to `$env:LOCALAPPDATA\mivia\bin` by default. Use `-InstallDir` to select another user-owned directory. The installer updates only the user PATH and the current PowerShell process. Other terminals need a restart. Use `-NoPathUpdate` to skip the PATH change.

The installer downloads a pinned archive and `checksums.txt`. It compares the archive SHA-256 value before extraction. It does not require administrator rights.

## Manual installation

Download one archive for your operating system and architecture from the [GitHub Releases](https://github.com/MiviaLabs/mivia-agent/releases) page. Download `checksums.txt` from the same release. Replace `v0.1.0` in the examples with the selected release version.

On Linux or macOS, verify the release files from the directory that contains them:

```bash
archive=mivia_0.1.0_linux_amd64.tar.gz
grep -F "  $archive" checksums.txt | sha256sum -c -
```

On macOS, use `grep -F "  $archive" checksums.txt | shasum -a 256 -c -` when `sha256sum` is not available. On Windows, use:

```powershell
Get-FileHash .\mivia_0.1.0_windows_amd64.zip -Algorithm SHA256
```

Compare the result with the matching line in `checksums.txt`. Extract the archive and place `mivia` or `mivia.exe` in a user-owned directory on `PATH`.

## Source installation

Install a published version with Go:

```bash
go install github.com/MiviaLabs/mivia-agent/cmd/mivia@v0.1.0
```

Use `@latest` only when you accept the latest published release. This method requires Go 1.25 or later.

## Upgrade and uninstall

Run the installer again with a newer pinned version. The installer replaces the existing binary.

To uninstall, remove the installed binary. The installer does not remove configuration, memory, or workflow data.

```bash
rm -f "$HOME/.local/bin/mivia"
```

On Windows, remove the installed executable:

```powershell
Remove-Item "$env:LOCALAPPDATA\mivia\bin\mivia.exe"
```

## Package managers

Homebrew, Scoop, and WinGet metadata are not active yet. Do not use package-manager commands until the project publishes an official formula, bucket entry, or WinGet manifest.

## Release integrity

GitHub Release assets include SHA-256 checksums. GitHub Actions creates build provenance attestations for the archives. Verify an archive after you download it:

```bash
gh attestation verify mivia_0.1.0_linux_amd64.tar.gz --repo MiviaLabs/mivia-agent
```

Treat a checksum mismatch, missing archive, or failed attestation verification as a release failure. Report installation failures in the repository issue tracker. Do not include API keys, memory contents, or private workspace data in an issue.
