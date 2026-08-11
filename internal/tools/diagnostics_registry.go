package tools

// diagnostics_registry.go owns the registration side of the get_diagnostics
// tool (locked plan v2): the declared result budget and the function that
// decides whether the tool is advertised. Keeping registration here (instead
// of in default_registry.go) keeps the default-registry file under the
// structure policy and groups the tool's surface with its lifecycle.

import "github.com/MiviaLabs/mivia-agent/internal/workspace"

// diagnosticsDefaultBudget is the result-envelope byte bound for
// get_diagnostics when the operator sets no tighter cap. 256 KiB is the
// dispatcher's ceiling floor, so the tool's declared budget cannot raise the
// shared output ceiling. An operator's max_tool_result_bytes clamps the
// budget tighter, exactly as registerCodeNavTools clamps navMaxBytes.
const diagnosticsDefaultBudget = 256 << 10

// registerDiagnosticsTool registers the get_diagnostics tool. The tool is
// advertised only when it can succeed. All of these must hold:
//
//   - the operator configured DiagnosticsCommands,
//   - the workspace has a root,
//   - the run_command allowlist is non-empty,
//   - the default command resolves (resolveAllowedCommand succeeds: its
//     argv[0] is allowlisted and resolvable on PATH).
//
// The default command is the "default" entry when present, else the sole
// configured command when exactly one exists, else none - with no default
// and multiple commands the tool still registers and every selection is
// probed at Execute time (the model must name a command). The v1 gate is
// preserved: the default's argv gates registration exactly as the legacy
// single-command argv did. When any condition fails, the tool is silently
// absent from the registry. It never registers as an error-returning stub
// (the same advertised-iff-can-succeed contract as run_command and extract).
// The result budget is diagnosticsDefaultBudget, clamped by MaxToolResultBytes.
func registerDiagnosticsTool(register func(Tool), opts DefaultOptions, ws *workspace.Root, allowlist []string, envExact map[string]bool, envPrefix []string, envBlockedExact map[string]bool, keywordBlock []string, patterns, exceptions []string) {
	commands := opts.DiagnosticsCommands
	if len(commands) == 0 || ws == nil || len(allowlist) == 0 {
		return
	}
	// The default command is what Execute runs when the "command" argument
	// is omitted: the reserved "default" entry, else the sole configured
	// command, else none (with multiple commands and no "default", callers
	// must name one - the ambiguity is resolved at Execute time).
	defaultName := ""
	if _, ok := commands["default"]; ok {
		defaultName = "default"
	} else if len(commands) == 1 {
		for name := range commands {
			defaultName = name
		}
	}
	// v1-consistent gate: the default command's argv is probed exactly as
	// the legacy single-command surface probed its argv. A default that
	// cannot resolve means the advertised command could never run, so the
	// tool stays absent. (Non-default commands are probed at Execute time,
	// on the argv actually selected by the "command" argument.)
	if defaultName != "" {
		if _, _, err := resolveAllowedCommand(commands[defaultName], allowlist); err != nil {
			return
		}
	}
	maxBytes := diagnosticsDefaultBudget
	if opts.MaxToolResultBytes > 0 {
		maxBytes = min(maxBytes, opts.MaxToolResultBytes)
	}
	register(&getDiagnosticsTool{
		ws:                   ws,
		allowlist:            allowlist,
		commands:             commands,
		defaultName:          defaultName,
		timeoutSec:           opts.RunTimeoutSec,
		maxBytes:             maxBytes,
		envExact:             envExact,
		envPrefix:            envPrefix,
		envBlockedExact:      envBlockedExact,
		envKeywordBlock:      keywordBlock,
		secretPathExceptions: exceptions,
		secretPathPatterns:   patterns,
	})
}
