#!/usr/bin/env bash
# mivia agent hook runner: apply repo guard before optional binary hook.
set -euo pipefail

AGENT="${1:?agent surface required}"
EVENT="${2:?hook event required}"

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "${ROOT}" ]]; then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi

PAYLOAD_FILE="$(mktemp)"
trap 'rm -f "$PAYLOAD_FILE"' EXIT
cat >"$PAYLOAD_FILE"

set +e
GUARD_OUTPUT="$(python3 "$ROOT/scripts/agent_hook_guard.py" "$AGENT" "$EVENT" <"$PAYLOAD_FILE")"
GUARD_STATUS=$?
set -e

if [[ "$GUARD_STATUS" -ne 0 ]]; then
  if [[ -n "$GUARD_OUTPUT" ]]; then
    printf '%s\n' "$GUARD_OUTPUT"
  fi
  exit "$GUARD_STATUS"
fi

if [[ -n "$GUARD_OUTPUT" ]]; then
  printf '%s\n' "$GUARD_OUTPUT"
  exit 0
fi

# Optional product binary hook once `mivia` ships a hook subcommand.
if command -v mivia >/dev/null 2>&1; then
  if mivia hook --help >/dev/null 2>&1; then
    TIMEOUT="${MIVIA_AGENT_HOOK_TIMEOUT_SECONDS:-30}"
    exec "$ROOT/scripts/git-hooks/run_with_timeout" "$TIMEOUT" mivia hook "$AGENT" "$EVENT" <"$PAYLOAD_FILE"
  fi
fi

exit 0
