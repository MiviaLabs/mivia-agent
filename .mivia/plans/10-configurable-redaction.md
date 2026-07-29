# 10 — Redaction is configuration, not code

**Status:** Design-ready.
**Date:** 2026-07-30
**Depends on:** nothing. **Blocks:** nothing.
**Blast radius:** HIGH — removes a security default. Read §5 before implementing.

---

## 1. Problem

Five independent redaction implementations are compiled into the binary. Each
has its own pattern list, its own key-name list, and its own idea of what a
secret looks like. None is configurable, and none can be turned off.

Re-derived at HEAD 2026-07-30:

| # | Site | What is hardcoded |
|---|---|---|
| 1 | `internal/agent/loop_tools.go:101-102,149` | `sensitiveToolText`, `privateKeyBlock` regexes; `redactJSONValue` key names (`password`, `token`, `secret`, `api_key`, `authorization`) |
| 2 | `internal/tools/run.go:342-361` | `scrubSecrets` value prefixes (`github_pat_`, `sk-ant-`, `ghp_`, `sk-`) + `isKeyChar` |
| 3 | `internal/runtime/dispatcher.go:421-422,437` | `sensitiveText`, `sensitivePEM` regexes; `scrub` key names — a *different* list including `private`, `prompt`, `reasoning`, `ssn`, `email`, `phone` |
| 4 | `internal/cli/toolpanel.go:12-19` | `previewSecretPattern`, `previewPrivateKeyBlock`, plus a third Bearer regex |
| 5 | `internal/provider/openai_compat.go:445` | `sanitizeErr` — *not* redaction despite the name; it only strips newlines and truncates. Out of scope, but the name invites the assumption that provider errors are scrubbed. They are not. |

Four separate answers to "what is a secret", none of which a user can inspect,
extend, or disable. The drift is not hypothetical:

- Site 3 redacts `email`, `phone`, `ssn`, `prompt`, `reasoning`; sites 1, 2 and 4
  do not.
- Site 2 knows `sk-ant-` and `xox…` is absent; site 3 knows `xox[baprs]-` and
  site 1 knows neither.
- Site 4 compiles a regex **on every call** (`toolpanel.go:17`), inside the
  render path.
- Site 1 leaked the credential in `Authorization: Bearer <tok>` until
  2026-07-30 because the pattern stopped at the scheme word — a bug that
  existed only in that copy.

Correctness aside, the behaviour is wrong in both directions: it over-redacts
ordinary text (any prose containing `Bearer ` is mangled) and under-redacts
anything the four lists happen not to name.

## 2. Decision

**One redaction engine, driven entirely by configuration. Nothing is compiled
in. With no configuration, nothing is redacted.**

> This deliberately makes redaction **fail open**. An unconfigured workspace
> sends tool previews, event bodies and audit metadata through untouched. That
> is the point: what counts as a secret is a property of a workspace, and a
> binary guessing on the user's behalf is what produced five disagreeing lists.
> §5 states the cost plainly and §6 requires the docs to lead with it.

Consistent with the same decision already taken for secret paths, the
`run_command` allowlist, and the environment allowlist (plans and commits
`2dcdd08`, `3ce279f`, `673299a`): policy lists live in `mivia.toml`, recommended
values ship in `.mivia/mivia.toml.example`, and an unconfigured workspace gets
none of them.

### Configuration shape

```toml
[privacy]
# Existing key, unchanged: hides run_command argv wholesale.
redact_tool_args = false

# Regexes applied to operator-visible text (tool previews, event bodies,
# audit metadata). Each match is replaced by redaction_placeholder.
# Unset or empty = no text redaction anywhere.
redaction_patterns = [
  '(?i)(?:password|passwd|token|secret|api[_-]?key|authorization)\s*[:=]\s*\S+',
  '(?i)bearer\s+[A-Za-z0-9._~+/=-]+',
  '(?:sk-ant-|sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9._~-]+',
  '(?is)-----BEGIN [A-Z0-9 ]+PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]+PRIVATE KEY-----|$)',
]

# JSON object keys whose values are replaced wholesale. Case-insensitive
# substring match on the key name. Unset or empty = no key-based redaction.
redaction_key_names = ["password", "token", "secret", "api_key", "authorization"]

redaction_placeholder = "[redacted]"   # default when unset
```

**One mechanism, not three.** The value-prefix scrubbing of site 2 and the
Bearer handling of sites 1/3/4 are expressible as ordinary patterns, so they
collapse into `redaction_patterns`. Only key-based JSON elision needs its own
list, because it replaces a *value* selected by its *key* rather than by
matching the value's text.

### Rejected

- **Keep a compiled floor "just for obvious secrets".** That is the current
  design with fewer authors. A floor nobody can see or disable is what let four
  lists drift apart, and it still would not cover the workspace-specific
  patterns that actually matter (internal token formats, customer identifiers).
- **Per-site configuration.** Four config sections mirroring four call sites
  preserves the drift in TOML instead of Go.
- **Inject a policy through every constructor.** Sites 3 and 4 (`runtime`
  dispatcher, `cli` tool panel) have no access to resolved config and would
  need signature changes across the dispatch and render paths. Rejected in
  favour of the process-wide setter below, which matches the existing
  `tools.SetRedactToolArgs` precedent.

## 3. Design

New package `internal/redact` — no dependencies on `agent`, `tools`, `runtime`
or `cli`, so all four call sites can use it without an import cycle.

```go
type Policy struct { /* compiled patterns, key names, placeholder */ }

// Compile validates and compiles a policy. A bad regex is a startup error,
// never a silent no-op.
func Compile(patterns, keyNames []string, placeholder string) (*Policy, error)

// Text and JSONValue are no-ops on a nil or empty Policy.
func (p *Policy) Text(s string) string
func (p *Policy) JSONValue(v any) any

// Process-wide policy, set once after config load. Zero value redacts nothing.
func SetPolicy(p *Policy)
func Text(s string) string
func JSONValue(v any) any
```

**The zero value must redact nothing.** Every site calls the package-level
helpers, so a path that runs before `SetPolicy` (tests, `mivia version`) is
unredacted rather than panicking or falling back to a compiled list.

**Compile once, at startup.** Site 4 currently compiles a regex per render
call; the whole point of a compiled `Policy` is that no call site builds a
regex. An invalid `redaction_patterns` entry must fail `config.Load` with the
offending pattern named, not be skipped.

## 4. Changes

| Site | File | Change |
|---|---|---|
| Config | `internal/config/types.go:72` | add `RedactionPatterns`, `RedactionKeyNames`, `RedactionPlaceholder` to `PrivacyConfig` |
| Config | `internal/config/load.go` | compile the policy during `Load`; a bad regex is a fatal error naming the pattern |
| New | `internal/redact/redact.go` | the engine (~120 LOC) |
| Wiring | `internal/cli/chat_command.go:45` | `redact.SetPolicy(...)` beside the existing `tools.SetRedactToolArgs` |
| 1 | `internal/agent/loop_tools.go` | delete `sensitiveToolText`, `privateKeyBlock`, `redactSensitiveText`; `redactToolInput`/`redactToolOutputForTool` call `redact.Text`; `redactJSONValue`'s key list comes from the policy |
| 2 | `internal/tools/run.go` | delete `scrubSecrets` and `isKeyChar`; the two call sites (`:129` output, `:172` argv header) call `redact.Text` |
| 3 | `internal/runtime/dispatcher.go` | delete `sensitiveText`, `sensitivePEM`, `redactText`; `scrub`'s key list comes from the policy. `prompt` and `reasoning` are **dropped, not migrated** — see §4a |
| 4 | `internal/cli/toolpanel.go:12-19` | delete all three patterns; `redactPreview` calls `redact.Text` |

Net: four pattern sets and three key lists become one config section.

### 4a. `prompt` and `reasoning` are never redacted — DECIDED

`runtime.scrub` uniquely elides any key containing `prompt` or `reasoning`.
Neither is a secret: they are the agent's own instructions and its own
deliberation, and eliding them was audit-volume control wearing a privacy
label. Redacting them makes audit metadata useless for the thing it exists for
— reconstructing why an agent did something — while protecting nothing.

**They are dropped from the key list and do not become configurable.** A user
who genuinely wants them elided can add `prompt` to `redaction_key_names`
themselves, but nothing in the shipped configuration will do it for them, and
no compiled path will.

This also removes the one case where redaction changed what a *reader* could
learn about mivia's own behaviour rather than what a *third party* could learn
about the user's secrets. Those are different problems; only the second one is
redaction's job.

## 5. What this costs

**An unconfigured workspace redacts nothing, anywhere.** Concretely, after this
lands and before a user writes any config:

- `run_command` output containing an API key is shown verbatim in the TUI and
  written to the session transcript on disk.
- Tool argument previews reach every `EventBus` sink with credentials intact.
- Audit metadata (`runtime.Meta.RedactedInput/RedactedOutput` — names that
  become misleading) carries whatever the tool saw.

This is a **removal of a security default**, not a refactor. Two consequences
the implementer must not skip:

1. **`docs/security/overview.md` must lead with it.** Line 25 today reads
   "Redacted diagnostics for secrets patterns (API keys) remain always-on for
   output scrub". That sentence becomes false the moment this ships.
2. **Three invariants change meaning** and must be amended in
   `.mivia/invariants.md` in the same commit:
   - `INV-AG-5` ("tool argument redaction is opt-in, default shows args") —
     still true, but its test `TestRedactToolInputDefaultShowsArgs` now passes
     trivially.
   - `INV-SEC-2` ("privacy redaction of tool args is off by default") — now
     the whole-system default, not just tool args.
   - `INV-TUI-7` cites `TestToolStatusLine_RedactsSecrets`, which asserts
     redaction happens. It must become "redacts secrets **when a policy is
     configured**" or the invariant is false at defaults.

## 6. Verification

```bash
go build ./... && go vet ./...
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**New tests:**

- `TestPolicyZeroValueRedactsNothing` — the load-bearing one. A nil and an empty
  `Policy` return input unchanged for text and JSON.
- `TestCompileRejectsInvalidPattern` — a bad regex fails `Compile` and names the
  pattern; it must not be silently dropped.
- `TestNoCompiledRedactionPatterns` — mechanical, mirrors
  `TestNoHardcodedLegacyNamespace`: walks the Go sources and fails on a
  `regexp.MustCompile` whose literal contains a credential keyword
  (`bearer`, `api[_-]?key`, `sk-`, `ghp_`, `PRIVATE KEY`) outside
  `internal/redact` and `_test.go`. Without this the lists grow back one call
  site at a time, which is exactly how four of them appeared.
- `TestUnconfiguredWorkspaceRedactsNothingEndToEnd` — integration: a real tool
  batch carrying a credential, asserting it appears verbatim in the event
  preview. Documents the fail-open posture as tested behaviour.
- `TestConfiguredPolicyRedactsEverySite` — integration: one policy, and a
  credential is redacted in the tool preview, the `run_command` body, the audit
  metadata and the TUI preview. This is what proves the four sites really share
  one engine.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Reintroduce a compiled default pattern list | `TestNoCompiledRedactionPatterns`, `TestUnconfiguredWorkspaceRedactsNothingEndToEnd` |
| M2 | Make `Compile` skip an invalid pattern instead of erroring | `TestCompileRejectsInvalidPattern` |
| M3 | Wire only one of the four sites to the shared policy | `TestConfiguredPolicyRedactsEverySite` |
| M4 | Make the zero-value policy fall back to any pattern | `TestPolicyZeroValueRedactsNothing` |

**Test fallout to expect:** 13 files carry ~50 redaction assertions
(`internal/agent`, `internal/tools`, `internal/runtime`, `internal/cli`,
`internal/coordinator`, `internal/config`). Most assert that a hardcoded
pattern fires. Each must be **retargeted onto a configured policy**, not
deleted — a test that asserted real behaviour still has a job once the source of
the pattern moves. Deleting them would silently drop the only coverage that the
engine works at all.

**Docs:** `docs/security/overview.md` (rewrite the always-on claim),
`docs/product/config.md` (the new keys and the default-off posture).
Both are OWNERS-registered; rule 00 requires them in the same commit.

## 7. Rollback criterion

If shipping default-off proves to leak credentials into recorded sessions in
practice, the answer is **not** to reintroduce a compiled list (§2 rejects it).
It is to make `.mivia/mivia.toml.example` the file a new workspace actually
starts from — i.e. ship an `init` path that writes a config with the
recommended patterns present — so the defaults are visible and editable rather
than invisible and fixed.
