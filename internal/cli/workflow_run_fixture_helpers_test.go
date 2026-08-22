package cli

// writeWorkflowRunFixture writes a complete stacking workflow run fixture.
// Duplicated from internal/cliworkflow (workflow_run_integration_test.go).

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeWorkflowRunFixture(t *testing.T, root, providerURL, storePath string) {
	workflowRoot := filepath.Join(root, ".mivia", "workflows")
	for _, dir := range []string{
		filepath.Join(workflowRoot, "templates"),
		filepath.Join(workflowRoot, "schemas"),
		filepath.Join(root, ".mivia", "agents"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := `[provider]
name = "openrouter"

[providers.openrouter]
base_url = "` + providerURL + `"
api_key_env = "WORKFLOW_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]

[subagents]
max_workers = 1
default_timeout_seconds = 30
store_backend = "sqlite"
store_path = "` + tomlPathLiteral(storePath) + `"
`
	writeFile(filepath.Join(root, "config.toml"), config)
	for _, name := range []string{"one", "two"} {
		writeFile(filepath.Join(root, ".mivia", "agents", name+".toml"), `name = "`+name+`"
description = "workflow test agent"
tools = ["read_file"]
max_turns = 1
`)
	}
	writeFile(filepath.Join(workflowRoot, "templates", "one.md"), "Return the result for {{ inputs.task }}.")
	writeFile(filepath.Join(workflowRoot, "templates", "two.md"), "Return the result for {{ evidence.previous }}.")
	writeFile(filepath.Join(workflowRoot, "schemas", "out.json"), `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`)
	writeFile(filepath.Join(workflowRoot, "two-step.toml"), `version = 1
name = "two-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
template = "templates/one.md"
output_schema = "schemas/out.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }, { from = "delivery.failure", as = "delivery_hint", max_bytes = 8192, optional = true }]

[[steps]]
id = "two"
kind = "agent"
agent = "two"
template = "templates/two.md"
output_schema = "schemas/out.json"
context = [{ from = "steps.one.output", as = "previous", max_bytes = 100 }, { from = "delivery.failure", as = "delivery_hint", max_bytes = 8192, optional = true }]

[[transitions]]
from = "one"
to = "two"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "two"
to = "success"
[transitions.match]
status = "succeeded"
`)
	t.Setenv("WORKFLOW_TEST_KEY", "test-key")
}

func initWorkflowGitRepoWithOrigin(t *testing.T, root string) {
	t.Helper()
	initWorkflowGitRepo(t, root)
	origin := filepath.Join(root, "origin.git")
	for _, args := range [][]string{
		{"init", "--bare", origin},
		{"-C", root, "remote", "add", "origin", origin},
		{"-C", root, "push", "-u", "origin", "main"},
	} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func initWorkflowGitRepo(t *testing.T, root string) {
	t.Helper()
	commands := [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "fixture"}}
	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
