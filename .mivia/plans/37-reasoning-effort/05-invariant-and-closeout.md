# Phase 05 - Example config, invariant, and closeout

Files:

- `.mivia/mivia.toml.example` - document `reasoning` and `reasoning_dialect` on
  model entries (never under `[chat]`), showing a configured model beside an
  unset one, and stating that DeepSeek needs the dialect spelled out.
- `.mivia/invariants.md` - allocate **INV-AG-36** (verified as the lowest free
  identifier at plan time; re-check before writing the row).

## Invariant text

*Reasoning control only ever ADDS fields.* When a model's reasoning level is
empty the request body is byte-identical to the pre-reasoning shape. When it is
active, exactly the resolved dialect's documented fields are added and no
sampling parameter is removed - the earlier "reasoning models reject
temperature" premise was disproved against current provider documentation, and
suppressing sampling would itself have changed valid requests. A level with no
resolvable dialect emits nothing, which is why configuration refuses an active
level on a provider with no vetted default unless `reasoning_dialect` is
explicit. The dial travels with the model binding, so every request path -
plain chat, agent loop, one-shot and multi-step subagents, routed agents, and
the non-streaming stream fallback - sends the same fields for the same binding.

## Verification

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- `make validate-invariants`
- `make verify`

Manual residual check: one configured reasoning request and one unset request
against representative provider endpoints, recording no credentials, prompts,
or provider payloads in repository artifacts.

## Known residual risks

- z.ai ignores `thinking: {"type":"disabled"}` on some server paths. Documented,
  not worked around.
- DeepSeek thinking mode expects `reasoning_content` replay on tool-call turns,
  which this slice does not implement. That is why DeepSeek has no default
  dialect; an explicit opt-in is the operator's informed choice.
- Per-model value sets are not validated client-side; the provider's 400 names
  the valid set.
