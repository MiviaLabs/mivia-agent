package cli

import (
	"bytes"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
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
	if err := cliworkflow.RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, &stdout, io.Discard); err != nil {
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
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := ledger.NewStorageRepository(store)
	before := assertWorkflowAdmission(t, repo, runID)
	beforeRequests := requests.Load()
	if err := os.WriteFile(filepath.Join(root, ".agents", "agents", "one.md"), []byte("not valid toml = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	var resumed strings.Builder
	if err := cliworkflow.RunWorkflowWithIO([]string{"resume", runID, "--workspace", root, "--config", filepath.Join(root, "config.toml")}, &resumed, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != beforeRequests || !strings.Contains(resumed.String(), "status=succeeded") {
		after, _ := repo.ListStepAttempts(t.Context(), runID)
		fresh, freshErr := OpenContextStorePath(storePath)
		freshCount := -1
		if freshErr == nil {
			freshAttempts, _ := ledger.NewStorageRepository(fresh).ListStepAttempts(t.Context(), runID)
			freshCount = len(freshAttempts)
			_ = fresh.Close()
		}
		t.Fatalf("resume output=%q requests=%d before_requests=%d before=%+v attempts=%d fresh=%d", resumed.String(), requests.Load(), beforeRequests, before, len(after), freshCount)
	}
}

// TestWorkflowRunProgressJSONLinesOnStderr checks that a workflow run prints
// progress JSON lines to stderr while stdout keeps its two-field contract.
func TestWorkflowRunProgressJSONLinesOnStderr(t *testing.T) {
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
	var stderr bytes.Buffer
	if err := cliworkflow.RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("provider requests = %d, want 2", requests.Load())
	}
	fields := strings.Fields(stdout.String())
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "run_id=wfr-") || fields[1] != "status=succeeded" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"step_started"`) || !strings.Contains(stderr.String(), `"run_finished"`) {
		t.Fatalf("stderr progress lines missing step_started/run_finished: %q", stderr.String())
	}
}

func assertWorkflowAdmission(t *testing.T, repo ledger.Repository, runID string) ledger.RunSnapshot {
	t.Helper()
	attempts, err := repo.ListStepAttempts(t.Context(), runID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %d, %v; want 2", len(attempts), err)
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
	run, err := repo.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := repo.GetRunSnapshot(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if run.SnapshotDigest != ledger.SnapshotDigest(raw) || run.InputDigest != ledger.InputDigest(snapshot.Inputs) {
		t.Fatalf("run digests do not match the immutable snapshot: %+v", run)
	}
	corrupt := append(append([]byte(nil), raw...), ' ')
	if _, _, _, err := cliworkflow.ValidateWorkflowResumeSnapshot(run, corrupt); err == nil || !strings.Contains(err.Error(), "snapshot digest") {
		t.Fatalf("corrupt immutable snapshot error = %v", err)
	}
	for _, name := range []string{"one", "two"} {
		binding := snapshot.Agents[name]
		if binding.ProviderName != "openrouter" || binding.Model != "test/model" {
			t.Fatalf("agent %q binding = %q/%q", name, binding.ProviderName, binding.Model)
		}
	}
	return run
}

func TestWorkflowRunRejectsOpenSchemaBeforeProviderCall(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	openSchema := `{"type":"object","properties":{"ok":{"type":"boolean"}}}`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "schemas", "out.json"), []byte(openSchema), 0o600); err != nil {
		t.Fatal(err)
	}

	err := cliworkflow.RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "additionalProperties") {
		t.Fatalf("workflow run error = %v, want closed-schema rejection", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("provider requests = %d, want 0", requests.Load())
	}
}

// TestWorkflowRunAllowsRunCommandAuthority: a workflow agent step that carries
// run_command is admissible, and the workflow is treated as write-capable
// (a shell program can write anywhere) so it must run in an isolated worktree.
func TestWorkflowRunAllowsRunCommandAuthority(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	configPath := filepath.Join(root, "config.toml")
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n[tools]\nrun_allowlist = [\"echo\"]\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(root, ".agents", "agents", "one.md")
	body := "---\nname: one\ndescription: command agent\ntools: [run_command]\nmax_turns: 1\n---\n"
	if err := os.WriteFile(agentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, root)
	var stdout strings.Builder
	err = cliworkflow.RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", configPath, "--input", "task=compile"}, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runID := strings.TrimPrefix(strings.Fields(stdout.String())[0], "run_id=")
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	run, err := ledger.NewStorageRepository(store).GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.WorktreeName == "" {
		t.Fatal("run_command workflow must be write-capable and create a worktree")
	}
}

func TestWorkflowRunCreatesWorktreeForWriteAuthority(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(root, ".agents", "agents", name+".md")
		body := "---\nname: " + name + "\ndescription: writer\ntools: [write_file]\nmax_turns: 1\n---\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	initWorkflowGitRepo(t, root)
	var stdout strings.Builder
	err := cliworkflow.RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runID := strings.TrimPrefix(strings.Fields(stdout.String())[0], "run_id=")
	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	run, err := ledger.NewStorageRepository(store).GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.WorktreeName == "" || run.BaseCommit == "" || run.BaseRef == "" {
		t.Fatalf("workflow Git identity is incomplete: %+v", run)
	}
	worktree, err := vcs.Resolve(t.Context(), root, run.WorktreeName)
	if err != nil || worktree == nil {
		t.Fatalf("resolve workflow worktree = %+v, %v", worktree, err)
	}
}

func TestWorkflowRunLoadsSourceHooksAndExecutesToolsInWorktree(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call%2 == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"write","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"generated.txt\",\"content\":\"ok\"}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	setWorkflowAgentTools(t, root, "write_file")
	initWorkflowGitRepo(t, root)
	marker := filepath.Join(root, "hook-ran")
	var hookScript string
	if runtime.GOOS == "windows" {
		// A .cmd hook is what can actually execute on Windows; .sh scripts
		// have no argv fallback there.
		hookScript = filepath.Join(root, "hook.cmd")
		if err := os.WriteFile(hookScript, []byte("@echo off\r\nset /p \"=x\" <nul >> \""+marker+"\"\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		hookScript = filepath.Join(root, "hook.sh")
		if err := os.WriteFile(hookScript, []byte("#!/bin/sh\nprintf x >> \""+marker+"\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// argv paths go through ToSlash so backslashes in a Windows temp path
	// cannot escape the TOML double-quoted string.
	hookConfig := "[[hooks]]\nevent = \"PostToolUse\"\nmatcher = \"^write_file$\"\n[[hooks.handlers]]\ntype = \"command\"\nargv = [\"" + filepath.ToSlash(hookScript) + "\"]\n"
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(hookConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	err := cliworkflow.RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("source checkout contains generated file: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "xx" {
		t.Fatalf("hook marker = %q, %v; want xx", data, err)
	}
}
