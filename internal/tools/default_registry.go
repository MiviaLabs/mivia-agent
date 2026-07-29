package tools

import (
	"net/http"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// DefaultOptions configures built-in tools.
type DefaultOptions struct {
	Workspace                                                                  *workspace.Root
	RunAllowlist, RunAllowlistOnly, RunBlocklist, DisableTools                 []string
	RunTimeoutSec, MaxReadBytes, MaxOutputBytes, MaxWriteKB, MaxListDirEntries int
	TavilyAPIKey                                                               string
	EnvAllowlist, EnvAllowlistOnly, EnvBlocklist                               []string
	SecretPathPatterns, SecretPathExceptions                                   []string
}

// DefaultAllowlist is the default run_command binary allowlist.
// Intentionally multi-language/multi-ecosystem: mivia is a generic coding agent host.
// Prefer bare names only; paths are rejected at execute time.
var DefaultAllowlist = []string{
	"git", "make", "cmake", "ninja", "sh", "bash", "go", "gofmt", "govulncheck",
	"python", "python3", "pip", "pip3", "pytest", "uv", "nox", "node", "npm", "npx", "yarn", "pnpm", "bun", "deno", "tsx",
	"cargo", "rustc", "rustfmt", "clippy-driver", "ruby", "bundle", "gem", "rake", "rspec", "php", "composer", "phpunit",
	"java", "javac", "mvn", "gradle", "kotlin", "kotlinc", "dotnet", "dotnet-script", "swift", "swiftc", "zig", "zigcc", "elixir", "mix", "erl", "ghc", "cabal", "stack", "hlint", "perl", "cpan", "R", "lua", "luac",
	"awk", "sed", "grep", "egrep", "fgrep", "xargs", "tee", "envsubst", "tar", "gzip", "gunzip", "bzip2", "bunzip2", "xz", "unxz", "unzip", "zip", "zstd",
	"mkdir", "mkdirp", "rm", "cp", "mv", "touch", "ln", "chmod", "chown", "install", "mktemp", "realpath", "readlink", "basename", "dirname",
	"rg", "ag", "fd", "fzf", "bat", "delta", "echo", "ls", "cat", "tac", "nl", "od", "xxd", "hexdump", "pwd", "true", "false", "yes", "seq", "printf",
	"head", "tail", "sort", "uniq", "wc", "cut", "tr", "fold", "fmt", "diff", "patch", "comm", "cmp", "sdiff", "join", "paste", "expand", "unexpand", "strings", "iconv", "base64", "uuencode", "uudecode",
	"timeout", "nice", "nohup", "stdbuf", "parallel", "curl", "wget", "ssh", "scp", "docker", "kubectl", "helm", "terraform", "tofu", "vagrant",
	"env", "which", "id", "whoami", "date", "hostname", "uname", "arch", "nproc", "jq", "yq", "tomlq", "strace", "ltrace", "perf", "tracepath", "traceroute", "gdb", "lldb", "dlv", "ps", "top", "htop", "free", "df", "du", "lsof", "dmesg", "sysctl", "uptime", "sqlite3", "psql", "mysql", "redis-cli", "nano", "vim", "vi", "less", "more", "most", "convert", "identify", "magick", "ffmpeg", "ffprobe", "sox", "gh", "nix", "nix-shell", "nix-build", "nix-env",
}

// NewDefaultRegistry registers all v1 tools.
func NewDefaultRegistry(opts DefaultOptions) *Registry {
	normalizeDefaultOptions(&opts)
	secretPatterns, secretExceptions := configuredSecretPaths(opts)
	allowlist := configuredRunAllowlist(opts)
	envExact, envPrefix := resolveEnvAllowlist(opts.EnvAllowlist, opts.EnvAllowlistOnly, opts.EnvBlocklist)
	disabled := disabledToolNames(opts.DisableTools)
	r := NewRegistry()
	registerDefaultTools(r, opts, allowlist, envExact, envPrefix, secretPatterns, secretExceptions, disabled)
	return r
}

func normalizeDefaultOptions(opts *DefaultOptions) {
	if opts.MaxReadBytes <= 0 {
		opts.MaxReadBytes = 256 * 1024
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = 200_000
	}
	if opts.MaxWriteKB <= 0 {
		opts.MaxWriteKB = 500
	}
	if opts.MaxListDirEntries <= 0 {
		opts.MaxListDirEntries = 500
	}
	if opts.RunTimeoutSec <= 0 {
		opts.RunTimeoutSec = 300
	}
}

func configuredSecretPaths(opts DefaultOptions) ([]string, []string) {
	patterns, exceptions := DefaultSecretPathPatterns, DefaultSecretPathExceptions
	if len(opts.SecretPathPatterns) > 0 {
		patterns = opts.SecretPathPatterns
	}
	if len(opts.SecretPathExceptions) > 0 {
		exceptions = opts.SecretPathExceptions
	}
	return patterns, exceptions
}

func configuredRunAllowlist(opts DefaultOptions) []string {
	allowlist := DefaultAllowlist
	if opts.RunAllowlistOnly != nil {
		allowlist = opts.RunAllowlistOnly
	}
	normalized := make([]string, 0, len(allowlist)+len(opts.RunAllowlist))
	for _, program := range allowlist {
		normalized = append(normalized, strings.ToLower(program))
	}
	for _, program := range opts.RunAllowlist {
		normalized = append(normalized, strings.ToLower(program))
	}
	blocked := disabledToolNames(opts.RunBlocklist)
	filtered := normalized[:0]
	for _, program := range normalized {
		if !blocked[program] {
			filtered = append(filtered, program)
		}
	}
	return filtered
}

func disabledToolNames(names []string) map[string]bool {
	disabled := make(map[string]bool, len(names))
	for _, name := range names {
		disabled[strings.ToLower(name)] = true
	}
	return disabled
}

func registerDefaultTools(r *Registry, opts DefaultOptions, allowlist []string, envExact map[string]bool, envPrefix []string, patterns, exceptions []string, disabled map[string]bool) {
	register := func(tool Tool) {
		if !disabled[strings.ToLower(tool.Name())] {
			r.Register(tool)
		}
	}
	ws := opts.Workspace
	register(&readFileTool{ws: ws, maxBytes: opts.MaxReadBytes, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&listDirTool{ws: ws, maxEntries: opts.MaxListDirEntries, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&grepTool{ws: ws, maxMatches: 50, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&globTool{ws: ws, maxMatches: 200, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&writeFileTool{ws: ws, maxWriteKB: opts.MaxWriteKB, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&searchReplaceTool{ws: ws, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&runCommandTool{ws: ws, allowlist: allowlist, timeoutSec: opts.RunTimeoutSec, maxOut: opts.MaxOutputBytes, redactArgs: RedactToolArgs(), envExact: envExact, envPrefix: envPrefix, secretPathExceptions: exceptions, secretPathPatterns: patterns})
	register(&webSearchTool{ws: ws, maxFetchKB: 100, httpClient: &http.Client{Timeout: 15 * time.Second}, tavilyKey: opts.TavilyAPIKey})
	register(&fetchURLTool{ws: ws, maxLocalBytes: opts.MaxReadBytes, maxFetchKB: 100, httpClient: &http.Client{Timeout: 15 * time.Second}, fetchClient: newSafeFetchHTTPClient(15 * time.Second)})
	register(&extractTool{tavilyKey: opts.TavilyAPIKey, httpClient: &http.Client{Timeout: 15 * time.Second}})
}
