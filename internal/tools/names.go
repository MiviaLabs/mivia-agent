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
		"write_file",
		"search_replace",
		RunCommandToolName,
		"search",
		"fetch_url",
		"extract",
		"find_references",
		SkillResourceToolName,
	}
	out := slices.Clone(names)
	slices.Sort(out)
	return out
}

// IsKnownToolName reports whether name appears in the compiled catalogue.
func IsKnownToolName(name string) bool {
	_, ok := allToolNameSet()[name]
	return ok
}

func allToolNameSet() map[string]struct{} {
	set := make(map[string]struct{}, 16)
	for _, n := range AllToolNames() {
		set[n] = struct{}{}
	}
	return set
}
