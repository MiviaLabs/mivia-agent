# Phase 04 - Existing provider wiring and example config (superseded; do not implement)

The parent plan is blocked. Do not expose configuration until a replacement has
an explicit, tested capability contract for each provider/model pair.

Files:

- Modify `internal/provider/deepseek.go`.
- Modify `internal/provider/zai.go`.
- Modify `internal/provider/openrouter.go`.
- Modify `.mivia/mivia.toml.example`.

Assign the intended dialect to each existing provider constructor. Preserve
existing behavior when the model has no reasoning field. Document reasoning on
model entries, not under `[chat]`, and show an unset model beside a configured
model.

Tests must assert constructor-level dialect selection without making network
calls. Keep provider-specific value/capability matrices out of config validation.

Gate: focused provider/config tests plus `make docs-check` if the repository
documentation gate includes the example config surface.
