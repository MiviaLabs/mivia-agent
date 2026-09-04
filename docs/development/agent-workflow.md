# Agent Workflow

How coding agents must work in this repository.

## Read first

1. `AGENTS.md`
2. `.agents/INDEX.md`
3. `.agents/doctrines/*`
4. Relevant rules and skills

## Standing doctrine

Always apply `.agents/doctrines/engineering-working-contract.md`.

## Task skills

- After code changes, apply `verify-code-change`
- For defect hunts, use `bug-audit` (confirmed bugs only)
- For docs, use `docs-update` and `docs/OWNERS.yaml`

The host skill lifecycle, including `resources.toml`, lazy activation, and the
scoped `read_skill_resource` capability, is documented in
[Skill System Architecture](../architecture/skills.md).

## Do

- Smallest change that satisfies the requirement
- Run real checks; never invent pass results
- Update owned docs only
- Keep binary name `mivia`

## Do not

- Bypass hooks
- Create duplicate documentation
- Process-farm subagents by default
- Leave TODO/FIXME/HACK/XXX in committed product or agent config
- Ship any CLI name other than `mivia`

## Workflow runs

Start every `feature-delivery` run with this script:

```bash
scripts/run-delivery-workflow.sh <label> <<'TASK'
...task text, any length, any number of lines...
TASK
```

The script sets `--allow-publish` and starts the run in the background. It
prints the log path, so you can start several runs and watch them together.

Do not call `mivia workflow run feature-delivery` directly. Without
`--allow-publish` the run does all the work, reaches its success terminal, then
stops at `delivery_pending` and opens no pull request.

### Live e2e test workflows

`.mivia/workflows/e2e-split-test.toml`, `.mivia/workflows/e2e-pr-metadata-test.toml`,
and `.mivia/workflows/e2e-scope-escape-test.toml`
(plus `.agents/agents/e2e-engineer.md` and `.mivia/workflows/templates/e2e-*.md`)
are real, checked-in workflows that exercise the delivery engine's repair
paths against the ACTUAL `MiviaLabs/mivia-agent` GitHub repo: real branches
pushed, real draft PRs opened, real `gh` and DeepSeek API calls.

- `e2e-split-test`: the diff-size gate and automatic split
  (`[stacking] split_deferred = true`) - its repair template deliberately
  never shrinks the diff, so the host's own split (and, when the run isn't
  part of a multi-chunk stack, delivery.EnsureFollowUpPublished) must do
  all the work.
- `e2e-pr-metadata-test`: the commit-subject repair path - implement
  deliberately emits an invalid `pr_title` on its first attempt, proving
  ValidateCommitSubject's rejection routes to repair and the agent's fix
  (reading the hint) succeeds on retry.
- `e2e-scope-escape-test`: the chunk-scope guard repair path - run in chunk
  mode with an explicit `chunk_plan` input, implement deliberately writes one
  file outside the declared slice, proving guardChunkScope's refusal routes
  to repair and the agent's fix (deleting the file per the hint) succeeds on
  retry.

**Never run any of them without the user explicitly asking for it in that
session.** They are not part of `make verify`, CI, or any other automated
path, and that must stay true. Each workflow's `description` field repeats
this warning.

When the user does ask for a live delivery-engine smoke test:

```bash
./mivia workflow run e2e-split-test --input task="short description" --allow-publish
./mivia workflow run e2e-pr-metadata-test --input task="short description" --allow-publish
./mivia workflow run e2e-scope-escape-test --input task="short description" \
  --input stack_mode=chunk --input chunk=c1 --input pr_base=main --input stack_part=1/1 \
  --input chunk_plan='{"id":"c1","title":"scope smoke","files":["testdata/e2e-smoke/scope-ok.md"]}' \
  --allow-publish
mivia stack drive e2e-split-test   # only if decompose produced a multi-chunk plan
```

Keep the `task` input short (the rendered PR title/commit subject must pass
this repo's own `.mivia/policy/commit-message.json`, ≤72 chars, `type(scope):
subject` shape). After the run settles, close and delete-branch any PR it
opened - the workflow's own PR body already says "Safe to close/delete."
Never merge one.

### Live auth smoke (`make live-auth-smoke`)

`internal/miviaauth/live_smoke_test.go` checks the CLI's `/v1/auth` model
against a real deployment. It is behind the `liveauth` build tag, so it does
not compile - and cannot run - unless someone asks for it by name. Same
never-run-without-explicit-ask rule as the workflows above; it is not part of
`make verify` or CI.

It exists because `api/contracts/auth.v1.json` is maintained by hand and its
README says it cannot see the live server drift: the snapshot pins what the Go
package models, so it catches our edits, never the API's. This test is the
other half of that guard, and it is the only thing in the repo that compares
the two.

```bash
MIVIA_LIVE_API_BASE_URL=https://... \
MIVIA_LIVE_EMAIL=... MIVIA_LIVE_PASSWORD=... make live-auth-smoke
```

Use a throwaway account, never a real user's. The run mutates real state: it
revokes the sessions it creates, and deliberately trips the server's
refresh-token theft detection once (that check is why it needs its own second
login). It spends 2 logins against the API's login rate limit per run.

Missing credentials fail the run rather than skipping it - if the tag is set,
a quiet pass would be the wrong answer.

### Live chat-session probe (`make live-chat-smoke`)

`internal/chatsync/live_contract_test.go` checks the deployed
`/v1/chat-sessions` surface: register a session, push events, read them by
cursor, stream them over SSE, and drive the remote-input long poll. It is
behind the `livechat` build tag and follows the same never-run-without-an-
explicit-ask rule as the auth smoke, with the same environment variables.

It exists because the API half of chat session sync shipped first and both
clients (the Go CLI and the web viewer) are still unwritten, so nothing else
exercises the real surface. The probe is not a client: it speaks raw HTTP with
its own wire structs, so it pins server behavior without freezing any decision
about how the CLI gets built.

It leaves ended session rows in the target database - the API has no delete
endpoint. Every row it creates is titled `mivia live probe: ...`.

**The probe found four API defects on its first run.** All four are now fixed
in `apps/api` and verified green against the deployment on 2026-08-31, so a red
run means a regression, not known debt:

| Probe | Was | Now |
|-------|-----|-----|
| `PayloadBoundIsAClientError` | 500, body carried the failing SQL and its bound parameters | 400 |
| `RejectsIntraBatchGap` | `[seq 1, seq 99]` accepted, `lastSeq` hid the hole | 400 |
| `ConsumeIsExactlyOnce` | second consume returned 200, so the loser of a race could not tell | 409 |
| `EndIsTerminal` | events still appended to an ended session | 409 |

The full run passes: lifecycle, validation and tenancy guards, SSE replay, SSE
live push, and cursor resume.

**`TestLiveChatSessionFanOutReachesEveryStream` is the multi-replica check.** It
opens six concurrent SSE streams, each on its own connection with keep-alives
off so the load balancer is free to place them, appends one event, and counts
how many streams received it. A working fan-out is six; local-only delivery is
roughly half, because a stream only hears an append served by the replica it
happens to sit on.

That count is a diagnosis rather than a hang, and it earned its place at once:
against two replicas it measured 5, then 2, then 2 of 6, which identified the
original Postgres LISTEN/NOTIFY transport as silently dead through Neon's
pooled endpoint. `LISTEN` succeeds through PgBouncer and never delivers. The
transport moved to Redis pub/sub; the probe now reads 6 of 6.

Run this one against a deployment with more than one replica. On a single
replica it passes trivially and proves nothing.

### e2e suite runner (`scripts/e2e_suite.py`)

`scripts/e2e_suite.py` is a small, versioned suite over live e2e scenarios,
so a live delivery-engine check does not mean inventing a fresh ad hoc task
prompt every time. Same never-run-without-explicit-ask rule as above; it
never runs itself and is not part of `make verify`/CI. Three scenario
kinds: **topology** (drives the real `feature-delivery` workflow with a
task engineered to force a known chunk-dependency shape - independent
chunks, a DAG diamond, a wide fan-in, a linear chain, a single-package
run), **scripted** (the checked-in `e2e-*.toml` workflows above), and
**bug-fix** (a real `bug-fix.toml` run, scope narrowed to a bug-dense area,
told to fix only the first confirmed bug rather than hunt exhaustively -
small and bounded, not an open-ended audit).

```bash
scripts/e2e_suite.py list                 # see every scenario
scripts/e2e_suite.py run independent-3    # launch one, backgrounded
scripts/e2e_suite.py run --all            # launch the whole suite in parallel
scripts/e2e_suite.py status               # summarize every launched run
scripts/e2e_suite.py kill --all           # stop every launched driver process
```

Logs land in `.mivia/run-logs/e2e-suite/`, one file per scenario name, with
a `manifest.json` tracking pid/log/start time so `status`/`kill` work in a
later session too. As with the checked-in workflows above: close and
delete-branch any PR a run opens; never merge one.

### Context-compaction e2e (`scripts/e2e_context_compaction.py`)

Drives the real `mivia` binary through automatic compaction, manual
`/compact`, the tool-enabled agent loop, and the summary-gate-off path.
Every assertion reads a surface a user or host app observes - the NDJSON
wire and the durable SQLite checkpoint - so a regression that unit tests
pass by construction still fails here.

```bash
scripts/e2e_context_compaction.py                  # hermetic, no credentials
scripts/e2e_context_compaction.py --provider real  # real API calls, costs money
```

The default `stub` backend runs its own local OpenAI-compatible server, so
it needs no key and is safe anywhere. `--provider real` uses
`DEEPSEEK_API_KEY`, `OPENROUTER_API_KEY`, `ZAI_API_KEY`, or
`OLLAMA_API_KEY` from the environment.

Same rule as every other e2e above: never part of `make verify` or CI, and
never run `--provider real` without the user asking for it in that session.

## Completion shape

Use the report shape in `.agents/rules/01-output-budget.md`.

## Skill frontmatter

Workspace skills (`.agents/skills/*/SKILL.md`) use a strict YAML subset for
frontmatter between `---` delimiters. The parser lives in
`internal/skills/frontmatter.go` and supports:

- `key: scalar` (quotes optional)
- `key: [a, b, c]` (flow sequence)
- `key:` followed by indented `- item` lines (block sequence)
- `#` comments and blank lines (skipped)

**Recognised keys:** `name`, `description`, `triggers`, `user-invocable`,
`argument-hint`, `short-description`, `tools` (optional list of required tool
names for agent skill binding).

**Rejected with a line-numbered error:**

- Nested maps, `>`/`|` block scalars, anchors, multi-document YAML
- Unknown keys (the recognised set is listed in the error)

The cap is 256 KiB, mirroring the maximum skill file size.

## Agent skills allowlist

File-backed agents may set `skills = ["…"]` in `.agents/agents/<name>.md`.
That is an **invocation allowlist**, not a preload. See
[Skill System Architecture](../architecture/skills.md#agent-skill-binding) and
this repo’s `.agents/agents/go-engineer.md` for a worked example.
