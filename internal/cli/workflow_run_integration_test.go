package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestWorkflowRunLinearTwoStepExitCriterion(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	var stdout strings.Builder
	if err := runWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("provider requests = %d, want 2", requests.Load())
	}
	fields := strings.Fields(stdout.String())
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "run_id=wfr-") || fields[1] != "status=succeeded" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	runID := strings.TrimPrefix(fields[0], "run_id=")
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := ledger.NewStorageRepository(store)
	attempts, err := repo.ListStepAttempts(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(attempts))
	}
	seenRuns := map[string]bool{}
	for _, attempt := range attempts {
		if attempt.CoordinatorRunID == "" || attempt.TaskID == "" {
			t.Fatalf("attempt lacks child identity: %+v", attempt)
		}
		seenRuns[attempt.CoordinatorRunID] = true
	}
	if len(seenRuns) != 2 {
		t.Fatalf("child run references = %d, want one per step", len(seenRuns))
	}
	before, err := repo.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRequests := requests.Load()
	var resumed strings.Builder
	if err := runWorkflowWithIO([]string{"resume", runID, "--workspace", root, "--config", filepath.Join(root, "config.toml")}, &resumed, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != beforeRequests || !strings.Contains(resumed.String(), "status=succeeded") {
		after, _ := repo.ListStepAttempts(t.Context(), runID)
		fresh, freshErr := openContextStorePath(storePath)
		freshCount := -1
		if freshErr == nil {
			freshAttempts, _ := ledger.NewStorageRepository(fresh).ListStepAttempts(t.Context(), runID)
			freshCount = len(freshAttempts)
			_ = fresh.Close()
		}
		t.Fatalf("resume output=%q requests=%d before_requests=%d before=%+v attempts=%d fresh=%d", resumed.String(), requests.Load(), beforeRequests, before, len(after), freshCount)
	}
}

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
store_path = "` + storePath + `"
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
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[steps]]
id = "two"
kind = "agent"
agent = "two"
template = "templates/two.md"
output_schema = "schemas/out.json"
context = [{ from = "steps.one.output", as = "previous", max_bytes = 100 }]

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
