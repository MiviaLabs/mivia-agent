#!/usr/bin/env bash
# Install mivia Git hooks: core.hooksPath points to .githooks (absolute path).
# Uses the main repo root even when run from inside a worktree, so hooks are
# shared across all worktrees.
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "${ROOT}" ]]; then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi

# When inside a git worktree, resolve to the main repo root so the hooks
# path is the shared .githooks directory, not a worktree-local copy.
if git worktree list >/dev/null 2>&1; then
  MAIN_ROOT="$(git worktree list --porcelain 2>/dev/null | awk '/^worktree /{print $2; exit}')"
  if [[ -n "${MAIN_ROOT:-}" && -d "${MAIN_ROOT}/.githooks" ]]; then
    ROOT="$MAIN_ROOT"
  fi
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
  scripts/docs-check \
  scripts/git-hooks/file-size-check \
  scripts/git-hooks/run_without_git_env 2>/dev/null || true

# Use absolute path so hooks resolve correctly in all worktrees.
# --local writes to .git/config (shared across worktrees) which is the
# right scope: every tree in this repo gets the same hooks.
HOOKS_DIR="$(cd "$ROOT/.githooks" && pwd)"
git config core.hooksPath "$HOOKS_DIR"
git config push.autoSetupRemote true

printf 'Installed mivia Git hooks via core.hooksPath=%s\n' "$HOOKS_DIR"
printf 'Required local commands: python3; go/gofmt when go.mod exists; semgrep for pre-push\n'
