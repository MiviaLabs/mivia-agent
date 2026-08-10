#!/bin/sh
set -eu

usage() {
  printf 'usage: %s [version] [install-directory]\n' "${0##*/}" >&2
}

version="${MIVIA_VERSION:-${1:-}}"
version_from_pointer=0
if [ -n "${MIVIA_INSTALL_DIR:-}" ] && [ -n "${2:-}" ]; then
  usage
  exit 2
fi
if [ -n "$version" ] && ! printf '%s\n' "$version" | awk '
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

tmp="$(mktemp -d "${TMPDIR:-/tmp}/mivia-install.XXXXXX")"
path_lock=
staged=
trap 'if [ -n "$path_lock" ]; then rm -f "$path_lock"; fi; if [ -n "$staged" ]; then rm -f "$staged"; fi; rm -rf "$tmp"' EXIT INT TERM

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

if [ -z "$version" ]; then
  download "https://github.com/MiviaLabs/mivia-agent/releases/latest/download/mivia-version.txt" "$tmp/mivia-version.txt" || {
    printf 'install: no stable release is published yet\n' >&2
    exit 1
  }
  version="$(tr -d '\r\n' <"$tmp/mivia-version.txt")"
  version_from_pointer=1
fi
if ! printf '%s\n' "$version" | awk -v stable="$version_from_pointer" '
  stable && $0 ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/ { found = 1 }
  !stable && $0 ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?([+][0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/ { found = 1 }
  END { exit found ? 0 : 1 }
'; then
  printf 'install: version is not a valid semantic version\n' >&2
  exit 1
fi

version_number=${version#v}
archive="mivia_${version_number}_${goos}_${goarch}.tar.gz"
base="https://github.com/MiviaLabs/mivia-agent/releases/download/${version}"
download "$base/$archive" "$tmp/$archive"
download "$base/checksums.txt" "$tmp/checksums.txt"
expected_lines="$tmp/checksum-matches"
awk -v name="$archive" 'length($1) == 64 && $1 ~ /^[[:xdigit:]]+$/ && NF == 2 && $2 == name {print $1}' "$tmp/checksums.txt" >"$expected_lines"
[ -s "$expected_lines" ] || { printf 'install: archive is missing from checksums.txt\n' >&2; exit 1; }
[ "$(wc -l <"$expected_lines" | tr -d ' ')" -eq 1 ] || { printf 'install: archive has duplicate checksums\n' >&2; exit 1; }
expected="$(sed -n '1p' "$expected_lines")"
[ "${#expected}" -eq 64 ] || { printf 'install: checksum format is invalid\n' >&2; exit 1; }
actual="$(if command -v sha256sum >/dev/null 2>&1; then sha256sum "$tmp/$archive" | awk '{print $1}'; else shasum -a 256 "$tmp/$archive" | awk '{print $1}'; fi)"
[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ] || { printf 'install: checksum verification failed\n' >&2; exit 1; }

mkdir -p "$install_dir"
tar -tzf "$tmp/$archive" >"$tmp/members"
member_count=0
seen_mivia=0
seen_readme=0
seen_license=0
valid_members=1
while IFS= read -r member; do
  member_count=$((member_count + 1))
  case "$member" in
    mivia) seen_mivia=$((seen_mivia + 1));;
    README.md) seen_readme=$((seen_readme + 1));;
    LICENSE) seen_license=$((seen_license + 1));;
    *) valid_members=0 ;;
  esac
done <"$tmp/members"
unsafe_members=0
tar -tvzf "$tmp/$archive" >"$tmp/member-details"
while IFS= read -r detail; do
  first=${detail%${detail#?}}
  case "$first" in -) ;; *) unsafe_members=1;; esac
done <"$tmp/member-details"
if [ "$member_count" -ne 3 ] || [ "$seen_mivia" -ne 1 ] || [ "$seen_readme" -ne 1 ] || [ "$seen_license" -ne 1 ] || [ "$valid_members" -ne 1 ] || [ "$unsafe_members" -ne 0 ]; then
  printf 'install: archive contents are unsafe or unexpected\n' >&2
  exit 1
fi
tar -xzf "$tmp/$archive" -C "$tmp"
[ -f "$tmp/mivia" ] && [ ! -L "$tmp/mivia" ] || { printf 'install: archive has no regular mivia binary\n' >&2; exit 1; }
staged="$install_dir/.mivia.new.$$"
install -m 0755 "$tmp/mivia" "$staged"
mv -f "$staged" "$install_dir/mivia"
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

update_profile() {
  profile=$1
  marker='# mivia installer: PATH'
  end_marker='# mivia installer: PATH END'
  path_line=$(path_command "$shell_name")
  if [ -f "$profile" ] && grep -F -x -e "$marker" -e "$end_marker" "$profile" >/dev/null 2>&1; then
    if ! awk -v start="$marker" -v end="$end_marker" '
      $0 == start { starts++; if (inside || finished) bad = 1; inside = 1; next }
      $0 == end { ends++; if (!inside) bad = 1; inside = 0; finished = 1; next }
      END { exit !(starts == 1 && ends == 1 && !inside && !bad) }
    ' "$profile"; then
      return 1
    fi
    awk -v start="$marker" -v end="$end_marker" '
      $0 == start { inside = 1; next }
      inside && $0 == end { exit }
      inside { print }
    ' "$profile" >"$tmp/profile-block"
    if [ "$(wc -l <"$tmp/profile-block" | tr -d '[:space:]')" -eq 1 ] &&
      grep -F -x "$path_line" "$tmp/profile-block" >/dev/null 2>&1; then
      return 0
    fi
  fi
  if [ -f "$profile" ]; then
    cleaned="$profile.mivia.$$"
    awk -v start="$marker" -v end="$end_marker" '
      $0 == start {skip=1; next}
      skip && $0 == end {skip=0; next}
      skip {next}
      {print}
    ' "$profile" >"$cleaned" || return 1
    mv "$cleaned" "$profile" || return 1
  fi
  printf '\n%s\n%s\n%s\n' "$marker" "$path_line" "$end_marker" >>"$profile" || return 1
}

if [ "${MIVIA_NO_PATH_UPDATE:-}" != 1 ]; then
  case ":${PATH}:" in
    *:"$install_dir":*)
      printf '%s is already on PATH\n' "$install_dir"
      ;;
    *)
      shell_name=${SHELL:-sh}
      shell_name=${shell_name##*/}
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
      path_line="$(path_command "$shell_name")"
      if ! mkdir -p "$(dirname "$profile")" 2>/dev/null; then
        printf 'warning: cannot update PATH in %s\n' "$profile" >&2
        printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
      elif [ -L "$profile" ] || { [ -f "$profile" ] && [ ! -w "$profile" ]; }; then
        printf 'warning: cannot update PATH in %s\n' "$profile" >&2
        printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
      else
        lock_candidate="$profile.mivia.lock"
        if ! (set -C; : >"$lock_candidate") 2>/dev/null; then
          printf 'warning: another installer is updating %s\n' "$profile" >&2
          printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
        else
          path_lock=$lock_candidate
          if update_profile "$profile" 2>/dev/null; then
          printf 'added %s to PATH in %s\n' "$install_dir" "$profile"
          if [ "$shell_name" = fish ]; then
            printf 'open a new shell or run: source %s\n' "$profile"
          else
            printf 'open a new shell or run: . %s\n' "$profile"
          fi
          rm -f "$path_lock"
          path_lock=
          else
          printf 'warning: cannot update PATH in %s\n' "$profile" >&2
          printf 'run this command, then open a new shell: %s\n' "$path_line" >&2
          fi
        fi
      fi
      ;;
  esac
else
  printf 'PATH update skipped by MIVIA_NO_PATH_UPDATE\n' >&2
  shell_name=${SHELL:-sh}
  printf 'run this command, then open a new shell: %s\n' "$(path_command "${shell_name##*/}")" >&2
fi
