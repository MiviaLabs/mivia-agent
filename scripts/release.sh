#!/usr/bin/env bash
# Build the release archives and checksums without publishing them.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

usage() {
  printf 'usage: %s vMAJOR.MINOR.PATCH[-prerelease]\n' "${0##*/}" >&2
}

version_from_tag() {
  local requested="${1:-}"
  if [[ -n "$requested" ]]; then
    if ! git rev-parse --verify --quiet "refs/tags/${requested}^{commit}" >/dev/null; then
      printf 'release: tag does not exist locally: %s\n' "$requested" >&2
      exit 1
    fi
    if [[ "$(git rev-parse "${requested}^{commit}")" != "$(git rev-parse HEAD)" ]]; then
      printf 'release: tag does not point to HEAD: %s\n' "$requested" >&2
      exit 1
    fi
    printf '%s' "$requested"
    return
  fi

  local tags
  tags="$(git tag --points-at HEAD --list 'v*' | sort)"
  if [[ "$(printf '%s\n' "$tags" | sed '/^$/d' | wc -l)" -ne 1 ]]; then
    printf 'release: pass one version tag or place one version tag on HEAD\n' >&2
    exit 1
  fi
  printf '%s' "$tags"
}

tag="$(version_from_tag "${1:-}")"
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  printf 'release: invalid version tag: %s\n' "$tag" >&2
  usage
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  printf 'release: working tree is not clean\n' >&2
  exit 1
fi

version="${tag#v}"
commit="$(git rev-parse --short=12 HEAD)"
pkg="$(go list -m)/internal/version"
ldflags="-X ${pkg}.Version=${version} -X ${pkg}.Commit=${commit} -X ${pkg}.Dirty=clean"
dist="${repo_root}/dist"
if [[ -L "$dist" ]]; then
  printf 'release: dist must not be a symbolic link\n' >&2
  exit 1
fi
build_root="$(mktemp -d "${TMPDIR:-/tmp}/mivia-release.XXXXXX")"
trap 'rm -rf "$build_root"' EXIT
mkdir -p "$dist"
find "$dist" -maxdepth 1 -type f \( -name 'mivia_*.tar.gz' -o -name 'mivia_*.zip' -o -name checksums.txt -o -name install.sh -o -name install.ps1 -o -name mivia-version.txt \) -delete

version_output="$(env -u GOOS -u GOARCH CGO_ENABLED=0 go run -trimpath \
  -ldflags "$ldflags" ./cmd/mivia version --json)"
VERSION_OUTPUT="$version_output" EXPECTED_VERSION="$version" python3 - <<'PY'
import json
import os
data = json.loads(os.environ["VERSION_OUTPUT"])
assert data["binary"] == "mivia"
assert data["version"] == os.environ["EXPECTED_VERSION"]
PY

verify_binary() {
  local binary="$1"
  if [[ "$binary" == "${build_root}/linux-amd64/mivia" ]] && [[ "$(uname -s)" == "Linux" ]] && [[ "$(uname -m)" == "x86_64" ]]; then
    "$binary" --version | grep -F "mivia ${version}" >/dev/null
  elif ! grep -a -F "${version}" "$binary" >/dev/null; then
    printf 'release: version is missing from %s\n' "$binary" >&2
    exit 1
  fi
}

archive_name() {
  local goos="$1" goarch="$2"
  if [[ "$goos" == "windows" ]]; then
    printf 'mivia_%s_%s_%s.zip' "$version" "$goos" "$goarch"
  else
    printf 'mivia_%s_%s_%s.tar.gz' "$version" "$goos" "$goarch"
  fi
}

build_archive() {
  local goos="$1" goarch="$2" binary="mivia" archive
  archive="$(archive_name "$goos" "$goarch")"
  [[ "$goos" == "windows" ]] && binary="mivia.exe"
  mkdir -p "${build_root}/${goos}-${goarch}"
  printf 'building %s/%s\n' "$goos" "$goarch"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$ldflags" \
    -o "${build_root}/${goos}-${goarch}/${binary}" ./cmd/mivia
  verify_binary "${build_root}/${goos}-${goarch}/${binary}"
  cp README.md LICENSE "${build_root}/${goos}-${goarch}/"
  if [[ "$goos" == "windows" ]]; then
    command -v zip >/dev/null 2>&1 || { printf 'release: zip is required\n' >&2; exit 1; }
    (cd "${build_root}/${goos}-${goarch}" && zip -q -X "${dist}/${archive}" "$binary" README.md LICENSE)
  else
    tar -C "${build_root}/${goos}-${goarch}" -czf "${dist}/${archive}" "$binary" README.md LICENSE
  fi
}

build_archive linux amd64
build_archive linux arm64
build_archive darwin amd64
build_archive darwin arm64
build_archive windows amd64
build_archive windows arm64

expected=6
actual="$(find "$dist" -maxdepth 1 -type f \( -name 'mivia_*.tar.gz' -o -name 'mivia_*.zip' \) | wc -l)"
[[ "$actual" -eq "$expected" ]] || { printf 'release: expected %d archives, found %d\n' "$expected" "$actual" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$dist" && sha256sum mivia_*) > "${dist}/checksums.txt"
else
  command -v shasum >/dev/null 2>&1 || { printf 'release: sha256sum or shasum is required\n' >&2; exit 1; }
  (cd "$dist" && shasum -a 256 mivia_*) > "${dist}/checksums.txt"
fi
cp scripts/install.sh scripts/install.ps1 "$dist/"
if [[ "$tag" != *-* ]]; then
  printf '%s\n' "$tag" >"$dist/mivia-version.txt"
fi

printf 'release: created %d archives in %s\n' "$expected" "$dist"
printf 'release: publish only after review with gh release create %s --verify-tag --generate-notes dist/mivia_* dist/checksums.txt dist/install.sh dist/install.ps1\n' "$tag"
