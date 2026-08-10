#!/bin/sh
set -eu

usage() {
  printf 'usage: MIVIA_VERSION=v1.2.3 %s [install-directory]\n' "${0##*/}" >&2
}

version="${MIVIA_VERSION:-${1:-}}"
if [ -z "$version" ]; then
  usage
  exit 2
fi
if ! printf '%s\n' "$version" | awk '
  $0 ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?([+][0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/ { found = 1 }
  END { exit found ? 0 : 1 }
'; then
  printf 'install: version must use semantic version format\n' >&2
  exit 2
fi

os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Linux) goos=linux;;
  Darwin) goos=darwin;;
  *) printf 'install: unsupported operating system: %s\n' "$os" >&2; exit 1;;
esac
case "$arch" in
  x86_64|amd64) goarch=amd64;;
  arm64|aarch64) goarch=arm64;;
  *) printf 'install: unsupported architecture: %s\n' "$arch" >&2; exit 1;;
esac

if [ "${2:-}" ]; then
  install_dir=$2
elif [ "${MIVIA_INSTALL_DIR:-}" ]; then
  install_dir=$MIVIA_INSTALL_DIR
elif [ "${XDG_BIN_DIR:-}" ]; then
  install_dir=$XDG_BIN_DIR
else
  install_dir=${HOME:?HOME is required}/.local/bin
fi

version_number=${version#v}
archive="mivia_${version_number}_${goos}_${goarch}.tar.gz"
base="https://github.com/MiviaLabs/mivia-agent/releases/download/${version}"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/mivia-install.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT INT TERM

download() {
  url=$1
  output=$2
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$url" -o "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only -q "$url" -O "$output"
  else
    printf 'install: curl or wget is required\n' >&2
    exit 1
  fi
}

download "$base/$archive" "$tmp/$archive"
download "$base/checksums.txt" "$tmp/checksums.txt"
expected="$(awk -v name="$archive" '$2 == name {print $1}' "$tmp/checksums.txt")"
[ -n "$expected" ] || { printf 'install: archive is missing from checksums.txt\n' >&2; exit 1; }
[ "$(printf '%s\n' "$expected" | wc -l)" -eq 1 ] || { printf 'install: archive has duplicate checksums\n' >&2; exit 1; }
actual="$(if command -v sha256sum >/dev/null 2>&1; then sha256sum "$tmp/$archive" | awk '{print $1}'; else shasum -a 256 "$tmp/$archive" | awk '{print $1}'; fi)"
[ "$actual" = "$expected" ] || { printf 'install: checksum verification failed\n' >&2; exit 1; }

mkdir -p "$install_dir"
tar -xzf "$tmp/$archive" -C "$tmp"
install -m 0755 "$tmp/mivia" "$install_dir/mivia"
printf 'installed mivia %s to %s/mivia\n' "$version" "$install_dir"
case ":${PATH}:" in
  *:"$install_dir":*) ;;
  *) printf 'add %s to PATH before running mivia\n' "$install_dir" >&2;;
esac
