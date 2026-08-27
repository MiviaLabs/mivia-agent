# Mutation Policy & Kill-Rate Floors

This directory holds per-package mutation testing policies and kill-rate floors for `scripts/check_mutation.py`.

## Schema
Each file is named `<package_with_underscores>.json` (e.g. `internal_hooks.json` for `internal/hooks`):

```json
{
  "floor": 0.80,
  "denylist": [
    {
      "file": "hooks.go",
      "snippet": "...",
      "reason": "audited equivalent mutant: defensive fallback unreachable in unit test"
    }
  ]
}
```

- `floor`: minimum kill rate ratio (0.0 to 1.0) required for the package to pass.
- `denylist`: list of audited equivalent/benign mutants excluded from the denominator.
