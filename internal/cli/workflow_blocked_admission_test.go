package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWorkflowTestWorkspace builds a workspace with a single-input workflow
// and (optionally) a configured write path blocklist.
func writeWorkflowTestWorkspace(t *testing.T, blocklist []string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	configBody := `[provider]
name = "openrouter"
[providers.openrouter]
base_url = "https://example.com"
api_key_env = "WORKFLOW_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
[subagents]
store_backend = "sqlite"
store_path = "` + tomlPathLiteral(filepath.Join(root, "store.db")) + `"
`
	if len(blocklist) > 0 {
		quoted := make([]string, 0, len(blocklist))
		for _, p := range blocklist {
			quoted = append(quoted, `"`+p+`"`)
		}
		configBody += "[tools]\nwrite_path_blocklist = [" + strings.Join(quoted, ", ") + "]\n"
	}
	if err := os.WriteFile(filepath.Join(root, "mivia.toml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	workflowBody := `version = 1
name = "demo"
initial_step = "one"
[inputs.task]
type = "string"
required = true
max_bytes = 16000
[[steps]]
id = "one"
kind = "agent"
agent = "worker"
[[transitions]]
from = "one"
to = "success"
match = { status = "succeeded" }
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "demo.toml"), []byte(workflowBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestPrepareWorkflowRunRefusesTaskDemandingBlocklistedEdit is the fail-fast
// admission guard: a fresh run whose task input instructs a write to a
// configured write-blocklisted path must be refused before any agent runs,
// instead of spinning through implement -> review -> blocked implement.
func TestPrepareWorkflowRunRefusesTaskDemandingBlocklistedEdit(t *testing.T) {
	root := writeWorkflowTestWorkspace(t, []string{".mivia/workflows"})
	_, err := prepareWorkflowRun("demo", root, filepath.Join(root, "mivia.toml"), []string{
		"task=edit .mivia/workflows/bug-fix.toml to lower max_bytes to 16000",
	})
	if err == nil {
		t.Fatal("prepareWorkflowRun() error = nil for a task demanding a blocked-path edit")
	}
	if !strings.Contains(err.Error(), "write-blocklisted") || !strings.Contains(err.Error(), ".mivia/workflows") {
		t.Fatalf("prepareWorkflowRun() error = %v, want a write-blocklisted diagnostic naming the path", err)
	}
}

// TestPrepareWorkflowRunRefusesTaskDemandingDefaultBlockedPath covers the
// built-in blocklist (.git, .mivia/mivia.toml) with no configured additions.
func TestPrepareWorkflowRunRefusesTaskDemandingDefaultBlockedPath(t *testing.T) {
	root := writeWorkflowTestWorkspace(t, nil)
	_, err := prepareWorkflowRun("demo", root, filepath.Join(root, "mivia.toml"), []string{
		"task=edit .mivia/mivia.toml and enable delivery",
	})
	if err == nil {
		t.Fatal("prepareWorkflowRun() error = nil for a task demanding a default-blocked-path edit")
	}
	if !strings.Contains(err.Error(), "write-blocklisted") {
		t.Fatalf("prepareWorkflowRun() error = %v, want a write-blocklisted diagnostic", err)
	}
}

// TestPrepareWorkflowRunAdmitsBlockedPathMention is the false-positive guard:
// a task that only asks to AUDIT or READ a blocklisted path must be admitted.
func TestPrepareWorkflowRunAdmitsBlockedPathMention(t *testing.T) {
	root := writeWorkflowTestWorkspace(t, []string{".mivia/workflows"})
	prepared, err := prepareWorkflowRun("demo", root, filepath.Join(root, "mivia.toml"), []string{
		"task=audit whether the .mivia/workflows definitions match the engine capabilities and report",
	})
	if err != nil {
		t.Fatalf("prepareWorkflowRun() error = %v, want admission for a read-only mention", err)
	}
	if prepared == nil {
		t.Fatal("prepareWorkflowRun() returned nil prepared run")
	}
}

// TestPrepareWorkflowRunAdmitsUnrelatedTask ensures ordinary tasks are
// untouched by the admission guard.
func TestPrepareWorkflowRunAdmitsUnrelatedTask(t *testing.T) {
	root := writeWorkflowTestWorkspace(t, []string{".mivia/workflows"})
	prepared, err := prepareWorkflowRun("demo", root, filepath.Join(root, "mivia.toml"), []string{
		"task=fix the bug in internal/cli/workflow_run.go",
	})
	if err != nil {
		t.Fatalf("prepareWorkflowRun() error = %v, want admission for an unrelated task", err)
	}
	if prepared == nil {
		t.Fatal("prepareWorkflowRun() returned nil prepared run")
	}
}
