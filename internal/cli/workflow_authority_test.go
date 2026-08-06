package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestWorkflowRegistryBlocksProtectedWrites(t *testing.T) {
	root := t.TempDir()
	for _, path := range workflowProtectedPaths() {
		abs := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := workflowDefaultRegistry(root, &config.Resolved{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range workflowProtectedPaths() {
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"write_file", map[string]any{"path": path, "content": "new"}},
			{"search_replace", map[string]any{"path": path, "old_string": "old", "new_string": "new"}},
			{"multi_edit", map[string]any{"path": path, "edits": []map[string]any{{"old_string": "old", "new_string": "new"}}}},
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
	for _, path := range []string{
		filepath.Join(root, ".mivia", "mivia.toml"),
		".mivia/agents/../agents/worker.toml",
	} {
		t.Run("normalized/"+path, func(t *testing.T) {
			_, err := registry.Execute(context.Background(), "write_file", json.RawMessage(`{"path":`+workflowQuoteJSON(t, path)+`,"content":"new"}`))
			if err == nil || !strings.Contains(err.Error(), "protected path") {
				t.Fatalf("write_file(%q) error = %v, want protected path error", path, err)
			}
		})
	}
}

func workflowProtectedPaths() []string {
	return []string{
		".mivia/mivia.toml", ".mivia/agents/worker.toml", ".mivia/policy/tooling.toml",
		".mivia/rules/workflow.md", ".mivia/skills/review/SKILL.md",
		".mivia/workflows/feature-delivery.toml", ".mivia/workflows/templates/repair.md",
		".mivia/workflows/schemas/verification-v1.json", ".git/config", "go.mod", "go.sum", "go.work",
	}
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

func workflowQuoteJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
