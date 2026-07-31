package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func writeAgentFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeUserConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := workspace.NamespacePath(home, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentSpec_NilVsEmpty(t *testing.T) {
	// Omitted tools → nil; tools = [] → non-nil empty; tools = ["x"] → populated.
	missing := []byte(`
name = "a"
description = "d"
`)
	spec, _, err := ParseAgentFileTOML(missing, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Tools != nil {
		t.Fatalf("omitted tools must be nil, got %#v", spec.Tools)
	}
	if spec.ToolsAdd != nil || spec.ToolsRemove != nil {
		t.Fatal("omitted deltas must be nil")
	}
	if spec.MaxTurns != nil {
		t.Fatal("omitted max_turns must be nil")
	}
	if spec.SystemPrompt != nil {
		t.Fatal("omitted system_prompt must be nil")
	}

	emptyTools := []byte(`
name = "a"
description = "d"
tools = []
`)
	spec, _, err = ParseAgentFileTOML(emptyTools, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Tools == nil {
		t.Fatal("tools = [] must be non-nil empty slice")
	}
	if len(*spec.Tools) != 0 {
		t.Fatalf("tools = [] length = %d", len(*spec.Tools))
	}

	// Empty system_prompt is an error (distinct from omitted).
	emptyPrompt := []byte(`
name = "a"
description = "d"
system_prompt = ""
`)
	if _, _, err := ParseAgentFileTOML(emptyPrompt, "a.toml"); err == nil {
		t.Fatal("system_prompt = \"\" must be an error")
	}
}

func TestAgentFilenameNameAgreement(t *testing.T) {
	body := []byte(`
name = "other"
description = "d"
`)
	_, _, err := ParseAgentFileTOML(body, "researcher.toml")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected name mismatch error, got %v", err)
	}
}

func TestAgentUnknownKeyRejected(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
mystery = true
`)
	_, _, err := ParseAgentFileTOML(body, "a.toml")
	if err == nil {
		t.Fatal("unknown key must be rejected")
	}
}

func TestAgentSkillsKeyParsed(t *testing.T) {
	// Plan 06: skills is a first-class allowlist field.
	body := []byte(`
name = "a"
description = "d"
skills = ["bug-audit"]
`)
	spec, _, err := ParseAgentFileTOML(body, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Skills == nil || len(*spec.Skills) != 1 || (*spec.Skills)[0] != "bug-audit" {
		t.Fatalf("skills = %#v", spec.Skills)
	}
}

func TestAgentSkillsEmptyListParsed(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
skills = []
`)
	spec, _, err := ParseAgentFileTOML(body, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Skills == nil {
		t.Fatal("skills = [] must be present as empty slice, not omitted")
	}
	if len(*spec.Skills) != 0 {
		t.Fatalf("skills = %v", *spec.Skills)
	}
}

func TestAgentSkillsOmittedIsNil(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
`)
	spec, _, err := ParseAgentFileTOML(body, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Skills != nil {
		t.Fatalf("omitted skills must be nil, got %#v", spec.Skills)
	}
}

func TestAgentMaxTurnsZeroMeansUnlimited(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
max_turns = 0
`)
	spec, _, err := ParseAgentFileTOML(body, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.MaxTurns == nil || *spec.MaxTurns != 0 {
		t.Fatalf("max_turns = 0 must parse as unlimited (0), got %#v", spec.MaxTurns)
	}
}

func TestAgentToolsAndToolsAddIsError(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
tools = ["read_file"]
tools_add = ["write_file"]
`)
	_, _, err := ParseAgentFileTOML(body, "a.toml")
	if err == nil {
		t.Fatal("tools + tools_add must be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "mutually exclusive") {
		t.Fatalf("error must say mutually exclusive: %s", msg)
	}
	if !strings.Contains(msg, "tools_add") || !strings.Contains(msg, "remove") {
		t.Fatalf("error must include remediation naming which field to remove: %s", msg)
	}
}

func TestWorkspaceAgentsAlwaysLoad(t *testing.T) {
	// Project agent files under <ws>/.mivia/agents/ always load (they replace
	// the former agent-prompt.md surface). loadWorkspace is ignored.
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	writeAgentFile(t, UserAgentsDir(), "useragent.toml", `
name = "useragent"
description = "user"
`)
	writeAgentFile(t, WorkspaceAgentsDir(ws), "wsagent.toml", `
name = "wsagent"
description = "ws"
`)

	for _, gate := range []bool{false, true} {
		files, _, err := DiscoverAgentFiles(ws, gate)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 2 {
			t.Fatalf("loadWorkspace=%v: got %d files, want both user and workspace", gate, len(files))
		}
		by := map[string]AgentSource{}
		for _, f := range files {
			by[f.Name] = f.Source
		}
		if by["useragent"] != AgentSourceUser || by["wsagent"] != AgentSourceWorkspace {
			t.Fatalf("loadWorkspace=%v: sources = %+v", gate, by)
		}
	}
}

func TestWorkspaceGlobalSettingsIgnored(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	writeUserConfig(t, home, `
[agents]
load_workspace_config = false
`)
	wsConfig := workspace.NamespacePath(ws, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsConfig, []byte(`
[agents]
load_workspace_config = true

[agents.guardrails]
require_explicit_tools = true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	g, err := LoadAgentsGlobal(ws)
	if err != nil {
		t.Fatal(err)
	}
	if g.LoadWorkspaceConfig {
		t.Fatal("workspace must not authorize the gate")
	}
	if g.RequireExplicitTools {
		t.Fatal("workspace guardrails must not apply")
	}
	if len(g.Warnings) == 0 {
		t.Fatal("expected warning that workspace [agents] was ignored")
	}
	joined := strings.Join(g.Warnings, " ")
	if !strings.Contains(joined, "ignoring workspace [agents]") {
		t.Fatalf("warning text = %q", joined)
	}
}

func TestWorkspaceAgentsRefusedWhenWorkspaceIsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write agents under the shared namespace (home == workspace).
	writeAgentFile(t, UserAgentsDir(), "only.toml", `
name = "only"
description = "shared"
`)
	// Discover with loadWorkspace true; same dir must still be user-only.
	files, warnings, err := DiscoverAgentFiles(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Source != AgentSourceUser {
		t.Fatalf("source = %s, want user (never re-interpret as workspace)", files[0].Source)
	}
	_ = warnings
}

func TestGateKeepsUserMeaningWhenWorkspaceIsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeUserConfig(t, home, `
[agents]
load_workspace_config = true

[agents.guardrails]
require_explicit_tools = true
fail_on_empty_toolset = false
mandatory_tool_denylist = ["run_command"]
`)

	// Workspace root is home — the single file is user config only.
	g, err := LoadAgentsGlobal(home)
	if err != nil {
		t.Fatal(err)
	}
	if !g.LoadWorkspaceConfig {
		t.Fatal("user gate value must survive when workspace is home")
	}
	if !g.RequireExplicitTools {
		t.Fatal("user require_explicit_tools must survive")
	}
	if g.FailOnEmptyToolset {
		t.Fatal("user fail_on_empty_toolset=false must survive")
	}
	if len(g.MandatoryToolDenylistAdditions) != 1 || g.MandatoryToolDenylistAdditions[0] != "run_command" {
		t.Fatalf("denylist additions = %#v", g.MandatoryToolDenylistAdditions)
	}
	// No "ignoring workspace" warning: there is no separate workspace file.
	for _, w := range g.Warnings {
		if strings.Contains(w, "ignoring workspace") {
			t.Fatalf("should not warn about ignoring the trusted file: %s", w)
		}
	}
}

func TestAgentSymlinkFileRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := UserAgentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "secret.toml")
	if err := os.WriteFile(target, []byte(`name = "leak"
description = "x"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "leak.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := DiscoverAgentFiles(t.TempDir(), false)
	if err == nil {
		t.Fatal("symlinked agent file must be refused")
	}
}

func TestAgentSymlinkDirRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	realDir := t.TempDir()
	writeAgentFile(t, realDir, "a.toml", `
name = "a"
description = "d"
`)
	// Point ~/.mivia/agents at a foreign directory via symlink.
	ns := workspace.NamespacePath(home)
	if err := os.MkdirAll(ns, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(ns, "agents")); err != nil {
		t.Fatal(err)
	}
	_, _, err := DiscoverAgentFiles(t.TempDir(), false)
	if err == nil {
		t.Fatal("symlinked agents directory must be refused")
	}
}

func TestAgentHardlinkRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := UserAgentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`
name = "a"
description = "d"
`)
	path := filepath.Join(dir, "a.toml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "a-hard.toml")
	if err := os.Link(path, hard); err != nil {
		t.Skipf("hardlink not supported: %v", err)
	}
	// Both names share Nlink>=2; discovery must refuse.
	_, _, err := DiscoverAgentFiles(t.TempDir(), false)
	if err == nil {
		t.Fatal("hardlinked agent file must be refused")
	}
}

func TestAgentOneFileParseRoundTrip(t *testing.T) {
	body := []byte(`
name = "researcher"
description = "Use for codebase exploration."
tools = ["read_file", "grep"]
disallowed_tools = ["run_command"]
model = "glm-4.5-air"
max_turns = 12
system_prompt = """
You are a read-only research agent.
"""
`)
	spec, name, err := ParseAgentFileTOML(body, "researcher.toml")
	if err != nil {
		t.Fatal(err)
	}
	if name != "researcher" {
		t.Fatalf("name = %q", name)
	}
	if spec.Tools == nil || len(*spec.Tools) != 2 {
		t.Fatalf("tools = %#v", spec.Tools)
	}
	if spec.MaxTurns == nil || *spec.MaxTurns != 12 {
		t.Fatalf("max_turns = %#v", spec.MaxTurns)
	}
	if spec.SystemPrompt == nil || !strings.Contains(*spec.SystemPrompt, "read-only") {
		t.Fatalf("system_prompt = %#v", spec.SystemPrompt)
	}
}

func TestLoadAgentsGlobalDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	g, err := LoadAgentsGlobal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if g.LoadWorkspaceConfig {
		t.Fatal("default gate must be false (M3)")
	}
	if !g.FailOnEmptyToolset {
		t.Fatal("default fail_on_empty_toolset must be true")
	}
}

func TestWorkspaceAgentCannotShadowUserAgentAtDiscovery(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	writeAgentFile(t, UserAgentsDir(), "researcher.toml", `
name = "researcher"
description = "user"
tools = ["read_file"]
`)
	writeAgentFile(t, WorkspaceAgentsDir(ws), "researcher.toml", `
name = "researcher"
description = "workspace tries to widen"
tools = ["read_file", "run_command"]
`)
	files, warnings, err := DiscoverAgentFiles(ws, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d, want 1", len(files))
	}
	if files[0].Source != AgentSourceUser {
		t.Fatal("user agent must win")
	}
	if files[0].Spec.Tools == nil || len(*files[0].Spec.Tools) != 1 {
		t.Fatalf("user tools preserved: %#v", files[0].Spec.Tools)
	}
	if len(warnings) == 0 {
		t.Fatal("expected shadow warning with both paths")
	}
	if !strings.Contains(warnings[0], "user") || !strings.Contains(warnings[0], "workspace") {
		t.Fatalf("warning = %q", warnings[0])
	}
}
