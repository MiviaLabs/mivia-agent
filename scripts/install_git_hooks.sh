#!/usr/bin/env bash
# Install mivia Git hooks: core.hooksPath=.githooks
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "${ROOT}" ]]; then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi
cd "$ROOT"

HOOKS=(
  pre-commit
  pre-push
  prepare-commit-msg
  commit-msg
  post-commit
)

for hook in "${HOOKS[@]}"; do
  wrapper=".githooks/${hook}"
  impl="scripts/git-hooks/${hook}"
  [[ -f "$wrapper" ]] || {
    printf 'install_git_hooks: missing %s\n' "$wrapper" >&2
    exit 1
  }
  [[ -f "$impl" ]] || {
    printf 'install_git_hooks: missing %s\n' "$impl" >&2
    exit 1
  }
  chmod +x "$wrapper" "$impl"
done

chmod +x \
  scripts/install_git_hooks.sh \
  scripts/run_agent_hook_guard.sh \
  scripts/agent_hook_guard.py \
  scripts/secret_scan.py \
  scripts/check_docs_ownership.py \
  scripts/verify_agent_config.py \
  scripts/test_git_hooks.py \
  scripts/test_secret_scan.py \
  scripts/test_docs_ownership.py \
  scripts/test_agent_hook_guard.py \
  scripts/test_semgrep_rules.py \
  scripts/secret-scan \
  scripts/docs-check 2>/dev/null || true

git config core.hooksPath .githooks

printf 'Installed mivia Git hooks via core.hooksPath=.githooks\n'
printf 'Required local commands: python3; go/gofmt when go.mod exists; semgrep for pre-push\n'
