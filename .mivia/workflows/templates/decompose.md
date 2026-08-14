# Decompose Agent — Stacked Small-PR Delivery

## Read-first contract

You are the decompose agent. The plan step output is bound as `plan`. Read
the bound value first and classify it:

1. **Complete plan object** — the value parses as a single JSON object whose
   keys include `summary`, `steps`, `inspected`, and `addressed_findings`.
   The engine inlined the FULL plan artifact; this is the authoritative plan.
   Work from it directly. Do not call `workflow_inspect`.
2. **Ledger-reference envelope** — the value is an object whose keys are
   exactly `artifact` and `note`, with `artifact` carrying `step`, `attempt`,
   `ref`, `bytes`, `digest` (and optionally a short `preview`) and `note`
   naming the workflow ledger. This means the plan exceeded the engine's
   inline cap for prior-step evidence (32KiB). The envelope's `note` invites
   `workflow_inspect(run_id, step, attempt)`; you may attempt it ONCE with
   the `artifact` fields. In this worktree context it will not resolve (your
   ledger view predates the plan step's output). If it does resolve, work
   from the full artifact. If it does not resolve, FAIL CLOSED: emit
   `stack_mode: "no_bug"` with an empty `chunk_plan` (`chunks: []`). The
   engine rejects `no_bug` deterministically when the plan declares steps and
   reroutes you here; a repeated rejection exhausts the bounded repair loop
   and fails the run honestly. NEVER guess plan content from `preview` or
   from the Evidence refs block — a guessed chunk plan could pass schema
   checks while misrepresenting the plan.
3. **Anything else** — FAIL CLOSED the same way: `stack_mode: "no_bug"` with
   an empty `chunk_plan`, and let the engine's deterministic rejection of
   `no_bug` on a step-declaring plan drive the honest outcome.

Reply with ONLY the output envelope: a `<mivia_output>` opening tag on its own
line, then one JSON object satisfying the output schema at
`schemas/chunk-plan-v1.json`, then a `</mivia_output>` closing tag on its own
line. No prose, markdown report, headings, bullets, or code fences inside or
outside the envelope. An invalid shape is rejected and you will be asked again
with the schema.

---

## Your task

Inspect the plan output and decide the stack mode, then produce a chunk plan
that the engine can validate and execute.

### Step 1 — Decide stack_mode

| Mode | When to use |
|------|-------------|
| `no_bug` | ONLY when the plan declares zero actionable steps (its `steps`
  array is empty). The engine rejects `no_bug` deterministically when the
  plan declares any step and reroutes it back to you. A plan with steps MUST
  be `single` or `multi`. |
| `single` | The plan declares actionable steps and the change is small
  enough for one PR. Output exactly one chunk. |
| `multi` | The plan declares actionable steps and the change must be split
  into multiple small PRs. Produce the required chunks (see constraints
  below). |

Use the workflow's `[stacking]` thresholds when they are present; otherwise use
`soft_lines=200`, `hard_lines=400`, `max_files=5`,
`max_chunks=12`.

### Rejected verdicts (repair iterations)

On a repair iteration the engine has rejected your previous decompose output.
The rejected verdict is bound as `prior_chunk_plan`. If it is a complete
chunk-plan object, read it and do NOT repeat the rejected verdict. If it is a
ledger-reference envelope you cannot resolve (see the Read-first contract),
treat it as absent and emit a fresh valid chunk plan per the mode table
above. A `no_bug` verdict on a plan with steps is rejected deterministically,
and a repeated rejection exhausts the repair loop and fails the run.

### Step 2 — Produce chunks (multi mode only)

For `multi` mode, produce chunks that satisfy ALL of these constraints:

1. **Small diffs**: Each chunk's `est_diff_lines` must be ≤ `soft_lines`
   (default 200) and must never exceed `hard_lines` (default 400).
2. **File limit**: Each chunk contains at most `max_files` (default 5) files.
3. **Disjoint file sets**: No file path may appear in more than one chunk.
   Every chunk's `files` array must be unique within that chunk and unique
   across all chunks.
4. **Tests required**: Every chunk must include tests. Set `tests` to `true`
   for every chunk. A chunk without tests is invalid.
5. **DAG dependencies**: `depends_on` must form a directed acyclic graph.
   Each entry is a chunk `id` string. No chunk may depend on itself directly
   or transitively. There must be no cycles.
6. **Chunk count**: Total chunks must be ≤ `max_chunks` (default 12).
7. **Chunk identity**: Each chunk has a unique `id` (string, minLength 1) and
   a descriptive `title` (string, minLength 1).

### Step 3 — Output

Produce the JSON object with these top-level keys:

- `stack_mode`: one of `"no_bug"`, `"single"`, `"multi"`.
- `chunk_plan`: object with required key `chunks` (array of chunk objects).

For `no_bug` mode, `chunks` is an empty array.
For `single` mode, `chunks` contains exactly one chunk with `depends_on` empty.
For `multi` mode, `chunks` contains 2–12 chunks with `depends_on` forming a
valid DAG.

The output MUST validate against `schemas/chunk-plan-v1.json`. If it does not,
the engine will reject it and ask you to re-emit.

## Output contract

Reply with ONLY the output envelope: a `<mivia_output>` opening tag on its own
line, then one JSON object satisfying the output schema appended to this task,
then a `</mivia_output>` closing tag on its own line. Do not use a skill
report format, markdown, or extra fields. The schema declares the only valid
keys. An invalid shape is rejected and you will be asked again with the
schema.
