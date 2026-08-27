package uiadapter

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestMCPOriginLabelSourcesTheNamespace is the point of moving this label out
// of the settings screen: the namespace directory is named once, in
// internal/workspace, and every other layer derives it.
func TestMCPOriginLabelSourcesTheNamespace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scope      ports.Scope
		global     bool
		wantPrefix string
	}{
		{"project", ports.ScopeProject, false, "Project (workspace: "},
		{"user scope", ports.ScopeUser, false, "Global (user: ~/"},
		{"global flag", ports.ScopeProject, true, "Global (user: ~/"},
	} {
		got := mcpOriginLabel(tc.scope, tc.global)
		if !strings.HasPrefix(got, tc.wantPrefix) {
			t.Fatalf("%s: mcpOriginLabel = %q, want prefix %q", tc.name, got, tc.wantPrefix)
		}
		if !strings.Contains(got, workspace.Namespace+"/"+mcpConfigFile) {
			t.Fatalf("%s: mcpOriginLabel = %q, want it to name %s/%s", tc.name, got, workspace.Namespace, mcpConfigFile)
		}
	}
}

// TestMCPOriginLabelUsesForwardSlashes keeps the label platform-stable: it is
// display text, not a host path, so path.Join is correct and filepath.Join
// would render backslashes on Windows.
func TestMCPOriginLabelUsesForwardSlashes(t *testing.T) {
	if got := mcpOriginLabel(ports.ScopeProject, false); strings.Contains(got, `\`) {
		t.Fatalf("mcpOriginLabel = %q, want forward slashes only", got)
	}
}

// TestMCPOriginFallbackNamesNoPath: a screen-built view has no label, and the
// fallback must stay free of the namespace name so the single-source rule
// cannot be reintroduced through it.
func TestMCPOriginFallbackNamesNoPath(t *testing.T) {
	for _, got := range []string{
		ports.MCPOriginFallback(ports.ScopeProject, false),
		ports.MCPOriginFallback(ports.ScopeUser, false),
		ports.MCPOriginFallback(ports.ScopeProject, true),
	} {
		if got == "" {
			t.Fatal("fallback origin label is empty")
		}
		if strings.Contains(got, workspace.Namespace) {
			t.Fatalf("fallback origin label %q names the namespace directory", got)
		}
	}
}
