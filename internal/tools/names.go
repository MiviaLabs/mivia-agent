package tools

import "slices"

// AllToolNames returns a sorted catalogue of every workspace tool name the
// binary can register via NewDefaultRegistry (with a workspace and no
// DisableTools). Agent validation uses this to distinguish typos from
// intentionally disabled tools.
//
// Session-control and ledger tools registered only by the CLI are not listed
// here; they are not selectable agent allowlist names.
func AllToolNames() []string {
	// Keep in registration order of registerDefaultTools, then sort for a
	// stable catalogue contract.
	names := []string{
		"read_file",
		"list_dir",
		"grep",
		"glob",
		"inspect_repository",
		"write_file",
		"delete_file",
		"search_replace",
		MultiEditToolName,
		RunCommandToolName,
		"search",
		"fetch_url",
		// "extract" stays in the catalogue because it CAN be registered: with
		// TAVILY_API_KEY configured it is a real workspace tool. It is the one
		// conditionally registered name here - without the key the default
		// registry does not construct it (a keyless extract could never succeed,
		// so it is absent, not error-returning). The catalogue is static so
		// unknown-tool validation still recognizes the name regardless of config.
		"extract",
		"find_references",
		"list_symbols",
		"go_to_definition",
		"find_symbol_context",
		// "get_diagnostics" is conditionally registered, like "extract". It is
		// a real workspace tool only when [tools] diagnostics_command is set
		// AND its argv[0] is on the effective run_command allowlist. The
		// catalogue is static, so unknown-tool validation still recognizes
		// the name regardless of config.
		"get_diagnostics",
		"memory_save",
		"memory_search",
		"memory_delete",
		SkillResourceToolName,
		// Phase 7 workflow tools: registered only when the workspace has
		// .mivia/workflows/. Names stay in the static catalogue so allowlist
		// validation recognizes them regardless of workspace shape (same
		// pattern as conditionally registered "extract").
		"workflow_run",
		"workflow_status",
		"workflow_events",
		"workflow_inspect",
		"workflow_list_runs",
		"workflow_deliver",
		"workflow_cancel",
		"workflow_delete",
	}
	out := slices.Clone(names)
	slices.Sort(out)
	return out
}

// DeclaredToolNames returns the static declared-tool catalogue: every name in
// AllToolNames except the activation-only read_skill_resource capability.
// Skill frontmatter `tools:` requirements and agent TOML tool declarations are
// validated against this catalogue (plan 43), so neither surface can statically
// require or declare the invocation-scoped resource reader.
func DeclaredToolNames() []string {
	all := AllToolNames()
	out := make([]string, 0, len(all)-1)
	for _, name := range all {
		if name == SkillResourceToolName {
			continue
		}
		out = append(out, name)
	}
	return out
}

// IsKnownToolName reports whether name appears in the compiled catalogue.
func IsKnownToolName(name string) bool {
	_, ok := allToolNameSet()[name]
	return ok
}

// IsDeclaredToolName reports whether name is a statically declared tool that
// skills and agent TOMLs may reference (plan 43). The activation-only
// read_skill_resource capability is deliberately excluded.
func IsDeclaredToolName(name string) bool {
	_, ok := declaredToolNameSet()[name]
	return ok
}

func allToolNameSet() map[string]struct{} {
	set := make(map[string]struct{}, 16)
	for _, n := range AllToolNames() {
		set[n] = struct{}{}
	}
	return set
}

func declaredToolNameSet() map[string]struct{} {
	set := make(map[string]struct{}, 16)
	for _, n := range DeclaredToolNames() {
		set[n] = struct{}{}
	}
	return set
}
