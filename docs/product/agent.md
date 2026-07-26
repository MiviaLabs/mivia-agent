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
| `run_command` | Run allowlisted command (e.g. `go`, `make`) |

## Safety

- Paths must stay under `--workspace` (default: current directory).
- `.env` and secret-like files are not readable via tools.
- `run_command` is **not** a free shell: pass `argv` as a string array; binary must be allowlisted.
- Default allowlist includes: `go`, `gofmt`, `git`, `make`, `rg`, `python3`.

## Loop

The model may call tools repeatedly (up to `max_steps`, default 30) until it returns a final answer.

## See also

- ADR `docs/adr/0005-agent-tools.md`
- Config: `docs/product/config.md`
