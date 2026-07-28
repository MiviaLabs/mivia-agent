## Handoff: challenge-outputs-persist
- **Problem**: `dispatch_tasks` returns ephemeral `ref:output:` pointers that can't be fetched. Challenge outputs were lost.
- **Root cause**: Agent prompts said "report findings" but not "write your findings to a file". Orchestrator can't read refs.
- **Fix**: Challenge agents must write their output to `.ai/plan/<name>/evidence/challenge-<N>.md` directly.
