# Coding Agent Mode

`mivia chat` runs as a **coding agent** by default: the model can call tools to read, search, edit, and run allowlisted commands in a workspace.

## Enable / disable

```bash
mivia chat                    # tools on
mivia chat --no-tools         # pure LLM chat
mivia chat -p "fix the test" # one-shot agent task
mivia chat --workspace /path/to/repo
```

## Tools

| Tool | Purpose |
|------|---------|
| `read_file` | Read a file |
| `list_dir` | List a directory |
| `grep` | Search file contents |
| `glob` | Find paths by pattern |
| `write_file` | Create or overwrite a file |
| `search_replace` | Replace exact text once in a file |
| `run_command` | Last-resort allowlisted argv (no shell); multi-ecosystem binaries |

Tool names, descriptions, and schemas are **project- and language-generic**. mivia is a host coding agent for any workspace. Do not reintroduce a single-stack bias into model-facing tool text (see `.ai/rules/60-tools-project-language-generic.md`).

## Safety

- Paths must stay under `--workspace` (default: current directory).
- `.env` and secret-like files are not readable via tools.
- `run_command` is **not** a free shell: pass `argv` as a string array; binary must be allowlisted.
- Default allowlist is multi-ecosystem (`git`, `make`, language toolchains, package managers, `rg`, …) and excludes shells/network fetchers.

## Loop

The model may call tools repeatedly. The default is unlimited; configure `/steps N` for an explicit per-turn limit. Cancellation, provider failure, or a final assistant response ends the run.

## See also

- ADR `docs/adr/0005-agent-tools.md`
- Config: `docs/product/config.md`
