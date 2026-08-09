package tools

import "strings"

func (t *runCommandTool) filterEnv(env []string) []string {
	// A nil exactSet is an empty allowlist, not a request for defaults: with
	// nothing configured, no variable is passed through.
	exactSet := t.envExact
	prefixSet := t.envPrefix

	var out []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		uk := strings.ToUpper(key)
		if !exactSet[uk] {
			matched := false
			for _, p := range prefixSet {
				if strings.HasPrefix(uk, p) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if t.envBlockedExact != nil && t.envBlockedExact[uk] {
				continue
			}
			if t.containsBlockedKeyword(uk) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// containsBlockedKeyword screens variables admitted by a prefix rule. It is
// subtractive only: an exact env_allowlist entry is never dropped, so a build
// that genuinely needs FOO_TOKEN names it outright.
func (t *runCommandTool) containsBlockedKeyword(s string) bool {
	for _, kw := range t.envKeywordBlock {
		if kw != "" && strings.Contains(s, strings.ToUpper(kw)) {
			return true
		}
	}
	return false
}

// The environment allowlist is configuration-only. No variable names or
// prefixes are compiled in: which variables a child process may see is
// workspace policy. Recommended values ship in .mivia/mivia.toml.example under
// [tools].env_allowlist, where a trailing "*" declares a prefix rule
// ("GIT_*"). With it unset, child processes inherit no environment.
//
// [tools].env_allow_keyword_blocklist is the companion subtractive filter for
// prefix matches; it too has no compiled-in value.

// resolveEnvAllowlist computes the effective env var allowlist from the
// built-in defaults plus configurable overrides. Resolution order:
//
//	config.EnvAllowlist (or config.EnvAllowlistOnly)
//	  → config.EnvBlocklist (removed)
//
// Entries in cfgEnvAllow / cfgEnvAllowOnly ending in "*" are treated as
// prefix rules (e.g. "GIT_*" matches GIT_DIR, GIT_WORK_TREE, etc.).
func resolveEnvAllowlist(cfgEnvAllow, cfgEnvAllowOnly, cfgEnvBlock []string) (exactSet map[string]bool, prefixSet []string, blockedExact map[string]bool) {
	// With no compiled-in list there is nothing to extend or replace, so
	// env_allowlist_only and env_allowlist differ only in name; both are
	// honoured so existing configs keep working.
	var base []string
	if len(cfgEnvAllowOnly) > 0 {
		cfgEnvAllow = cfgEnvAllowOnly
	}

	// Separate wildcard (prefix) entries from exact entries.
	var extraPrefixes []string
	for _, v := range cfgEnvAllow {
		if strings.HasSuffix(v, "*") {
			p := strings.TrimSuffix(v, "*")
			extraPrefixes = append(extraPrefixes, p)
		} else {
			base = append(base, v)
		}
	}

	// Build blocklist set (uppercased).
	blocked := make(map[string]bool, len(cfgEnvBlock))
	for _, v := range cfgEnvBlock {
		blocked[strings.ToUpper(v)] = true
	}
	blockedPrefixes := make(map[string]bool)
	for _, v := range cfgEnvBlock {
		if strings.HasSuffix(v, "*") {
			blockedPrefixes[strings.ToUpper(strings.TrimSuffix(v, "*"))] = true
		}
	}

	// Blocked exact entries are returned separately so filterEnv can
	// reject prefix-matched variables that match a blocked exact name.
	// Without this, a GIT_* prefix rule would admit GIT_DIR even when
	// GIT_DIR is in env_blocklist (prefix matching has no awareness of
	// exact blocklist entries).
	blockedExact = make(map[string]bool)
	for _, v := range cfgEnvBlock {
		if !strings.HasSuffix(v, "*") {
			blockedExact[strings.ToUpper(v)] = true
		}
	}

	// Apply blocklist and build exact set.
	exactSet = make(map[string]bool, len(base))
	for _, v := range base {
		uk := strings.ToUpper(v)
		if blocked[uk] {
			continue
		}
		exactSet[uk] = true
	}

	// Build prefix set from the configured wildcard entries, minus blocklist.
	allPrefixes := extraPrefixes
	prefixSet = make([]string, 0, len(allPrefixes))
	for _, p := range allPrefixes {
		up := strings.ToUpper(p)
		if blocked[up] || blockedPrefixes[up] {
			continue
		}
		prefixSet = append(prefixSet, up)
	}

	return exactSet, prefixSet, blockedExact
}
