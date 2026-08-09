#!/usr/bin/env bash
# Build release binaries for every supported platform into dist/ and write
# dist/checksums.txt. Prints the gh command to publish a GitHub Release.
# This is the local twin of .github/workflows/release.yml.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

pkg="$(go list -m)/internal/version"
tag="$(git describe --tags --abbrev=0 2>/dev/null || echo dev)"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
ldflags="-X ${pkg}.Version=${tag#v} -X ${pkg}.Commit=${commit} -X ${pkg}.Dirty=clean"

rm -rf dist
mkdir -p dist

build() {
  local goos="$1" goarch="$2" name="$3"
  echo "building ${goos}/${goarch} -> dist/${name}"
  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 \
    go build -trimpath -ldflags "${ldflags}" -o "dist/${name}" ./cmd/mivia
}

build linux amd64 mivia-linux-amd64
build linux arm64 mivia-linux-arm64
build darwin amd64 mivia-darwin-amd64
build darwin arm64 mivia-darwin-arm64
build windows amd64 mivia-windows-amd64.exe
build windows arm64 mivia-windows-arm64.exe

if command -v sha256sum >/dev/null 2>&1; then
	(cd dist && sha256sum mivia-* > checksums.txt)
else
	(cd dist && shasum -a 256 mivia-* > checksums.txt)
fi

echo "artifacts in dist/"
echo "publish with: gh release create v${tag#v} --generate-notes dist/*"
