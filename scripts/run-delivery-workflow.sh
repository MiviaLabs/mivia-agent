#!/usr/bin/env bash
# Start a feature-delivery workflow run that publishes its pull request.
#
# Use this script for every feature-delivery run. It sets --allow-publish, which
# a draft-mode run needs to create the pull request. Without the flag the run
# does all the work, reaches its success terminal, and then stops at
# delivery_pending with no pull request. Recovery is a second manual command.
#
# Usage:
#   scripts/run-delivery-workflow.sh <label> <<'TASK'
#   ...task text, any length, any number of lines...
#   TASK
#
#   scripts/run-delivery-workflow.sh <label> "one line task"
#
# <label> names the log file. Use a short slug, for example "vcs-hunt".
#
# The run starts in the background. The script prints the log path and returns
# at once, so you can start several runs and watch them together.
#
# Log directory: $MIVIA_RUN_LOG_DIR, or ./.mivia/run-logs when it is not set.
set -uo pipefail

usage() {
	echo "usage: $0 <label> [task]        (task on stdin when the argument is absent)" >&2
	exit 2
}

[ $# -ge 1 ] || usage
label="$1"
shift

case "$label" in
*/* | "") echo "$0: label must be a plain name, not a path" >&2; exit 2 ;;
esac

if [ $# -ge 1 ]; then
	task="$*"
else
	task="$(cat)"
fi

if [ -z "${task//[[:space:]]/}" ]; then
	echo "$0: task is empty" >&2
	exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root" || exit 1

# Prefer the freshly built binary in the repository root. Fall back to the
# binary on PATH so the script also works from an installed mivia.
if [ -x "./mivia" ]; then
	bin="./mivia"
elif command -v mivia >/dev/null 2>&1; then
	bin="mivia"
else
	echo "$0: no mivia binary; run 'make build' first" >&2
	exit 1
fi

log_dir="${MIVIA_RUN_LOG_DIR:-$repo_root/.mivia/run-logs}"
mkdir -p "$log_dir" || exit 1
log="$log_dir/${label}.log"

# --allow-publish is the point of this script. Draft mode still opens the pull
# request as a draft; the flag only permits the publish step to run at all.
nohup "$bin" workflow run feature-delivery \
	--allow-publish \
	--input task="$task" \
	>"$log" 2>&1 &

echo "started label=$label pid=$! log=$log"
