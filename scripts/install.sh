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

case "$install_dir" in
  /*) ;;
  *) install_dir="$(mkdir -p "$install_dir" && CDPATH= cd -- "$install_dir" && pwd -P)" ;;
esac

version_number=${version#v}
archive="mivia_${version_number}_${goos}_${goarch}.tar.gz"
base="https://github.com/MiviaLabs/mivia-agent/releases/download/${version}"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/mivia-install.XXXXXX")"
path_lock=
trap 'if [ -n "$path_lock" ]; then rm -f "$path_lock"; fi; rm -rf "$tmp"' EXIT INT TERM

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

shell_quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

path_command() {
  case "$1" in
    fish) printf "set -gx PATH %s \$PATH" "$(shell_quote "$install_dir")";;
    *) printf 'export PATH=%s:"$PATH"' "$(shell_quote "$install_dir")";;
  esac
}

if [ "${MIVIA_NO_PATH_UPDATE:-}" != 1 ]; then
  case ":${PATH}:" in
    *:"$install_dir":*)
      printf '%s is already on PATH\n' "$install_dir"
      ;;
    *)
      shell_name=${SHELL##*/}
      case "$shell_name" in
        bash)
          if [ -n "${MIVIA_SHELL_RC:-}" ]; then profile=$MIVIA_SHELL_RC
          elif [ -f "${HOME:?HOME is required}/.bash_profile" ]; then profile=$HOME/.bash_profile
          elif [ -f "$HOME/.bash_login" ]; then profile=$HOME/.bash_login
          elif [ -f "$HOME/.profile" ]; then profile=$HOME/.profile
          else profile=$HOME/.bashrc
          fi
          ;;
        zsh) profile=${MIVIA_SHELL_RC:-${HOME:?HOME is required}/.zshrc} ;;
        fish) profile=${MIVIA_SHELL_RC:-${HOME:?HOME is required}/.config/fish/config.fish} ;;
        *) profile=${MIVIA_SHELL_RC:-${HOME:?HOME is required}/.profile} ;;
      esac
      marker='# mivia installer: PATH'
      path_line="$(path_command "$shell_name")"
      if ! mkdir -p "$(dirname "$profile")" 2>/dev/null; then
        printf 'warning: cannot update PATH in %s\n' "$profile" >&2
        printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
      elif [ -L "$profile" ] || { [ -f "$profile" ] && [ ! -w "$profile" ]; }; then
        printf 'warning: cannot update PATH in %s\n' "$profile" >&2
        printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
      else
        path_lock="$profile.mivia.lock"
        if ! (set -C; : >"$path_lock") 2>/dev/null; then
          printf 'warning: another installer is updating %s\n' "$profile" >&2
          printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
        elif grep -F -q "$marker" "$profile" 2>/dev/null &&
          grep -F -q "$path_line" "$profile" 2>/dev/null; then
          printf 'PATH setup already exists in %s\n' "$profile"
          rm -f "$path_lock"
          path_lock=
        elif printf '\n%s\n%s\n' "$marker" "$path_line" >>"$profile" 2>/dev/null; then
          printf 'added %s to PATH in %s\n' "$install_dir" "$profile"
          printf 'open a new shell or run: . %s\n' "$profile"
          rm -f "$path_lock"
          path_lock=
        else
          printf 'warning: cannot update PATH in %s\n' "$profile" >&2
          printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
        fi
      fi
      ;;
  esac
else
  printf 'PATH update skipped by MIVIA_NO_PATH_UPDATE\n' >&2
  printf 'run this command, then open a new shell: %s\n' "$(path_command "${SHELL##*/}")" >&2
fi
