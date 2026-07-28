# Research Findings: Go Embed Patterns for Agent Instructions

> **Date:** 2025-04-27
> **Scope:** Patterns for embedding agent instructions (markdown) into Go binaries using `//go:embed`, with fallback/disk-seeding strategies.

---

## 1. Core `go:embed` Mechanics (Go 1.16+)

- **Directive syntax:** `//go:embed <pattern>` — must immediately precede a package-level `string`, `[]byte`, or `embed.FS` variable.
- **String/[]byte:** Single file only, embedded as raw content. Use `import _ "embed"` (blank import) when not using `embed.FS`.
- **`embed.FS`:** Directory tree or glob pattern; implements `fs.FS` interface. Supports `Open`, `ReadDir`, `ReadFile`. Paths are forward-slash, case-sensitive.
- **Exclusions:** Files starting with `.` or `_` are excluded by default; use `all:` prefix to include them.
- **Build-time only:** Files must exist at compile time; embedded data is immutable at runtime.

```go
//go:embed instructions/*.md
var instructionsFS embed.FS

//go:embed AGENTS.md
var defaultAgentsMD string
```

---

## 2. Embedding Markdown Files — Patterns

| Pattern | Use Case | Example |
|---------|----------|---------|
| Single file as `string` | One canonical agent instruction file | `//go:embed AGENTS.md var agentsMD string` |
| Single file as `[]byte` | Binary processing needed | `//go:embed CLAUDE.md var claudeBytes []byte` |
| Directory glob with `embed.FS` | Multiple instruction files (hierarchy) | `//go:embed instructions/*.md var instFS embed.FS` |
| Subdirectory tree | Versioned/organized instructions | `//go:embed agents/rules/*.md var rulesFS embed.FS` |
| Multiple patterns per `embed.FS` | Mixed file types | `//go:embed prompts/*.md config/*.yaml` |

**Key insight:** For agent instructions, `embed.FS` is preferred over `string`/`[]byte` when you have multiple files (e.g., separate files for system prompt, tool definitions, rules). Single-file instructions are simpler and embed as a `string`.

---

## 3. `embed.FS` Best Practices (Go 1.16–1.26+)

- **Read-only & concurrent-safe:** `embed.FS` is safe for concurrent access from multiple goroutines.
- **Use `fs.Sub` for path scoping:** Create a sub-filesystem to strip prefix directories before passing to HTTP or template parsers.

```go
subFS, _ := fs.Sub(instructionsFS, "instructions")
tmpl, _ := template.ParseFS(subFS, "*.md")
```

- **`fs.WalkDir` for discovery:** Iterate embedded files at init time to build registries or caches.
- **Build tags for environments:** Use `//go:build dev` / `//go:build prod` to swap between unminified and minified assets, or between dev-only and release instructions.
- **Memory overhead:** Every embedded file resides in RAM for process lifetime. Keep agent instruction files small (they are, typically <100KB).
- **Pre-parse at init:** Parse templates / markdown at startup rather than on each request.

---

## 4. Serve Embedded Files as Fallback When Disk Files Don't Exist

This is the **core pattern** for allowing user-overridable agent instructions while shipping defaults embedded in the binary.

### Pattern: "Try disk first, fall back to embed"

```go
type AgentInstructionProvider struct {
    embedded embed.FS
    diskPath string // e.g., ".ai/instructions/"
}

func (p *AgentInstructionProvider) ReadFile(name string) ([]byte, error) {
    // 1. Try disk first (allows user overrides)
    diskFile := filepath.Join(p.diskPath, name)
    if data, err := os.ReadFile(diskFile); err == nil {
        return data, nil
    }
    // 2. Fall back to embedded default
    return p.embedded.ReadFile(name)
}
```

### Variant: "Production-embed, development-disk" (Brandur pattern)

```go
var fileSystem http.FileSystem
if isProduction {
    fileSystem = http.FS(embeddedAssets)
} else {
    fileSystem = http.Dir("./static")
}
```

### Variant: Overlay filesystem (os.DirFS + embed.FS joined)

```go
// Merge: disk overrides embedded
var combinedFS fs.FS
if _, err := os.Stat(diskPath); err == nil {
    combinedFS = os.DirFS(diskPath)
} else {
    combinedFS = instructionsFS
}
```

**Source:** [Brandur — go:embed in prod, serve-from-disk in development](https://brandur.org/fragments/go-embed)

---

## 5. Write Embedded Files to Disk on First Run ("Seeding")

This pattern is used when tools need to make embedded defaults visible & editable on disk.

### Full directory copy from embed.FS to disk

```go
func SeedInstructions(embedded embed.FS, targetDir string) error {
    return fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
        if err != nil {
            return err
        }
        if d.IsDir() {
            return os.MkdirAll(filepath.Join(targetDir, path), 0755)
        }
        data, err := embedded.ReadFile(path)
        if err != nil {
            return err
        }
        dest := filepath.Join(targetDir, path)
        if _, err := os.Stat(dest); os.IsNotExist(err) {
            if err := os.WriteFile(dest, data, 0644); err != nil {
                return err
            }
        }
        return nil
    })
}
```

### Single file seeding

```go
func SeedDefaultInstructions(path string) error {
    if _, err := os.Stat(path); os.IsNotExist(err) {
        return os.WriteFile(path, []byte(defaultAgentsMD), 0644)
    }
    return nil // already exists, don't overwrite
}
```

**Key principle:** **Never overwrite** user-modified files. Only write if the file does not exist (`os.IsNotExist`). This respects user edits while still setting up defaults for new environments.

**Source:** [StackOverflow — How to create a copy of an embed directory](https://stackoverflow.com/questions/70314526/how-to-create-a-copy-of-an-embed-directory-using-golang)

---

## 6. How Other AI Tools Handle Self-Contained Agent Instructions

| Tool / Pattern | Approach |
|---------------|----------|
| **Claude Code** | `CLAUDE.md` at repo root; hierarchical fallback (project → global). No embedded binary pattern. |
| **GitHub Copilot** | `.github/copilot-instructions.md`; reads from disk. |
| **Cursor** | `.cursorrules` / `.cursor/rules/`; disk-based. |
| **Generic AGENTS.md** | Emerging convention: `AGENTS.md` at repo root. Some tools check `CLAUDE.md` first, then `AGENTS.md` as fallback. |

**Key observation:** Most AI tools use **disk-based** instruction files. The Go embed pattern applies when you are **building a Go CLI tool or agent** that ships self-contained with its own instructions (e.g., a Go-based coding agent that carries its own prompt files). This is common in:

- Go-based AI agent frameworks (e.g., `agent-sdk-go`, Zep's agentic Go patterns)
- CLI tools that embed system prompts and tool definitions
- Self-contained binaries that must work offline with no external file dependencies

**Relevant Go AI agent projects:**
- [mhmtszr/go-guidelines](https://github.com/mhmtszr/go-guidelines) — Ships version-aware Go guidelines as embedded markdown
- TutorialEdge's Go AI agent patterns — Embed tool descriptions and system prompts in the binary

---

## 7. Recommended Architecture for Embedding Agent Instructions

```
Binary
 ├── embedded embed.FS
 │   ├── AGENTS.md          (canonical instructions)
 │   ├── prompts/*.md       (prompt templates)
 │   └── rules/*.md         (rule files)
 │
 ├── SeedInstructions()     (write-to-disk-on-first-run)
 │   └── only if files don't exist
 │
 └── Provider               (read-time fallback)
     ├── ReadFile → try disk first
     └──             → fall back to embed
```

### Init-order flow:

1. `init()` or early startup: call `SeedInstructions(embedded, ".ai/instructions")`
2. Create `Provider{embedded, diskPath: ".ai/instructions"}`
3. At query time: `Provider.ReadFile("AGENTS.md")` → returns user-modified or embedded default

---

## 8. Key Takeaways

1. **Prefer `embed.FS`** over `string`/`[]byte` for multi-file agent instruction sets.
2. **Fallback pattern** (disk → embed) enables user overrides without breaking defaults.
3. **Seed-only-if-not-exists** preserves user edits across software updates.
4. **`fs.WalkDir`** is the idiomatic way to copy an entire embedded tree to disk.
5. **AI tools** currently use disk-based instruction files; Go embed is the mechanism for **self-contained Go binaries** that carry their own instructions.
6. **Build tags** can switch between dev and prod instruction sets.
7. **Pre-parse at init** for performance (e.g., parse markdown frontmatter or template instructions once).

---

## References

- [Go embed package documentation](https://pkg.go.dev/embed) — Official reference
- [Brandur: go:embed in prod, serve-from-disk in development](https://brandur.org/fragments/go-embed) — Dev/prod asymmetry pattern
- [Rez Moss: Embedded File Systems — Using embed.FS in Production](https://dev.to/rezmoss/embedded-file-systems-using-embedfs-in-production-89-2fpa) — Complete production patterns
- [StackOverflow: Best practice to go:embed assets up the file tree](https://stackoverflow.com/questions/66726902/best-practice-to-goembed-assets-up-the-file-tree) — Directory layout advice
- [JetBrains: How to Use go:embed in Go](https://blog.jetbrains.com/go/2021/06/09/how-to-use-go-embed-in-go-1-16) — Introductory guide
- [GitHub: wbern/claude-instructions](https://github.com/wbern/claude-instructions) — AI agent instruction file conventions
