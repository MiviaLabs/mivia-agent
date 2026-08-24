package cliworkflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// workflowDefaultProtectedPaths are concrete files under the built-in write
// protections for workflow agent steps (DefaultWritePathBlocklist in
// internal/config). ".git/config" is NOT a separate default: it is a file
// inside the ".git" default directory, blocked by prefix match.
func workflowDefaultProtectedPaths() []string {
	return []string{".mivia/mivia.toml", ".git/config"}
}

// blockedPaths exercises every write tool against the given paths and asserts
// each one is refused with the protected-path error.
func blockedPaths(t *testing.T, registry interface {
	Execute(context.Context, string, json.RawMessage) (string, error)
}, root string, paths []string) {
	t.Helper()
	for _, path := range paths {
		abs := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"write_file", map[string]any{"path": path, "content": "new"}},
			{"search_replace", map[string]any{"path": path, "old_string": "old", "new_string": "new"}},
			{"multi_edit", map[string]any{"path": path, "edits": []map[string]any{{"old_string": "old", "new_string": "new"}}}},
			{"delete_file", map[string]any{"path": path}},
		} {
			t.Run(path+"/"+tc.tool, func(t *testing.T) {
				args, err := json.Marshal(tc.args)
				if err != nil {
					t.Fatal(err)
				}
				_, err = registry.Execute(context.Background(), tc.tool, args)
				if err == nil || !strings.Contains(err.Error(), "protected path") {
					t.Fatalf("Execute(%s) error = %v, want protected path error", tc.tool, err)
				}
			})
		}
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "old" {
			t.Fatalf("%s = %q, want old", path, got)
		}
	}
}

// writablePath asserts write_file succeeds on the given path.
func writablePath(t *testing.T, registry interface {
	Execute(context.Context, string, json.RawMessage) (string, error)
}, root string, path string) {
	t.Helper()
	abs := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{"path": path, "content": "new"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "write_file", args); err != nil {
		t.Fatalf("write_file(%q) error = %v, want success", path, err)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("%s = %q, want new", path, got)
	}
}

func TestWorkflowRegistryBlocksDefaultProtectedWrites(t *testing.T) {
	root := t.TempDir()
	registry, err := workflowDefaultRegistry(root, &config.Resolved{})
	if err != nil {
		t.Fatal(err)
	}
	blockedPaths(t, registry, root, workflowDefaultProtectedPaths())
	// Normalized inputs that clean into a default-protected path stay blocked.
	for _, path := range []string{
		filepath.Join(root, ".mivia", "mivia.toml"),
		".mivia/agents/../mivia.toml",
		"sub/../.git/config",
	} {
		t.Run("normalized/"+path, func(t *testing.T) {
			_, err := registry.Execute(context.Background(), "write_file", json.RawMessage(`{"path":`+workflowQuoteJSON(t, path)+`,"content":"new"}`))
			if err == nil || !strings.Contains(err.Error(), "protected path") {
				t.Fatalf("write_file(%q) error = %v, want protected path error", path, err)
			}
		})
	}
}

func TestWorkflowRegistryHonorsConfiguredWritePathBlocklist(t *testing.T) {
	root := t.TempDir()
	res := &config.Resolved{Tools: config.ToolsConfig{WritePathBlocklist: []string{
		".mivia/workflows/feature-delivery.toml", "go.mod", ".mivia/agents", ".mivia/policy",
	}}}
	registry, err := workflowDefaultRegistry(root, res)
	if err != nil {
		t.Fatal(err)
	}
	// Configured entries are blocked.
	blockedPaths(t, registry, root, []string{
		".mivia/workflows/feature-delivery.toml",
		"go.mod",
		".mivia/agents/worker.toml",
		".mivia/policy/commit-message.json",
	})
	// The built-in defaults stay blocked even with additions; ".git/config"
	// exercises the directory-prefix match against the ".git" default.
	blockedPaths(t, registry, root, workflowDefaultProtectedPaths())
	// An input that cleans into a configured entry is blocked.
	for _, path := range []string{
		".mivia/x/../workflows/feature-delivery.toml",
		"sub/../go.mod",
	} {
		t.Run("normalized/"+path, func(t *testing.T) {
			_, err := registry.Execute(context.Background(), "write_file", json.RawMessage(`{"path":`+workflowQuoteJSON(t, path)+`,"content":"new"}`))
			if err == nil || !strings.Contains(err.Error(), "protected path") {
				t.Fatalf("write_file(%q) error = %v, want protected path error", path, err)
			}
		})
	}
	// Paths outside the effective blocklist remain writable: the default set
	// is only .git and .mivia/mivia.toml, and a project controls the rest.
	writablePath(t, registry, root, ".mivia/workflows/other.toml")
	writablePath(t, registry, root, "internal/foo.go")
	writablePath(t, registry, root, "go.work")
}

func TestEffectiveWorkflowWriteDenylistRemovals(t *testing.T) {
	res := &config.Resolved{Tools: config.ToolsConfig{
		WritePathBlocklist:       []string{".mivia/workflows", "go.mod"},
		WritePathBlocklistRemove: []string{".git", ".mivia/mivia.toml"},
	}}
	got := effectiveWorkflowWriteDenylist(res)
	if slices.Contains(got, ".git") || slices.Contains(got, ".mivia/mivia.toml") {
		t.Fatalf("denylist = %v, want the defaults removed by explicit opt-out", got)
	}
	if !slices.Contains(got, ".mivia/workflows") || !slices.Contains(got, "go.mod") {
		t.Fatalf("denylist = %v, want the additions kept", got)
	}
}

// TestWorkflowRegistryHonorsWritePathBlocklistRemove covers the explicit
// opt-out end to end: write_path_blocklist_remove unblocks a default entry
// (.git, .mivia/mivia.toml) and a project addition for workflow agent steps,
// while a non-removed addition stays blocked.
func TestWorkflowRegistryHonorsWritePathBlocklistRemove(t *testing.T) {
	root := t.TempDir()
	res := &config.Resolved{Tools: config.ToolsConfig{
		WritePathBlocklist:       []string{".mivia/workflows/feature-delivery.toml", "go.mod"},
		WritePathBlocklistRemove: []string{".git", ".mivia/mivia.toml", ".mivia/workflows/feature-delivery.toml"},
	}}
	registry, err := workflowDefaultRegistry(root, res)
	if err != nil {
		t.Fatal(err)
	}
	// Removed entries are writable again.
	writablePath(t, registry, root, ".git/config")
	writablePath(t, registry, root, ".mivia/mivia.toml")
	writablePath(t, registry, root, ".mivia/workflows/feature-delivery.toml")
	// A non-removed addition stays blocked.
	blockedPaths(t, registry, root, []string{"go.mod"})
}

func TestWorkflowRegistryAllowsWorkspaceWrites(t *testing.T) {
	root := t.TempDir()
	registry, err := workflowDefaultRegistry(root, &config.Resolved{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"output.txt","content":"old"}`)); err != nil {
		t.Fatalf("write_file() error = %v", err)
	}
	if _, err := registry.Execute(context.Background(), "search_replace", json.RawMessage(`{"path":"output.txt","old_string":"old","new_string":"new"}`)); err != nil {
		t.Fatalf("search_replace() error = %v", err)
	}
	if _, err := registry.Execute(context.Background(), "multi_edit", json.RawMessage(`{"path":"output.txt","edits":[{"old_string":"new","new_string":"done"}]}`)); err != nil {
		t.Fatalf("multi_edit() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "done" {
		t.Fatalf("output.txt = %q, want done", got)
	}
}

// TestEffectiveWorkflowPanelLimitsDefaultsUnconfigured proves a nil res
// and an unset [workflows.panels] table both reproduce
// controller.DefaultPanelLimits() exactly - no [workflows.panels] key
// changes today's behavior.
func TestEffectiveWorkflowPanelLimitsDefaultsUnconfigured(t *testing.T) {
	want := controller.DefaultPanelLimits()
	if got := effectiveWorkflowPanelLimits(nil); got != want {
		t.Fatalf("nil res: PanelLimits = %+v, want default %+v", got, want)
	}
	if got := effectiveWorkflowPanelLimits(&config.Resolved{}); got != want {
		t.Fatalf("unset [workflows.panels]: PanelLimits = %+v, want default %+v", got, want)
	}
}

// TestEffectiveWorkflowPanelLimitsAppliesOverrides proves each
// [workflows.panels] key overrides exactly its matching PanelLimits
// field, leaving the rest at the compiled default.
func TestEffectiveWorkflowPanelLimitsAppliesOverrides(t *testing.T) {
	memberOut, memberCalls, synthOut, synthCalls, deadlineSec := 4096, 8, 2048, 4, 3600
	res := &config.Resolved{Workflows: config.WorkflowsConfig{Panels: config.WorkflowPanelLimits{
		MemberMaxOutputPerCall:       &memberOut,
		MemberMaxToolCalls:           &memberCalls,
		SynthesisMaxOutputPerCall:    &synthOut,
		SynthesisMaxToolCalls:        &synthCalls,
		MemberDeadlineDefaultSeconds: &deadlineSec,
	}}}
	want := controller.PanelLimits{
		MemberMaxOutputPerCall:    4096,
		MemberMaxToolCalls:        8,
		SynthesisMaxOutputPerCall: 2048,
		SynthesisMaxToolCalls:     4,
		MemberDeadlineDefault:     time.Hour,
	}
	if got := effectiveWorkflowPanelLimits(res); got != want {
		t.Fatalf("PanelLimits = %+v, want %+v", got, want)
	}
}

func workflowQuoteJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
