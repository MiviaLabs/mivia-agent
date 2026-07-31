# Model-Facing Surfaces: Project- and Language-Generic

**Non-negotiable product rule.** mivia is a **generic coding-agent host**. Users run it in any workspace (Go, Python, TypeScript, Rust, polyglot, docs-only). Agents working **on this repository** must never re-introduce host-language or product-stack bias into the model-facing surfaces listed below.

This repo is confusing on purpose: the product is an agent CLI for agents. Do not conflate:

| Surface | May be Go / mivia-specific? |
|---------|------------------------------|
| Host implementation (`cmd/mivia`, `internal/**`, Makefile, hooks) | Yes — this product is written in Go |
| Model-facing tool `Description()`, parameter `description` fields, `OpenAITools()` | **No** — always project- and language-generic |
| Compiled-in `defaultAgentPrompt` / `defaultSystemPrompt` (fallback for any workspace) | **No** — generic coding agent |
| Portable `architecture-review` skill | **No** — discover workspace-native structure, evidence, and checks |
| Other workspace-local skills | Yes — they may encode that workspace's policy and workflow |
| Workspace file `.mivia/agent-prompt.md` **in a user project** | Yes — that project's conventions |
| Workspace file `.mivia/agent-prompt.md` **in this repo** | Yes — *this* product's Go build/test knowledge |
| Docs under `docs/product/` describing the tool product contract | Prefer generic examples; multi-ecosystem when listing commands |

## What "generic" means

1. **Tool names and schemas** describe capabilities (read, search, edit, run allowlisted argv), not a language.
2. **Examples** in descriptions use multiple ecosystems or language-neutral paths (`*.md`, `src/**/*.ts`, `["make","test"]`, `["npm","test"]`) — never Go-only (`go test ./...`, `*.go` as the sole glob example).
3. **`run_command`** is LAST RESORT for file work; filesystem tools first. Prefer project-discovered verify commands (Makefile, package.json scripts, cargo, pytest, etc.), not a hardcoded `go test`.
4. **The shipped `run_allowlist`** in `.mivia/mivia.toml.example` stays multi-ecosystem. No allowlist is compiled into the binary, so this guarantee lives in the example config and is asserted by `TestExampleAllowlistIsMultiEcosystem`.
5. **Never** put this module path, `cmd/mivia`, or product-only architecture into tool `Description()` strings.
6. The portable `architecture-review` skill must not require this product, a fixed
   repository layout, a version-control system, a build tool, or a programming
   language. Repository-local orchestration and report envelopes stay outside it.

## Mechanical enforcement

- Go tests: `internal/tools/generic_surface_test.go` — fails CI if model-facing tool text matches language-bias patterns.
- Go tests: `internal/cli/prompt_generic_test.go` — fails if compiled-in default prompts are stack-biased.
- Semgrep: `mivia.generic.architecture-review-must-stay-portable` — rejects a tested
  regression corpus of product, fixed-path, version-control, language, and build-tool
  assumptions in the portable skill. `scripts/test_semgrep_rules.py` checks the
  rule's scope and positive/negative behavior. This guard is not exhaustive semantic
  proof; review must reject any unqualified ecosystem assumption the corpus misses.
- Do not weaken those tests to “fix” a biased description; fix the description.

## When editing tools

1. Change behavior in `internal/tools/*`.
2. Keep `Description()` / schema prose generic.
3. Run `go test ./internal/tools/ ./internal/cli/ -count=1`.
4. Update `docs/product/agent.md` with multi-ecosystem wording if the product contract changed.
5. Update **this rule** only if the generic contract itself changes.

## When editing the portable architecture skill

1. Describe architecture in terms of artifacts, boundaries, consumers, contracts,
   dependencies, data, deployment, and ownership.
2. Discover instructions, baselines, dependency models, and verification mechanisms
   from the workspace; do not prescribe this repository's paths or commands.
3. Cover non-versioned, polyglot, infrastructure, data, and documentation scopes.
4. Keep repository-specific invocation and reporting requirements in the caller's
   control surface.
5. Keep at most one explicit ecosystem recommendation on a line so a conditional
   qualifier binds unambiguously.
6. Run `python3 scripts/test_semgrep_rules.py` and the Semgrep scan.

## Allowed exceptions

- Unit-test **fixtures** may use `*.go` sample files.
- Host code comments and Go package docs may mention Go.
- Workspace-local `.mivia/agent-prompt.md` may document this repo's `go test` / `make verify` workflow.
