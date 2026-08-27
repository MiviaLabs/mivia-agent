package tools

// DefaultRunAllowlist is the built-in set of programs run_command may
// execute with no [tools] run_allowlist configured. It is open-by-default,
// deliberately smaller than the fuller list in .mivia/mivia.toml.example:
// common compilers/interpreters, their package managers, git, and read-only
// Unix utilities that cannot mutate the filesystem or escalate into
// arbitrary execution. It excludes shells (sh, bash - unrestricted execution
// makes the allowlist concept moot), file-mutating utilities (rm, cp, mv,
// mkdir, chmod, tar, and friends - run_command is not gated by [tools]
// write_path_blocklist, so a mutating program here would bypass it
// entirely), "find" (its -exec/-delete flags run arbitrary commands and
// delete files), and networking/container/infra tools (curl, wget, ssh,
// docker, kubectl, terraform). A project adds any of those explicitly via
// [tools] run_allowlist if it wants them; [tools] run_allowlist_only
// replaces this list entirely rather than extending it.
//
// Owned here, not by internal/config: run_command is the enforcer of this
// policy, and per .agents/rules/60-tools-project-language-generic.md every
// model-facing tool implementation must stay project/language-generic and
// reusable outside this app - a tool package importing the app's own config
// system inverts that. internal/config imports this constant (not the
// reverse) to validate [tools] diagnostics_commands entries against the
// same effective allowlist run_command enforces.
var DefaultRunAllowlist = []string{
	"git",
	"make", "cmake", "ninja",
	"go", "gofmt",
	"node", "npm", "npx", "yarn", "pnpm", "bun",
	"python", "python3", "pip", "pip3", "pytest",
	"cargo", "rustc",
	"ruby", "gem", "bundle", "rake", "rspec",
	"java", "javac", "mvn", "gradle",
	"php", "composer", "phpunit",
	"ls", "cat", "pwd", "echo", "grep", "egrep", "fgrep",
	"sed", "awk", "head", "tail", "sort", "uniq", "cut", "tr", "wc",
	"diff", "which", "whoami", "date", "hostname", "uname", "env",
	"true", "false", "yes", "printf", "basename", "dirname",
	"realpath", "readlink",
}
