#!/bin/sh
# PostToolUse: gofmt the file the agent just wrote.
#
# Reactive, never blocking - the write already happened, and PostToolUse has no
# denial channel anyway. The point is that the next `make verify` does not fail
# on formatting the agent could have fixed at the moment it edited.
#
# MIVIA_FILE is the tool's top-level `path` argument, which write_file and
# search_replace both carry. It is passed through the environment and never
# re-parsed as syntax, so a filename containing shell characters is inert - but
# it is still quoted here, because this script is the example people copy.

set -eu

file="${MIVIA_FILE:-}"

# Not a Go file, or a tool call with no path: nothing to do, and say nothing.
# A hook that narrates its own no-ops turns the transcript into noise.
case "$file" in
  *.go) ;;
  *) exit 0 ;;
esac

[ -f "$file" ] || exit 0

# No gofmt on this machine is the operator's situation to know about, not an
# error in this call. Exit 1 is a non-blocking warning: stderr reaches the
# operator, the tool result is untouched.
if ! command -v gofmt >/dev/null 2>&1; then
  printf 'gofmt is not on PATH; %s was left unformatted\n' "$file" >&2
  exit 1
fi

# -l lists the file only when it is not already formatted, so the common case
# writes nothing at all to the model's context.
if [ -z "$(gofmt -l "$file" 2>/dev/null)" ]; then
  exit 0
fi

if ! gofmt -w "$file" 2>/dev/null; then
  printf 'gofmt could not rewrite %s (does it parse?)\n' "$file" >&2
  exit 1
fi

printf 'gofmt reformatted %s\n' "$file"
exit 0
