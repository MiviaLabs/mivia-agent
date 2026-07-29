package tools

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// resolveEnvAllowlist resolution order tests
// ---------------------------------------------------------------------------

// The env allowlist is configuration-only; these fixtures mirror the values
// shipped in .mivia/mivia.toml.example so these tests exercise a realistic
// configuration rather than a compiled-in default that no longer exists.
var testEnvAllowlistExact = []string{
	"PATH", "HOME", "USER", "USERNAME", "LOGNAME", "TMPDIR",
	"TMP", "TEMP", "SHELL", "TERM", "PWD", "OLDPWD",
	"HOSTNAME", "HOST", "LANG", "LANGUAGE", "EDITOR", "VISUAL",
	"MAKE", "MAKEFLAGS", "MAKELEVEL", "MFLAGS", "DISPLAY", "WAYLAND_DISPLAY",
	"XAUTHORITY", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR",
	"GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "NPM_CONFIG_USERCONFIG", "CARGO_HOME", "RUSTUP_HOME",
	"GOPATH", "GOROOT", "KUBECONFIG", "CC", "CXX", "CGO_ENABLED",
	"CGO_CFLAGS", "CGO_LDFLAGS", "GOFLAGS", "GOPRIVATE", "GONOSUMCHECK", "GOSUMDB",
	"GOEXPERIMENT", "RUST_BACKTRACE", "RUST_LOG", "PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "NODE_PATH",
	"CMAKE_GENERATOR", "MAKEOBJDIRPREFIX",
}

var testEnvPrefixes = []string{
	"LC_", "XDG_", "GIT_", "NODE_",
}

// testEnvKeywordBlock mirrors the example config's subtractive prefix filter.
var testEnvKeywordBlock = []string{"SECRET", "TOKEN", "PASSWORD", "API_KEY"}

// testEnvAllowlist is the config form: prefixes carry a trailing "*".
var testEnvAllowlist = func() []string {
	out := append([]string(nil), testEnvAllowlistExact...)
	for _, p := range testEnvPrefixes {
		out = append(out, p+"*")
	}
	return out
}()

func TestResolveEnvAllowlist_ConfiguredBase(t *testing.T) {
	exact, prefixes := resolveEnvAllowlist(testEnvAllowlist, nil, nil)

	// Default exact vars must be present.
	for _, v := range testEnvAllowlistExact {
		if !exact[strings.ToUpper(v)] {
			t.Errorf("default exact var %q missing from resolved set", v)
		}
	}
	// Default prefix vars must be present.
	for _, p := range testEnvPrefixes {
		found := false
		for _, rp := range prefixes {
			if rp == strings.ToUpper(p) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default prefix %q missing from resolved prefixes", p)
		}
	}
}

func TestResolveEnvAllowlist_EnvAllowlistOnlyIsSelfContained(t *testing.T) {
	only := []string{"MY_TOOL_HOME", "MY_CACHE_DIR"}
	exact, prefixes := resolveEnvAllowlist(nil, only, nil)

	// Defaults should be absent.
	for _, v := range testEnvAllowlistExact {
		if exact[strings.ToUpper(v)] {
			t.Errorf("default var %q present when EnvAllowlistOnly should replace defaults", v)
		}
	}
	// Custom vars should be present.
	for _, v := range only {
		if !exact[strings.ToUpper(v)] {
			t.Errorf("custom var %q missing from resolved set", v)
		}
	}
	// Default prefixes should be absent.
	if len(prefixes) > 0 {
		t.Errorf("expected no prefix rules when EnvAllowlistOnly replaces defaults, got %v", prefixes)
	}
}

func TestResolveEnvAllowlist_EnvAllowlistAppendsToConfiguredBase(t *testing.T) {
	extra := []string{"MY_CUSTOM_VAR", "FOO_BAR"}
	exact, prefixes := resolveEnvAllowlist(append(append([]string(nil), testEnvAllowlist...), extra...), nil, nil)

	// Defaults must still be present.
	for _, v := range testEnvAllowlistExact {
		if !exact[strings.ToUpper(v)] {
			t.Errorf("default var %q missing when EnvAllowlist appends", v)
		}
	}
	// Custom vars must be present.
	for _, v := range extra {
		if !exact[strings.ToUpper(v)] {
			t.Errorf("custom var %q missing when EnvAllowlist appends", v)
		}
	}
	// Prefixes unchanged.
	if len(prefixes) != len(testEnvPrefixes) {
		t.Errorf("prefix count changed: got %d, want %d", len(prefixes), len(testEnvPrefixes))
	}
}

func TestResolveEnvAllowlist_EnvBlocklistRemoves(t *testing.T) {
	block := []string{"HOME", "USER"}
	exact, _ := resolveEnvAllowlist(testEnvAllowlist, nil, block)

	// Blocked vars should be absent.
	for _, v := range block {
		if exact[strings.ToUpper(v)] {
			t.Errorf("blocked var %q still present", v)
		}
	}
	// Other defaults should still be present.
	if !exact["PATH"] {
		t.Errorf("PATH missing after blocking unrelated vars")
	}
}

func TestResolveEnvAllowlist_KeywordBlocklist(t *testing.T) {
	// Use EnvAllowlistOnly to set a known prefix so keyword filtering applies.
	only := []string{"GIT_*"}
	exact, prefixes := resolveEnvAllowlist(nil, only, nil)

	// GIT_ is a prefix — exact should be empty.
	if len(exact) > 0 {
		t.Errorf("expected empty exact set with only wildcard entries, got %v", exact)
	}
	// Prefix must contain GIT_.
	hasGit := false
	for _, p := range prefixes {
		if p == "GIT_" {
			hasGit = true
			break
		}
	}
	if !hasGit {
		t.Fatalf("GIT_ prefix missing from resolved prefixes: %v", prefixes)
	}

	// Simulate filterEnv behaviour with keyword blocklist on GIT_ prefix.
	env := []string{
		"GIT_DIR=/repo",
		"GIT_SSH_COMMAND=ssh -v",
		"GIT_TOKEN=abc123",
		"GIT_TOKEN_ABC=xyz",
		"GIT_PASSWORD=secret",
		"GIT_API_KEY=key",
	}
	exact, prefixes = resolveEnvAllowlist(nil, only, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}
	filtered := tool.filterEnv(env)

	// GIT_DIR and GIT_SSH_COMMAND should be allowed (no keyword in name).
	if !containsEnv(filtered, "GIT_DIR") {
		t.Errorf("GIT_DIR should be allowed but was filtered")
	}
	if !containsEnv(filtered, "GIT_SSH_COMMAND") {
		t.Errorf("GIT_SSH_COMMAND should be allowed but was filtered")
	}
	// GIT_TOKEN, GIT_TOKEN_ABC, GIT_PASSWORD, GIT_API_KEY should be blocked.
	if containsEnv(filtered, "GIT_TOKEN") {
		t.Errorf("GIT_TOKEN should be blocked by keyword TOKEN")
	}
	if containsEnv(filtered, "GIT_TOKEN_ABC") {
		t.Errorf("GIT_TOKEN_ABC should be blocked by keyword TOKEN")
	}
	if containsEnv(filtered, "GIT_PASSWORD") {
		t.Errorf("GIT_PASSWORD should be blocked by keyword PASSWORD")
	}
	if containsEnv(filtered, "GIT_API_KEY") {
		t.Errorf("GIT_API_KEY should be blocked by keyword API_KEY")
	}
}

func TestResolveEnvAllowlist_WildcardPrefixCustomEntries(t *testing.T) {
	allow := []string{"MYCUSTOM_*", "CI_*"}
	_, prefixes := resolveEnvAllowlist(append(append([]string(nil), testEnvAllowlist...), allow...), nil, nil) // exact set intentionally unused, only testing prefixes

	// Wildcard entries should become prefix rules.
	hasMycustom := false
	hasCi := false
	for _, p := range prefixes {
		if p == "MYCUSTOM_" {
			hasMycustom = true
		}
		if p == "CI_" {
			hasCi = true
		}
	}
	if !hasMycustom {
		t.Errorf("MYCUSTOM_ prefix missing from resolved prefixes: %v", prefixes)
	}
	if !hasCi {
		t.Errorf("CI_ prefix missing from resolved prefixes: %v", prefixes)
	}
	// Default prefixes should still be present.
	for _, dp := range testEnvPrefixes {
		found := false
		for _, p := range prefixes {
			if p == strings.ToUpper(dp) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default prefix %q missing when custom wildcards added", dp)
		}
	}
}

func TestResolveEnvAllowlist_WildcardBlocklist(t *testing.T) {
	// Block the entire GIT_ prefix using wildcard blocking.
	block := []string{"GIT_*"}
	exact, prefixes := resolveEnvAllowlist(testEnvAllowlist, nil, block)

	// GIT_ prefix should be removed from the prefix set.
	for _, p := range prefixes {
		if p == "GIT_" {
			t.Errorf("GIT_ prefix present despite wildcard block")
		}
	}

	// Exact entries from testEnvAllowlistExact that have the GIT_ prefix
	// are not affected by prefix wildcard blocking (exact matches survive).
	// Test with vars that would only match via the prefix set.

	// Create a custom env with vars that match only via GIT_ prefix
	// (not exact testEnvAllowlistExact matches).
	env := []string{
		"GIT_DIR=/repo",
		"GIT_WORK_TREE=/work",
		"PATH=/usr/bin",
	}
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}
	filtered := tool.filterEnv(env)

	// GIT_DIR and GIT_WORK_TREE should be blocked by wildcard blocklist
	// (they match GIT_ prefix but that prefix was removed).
	if containsEnv(filtered, "GIT_DIR") {
		t.Errorf("GIT_DIR should be blocked by wildcard blocklist")
	}
	if containsEnv(filtered, "GIT_WORK_TREE") {
		t.Errorf("GIT_WORK_TREE should be blocked by wildcard blocklist")
	}
	// PATH should still be present (exact entry, unrelated to GIT_).
	if !containsEnv(filtered, "PATH") {
		t.Errorf("PATH should still be present despite GIT_ block")
	}

	// GIT_PAGER is in testEnvAllowlistExact as an exact entry, so it
	// survives the prefix wildcard block.
	env2 := []string{"GIT_PAGER=less"}
	filtered2 := tool.filterEnv(env2)
	if !containsEnv(filtered2, "GIT_PAGER") {
		t.Errorf("GIT_PAGER is an exact testEnvAllowlistExact entry and should survive prefix wildcard block")
	}
}

// ---------------------------------------------------------------------------
// GIT_* / NODE_* prefix regression tests
// ---------------------------------------------------------------------------

func TestResolveEnvAllowlist_GitPrefix_AllowsKnownSafe(t *testing.T) {
	exact, prefixes := resolveEnvAllowlist(testEnvAllowlist, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}

	// GIT_DIR and GIT_SSH_COMMAND are known safe GIT_* vars that should be allowed.
	env := []string{
		"GIT_DIR=/repo/.git",
		"GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=no",
		"GIT_PAGER=less",
		"GIT_EDITOR=vim",
		"GIT_SEQUENCE_EDITOR=vim",
	}
	filtered := tool.filterEnv(env)
	for _, v := range []string{"GIT_DIR", "GIT_SSH_COMMAND", "GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR"} {
		if !containsEnv(filtered, v) {
			t.Errorf("%s should be allowed via GIT_ prefix", v)
		}
	}
}

func TestResolveEnvAllowlist_GitPrefix_BlocksTokenContaining(t *testing.T) {
	exact, prefixes := resolveEnvAllowlist(testEnvAllowlist, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}

	// GIT_TOKEN and GIT_TOKEN_ABC contain keyword "TOKEN" and should be blocked.
	env := []string{
		"GIT_TOKEN=ghp_abc123",
		"GIT_TOKEN_ABC=xyz",
	}
	filtered := tool.filterEnv(env)
	if containsEnv(filtered, "GIT_TOKEN") {
		t.Errorf("GIT_TOKEN should be blocked by keyword blocklist")
	}
	if containsEnv(filtered, "GIT_TOKEN_ABC") {
		t.Errorf("GIT_TOKEN_ABC should be blocked by keyword blocklist")
	}
}

func TestResolveEnvAllowlist_NodePrefix_AllowsKnownSafe(t *testing.T) {
	exact, prefixes := resolveEnvAllowlist(testEnvAllowlist, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}

	// NODE_ENV and NODE_DEBUG contain no keyword, so should be allowed via NODE_ prefix.
	env := []string{
		"NODE_ENV=production",
		"NODE_DEBUG=app:db",
	}
	filtered := tool.filterEnv(env)
	if !containsEnv(filtered, "NODE_ENV") {
		t.Errorf("NODE_ENV should be allowed via NODE_ prefix")
	}
	if !containsEnv(filtered, "NODE_DEBUG") {
		t.Errorf("NODE_DEBUG should be allowed via NODE_ prefix")
	}
}

func TestResolveEnvAllowlist_NodePrefix_AllowsOptionsAndSymlinks(t *testing.T) {
	exact, prefixes := resolveEnvAllowlist(testEnvAllowlist, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}

	// NODE_OPTIONS and NODE_PRESERVE_SYMLINKS contain no keyword (SECRET/TOKEN/PASSWORD/API_KEY),
	// so they should be allowed by the NODE_ prefix. (Note: the deprecated isAllowedEnvVar
	// blocked them, but resolveEnvAllowlist only blocks by keyword, not by hard-coded exceptions.)
	env := []string{
		"NODE_OPTIONS=--max-old-space-size=4096",
		"NODE_PRESERVE_SYMLINKS=1",
	}
	filtered := tool.filterEnv(env)
	if !containsEnv(filtered, "NODE_OPTIONS") {
		t.Errorf("NODE_OPTIONS should be allowed via NODE_ prefix (no keyword match)")
	}
	if !containsEnv(filtered, "NODE_PRESERVE_SYMLINKS") {
		t.Errorf("NODE_PRESERVE_SYMLINKS should be allowed via NODE_ prefix (no keyword match)")
	}
}

// containsEnv checks whether a filtered env slice contains a variable with the given key prefix.
func containsEnv(env []string, keyPrefix string) bool {
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if strings.EqualFold(k, keyPrefix) {
			return true
		}
	}
	return false
}

// Unconfigured means unfiltered here too: with no keyword blocklist a prefix
// rule admits every variable it matches, including secret-looking names.
func TestFilterEnv_NoKeywordBlocklistAdmitsPrefixMatches(t *testing.T) {
	exact, prefixes := resolveEnvAllowlist([]string{"GIT_*"}, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes}
	got := tool.filterEnv([]string{"GIT_TOKEN=abc"})
	if !containsEnv(got, "GIT_TOKEN") {
		t.Fatal("with no keyword blocklist configured, GIT_TOKEN must pass the GIT_ prefix rule")
	}
}
