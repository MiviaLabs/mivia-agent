//go:build unix

package cliworkflow

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

const workflowKillRecoveryHelper = "MIVIA_WORKFLOW_KILL_RECOVERY_HELPER"

// TestWorkflowRunRecoversAfterKilledHarness runs the CLI in a child process,
// kills it during its first provider request, then resumes from the same
// SQLite ledger in the parent process.
func TestWorkflowRunRecoversAfterKilledHarness(t *testing.T) {
	if os.Getenv(workflowKillRecoveryHelper) == "1" {
		runWorkflowKillRecoveryHelper(t)
		return
	}
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var block atomic.Bool
	block.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if block.Load() {
			once.Do(func() { close(entered) })
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testBinary, "-test.run=^TestWorkflowRunRecoversAfterKilledHarness$")
	cmd.Env = replaceTestEnv(os.Environ(), map[string]string{
		workflowKillRecoveryHelper:  "1",
		"MIVIA_KILL_ROOT":           root,
		"MIVIA_KILL_CONFIG":         filepath.Join(root, "config.toml"),
		"MIVIA_ALLOW_INSECURE_HTTP": "1",
	})
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("child did not reach provider request: %s", output.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("killed child exited successfully: %s", output.String())
	}
	close(release)
	block.Store(false)

	store, err := OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := ledger.NewStorageRepository(store)
	runs, err := repo.ListRuns(t.Context())
	_ = store.Close()
	if err != nil || len(runs) != 1 {
		t.Fatalf("killed child runs = %+v, %v; want one admitted run", runs, err)
	}
	var stdout strings.Builder
	if err := RunWorkflowWithIO([]string{"resume", runs[0].RunID, "--force", "--workspace", root, "--config", filepath.Join(root, "config.toml")}, &stdout, io.Discard); err != nil {
		t.Fatalf("resume after kill: %v; child output: %s", err, output.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("resume output = %q, want succeeded", stdout.String())
	}
}

func runWorkflowKillRecoveryHelper(t *testing.T) {
	root, configPath := os.Getenv("MIVIA_KILL_ROOT"), os.Getenv("MIVIA_KILL_CONFIG")
	if err := RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", configPath, "--input", "task=kill"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}
