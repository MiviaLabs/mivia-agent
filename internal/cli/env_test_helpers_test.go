package cli

// replaceTestEnv is a package-local copy of internal/cliworktree's helper of
// the same name (worktree_lifecycle_orphan_test.go): a generic os.Environ
// filter/override with no worktree dependency, needed here for
// workflow_kill_recovery_test.go's git-command environment fixtures.

import "strings"

func replaceTestEnv(current []string, replacements map[string]string) []string {
	result := make([]string, 0, len(current)+len(replacements))
	for _, entry := range current {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := replacements[key]; !replace {
			result = append(result, entry)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}
