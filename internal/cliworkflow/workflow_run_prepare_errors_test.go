package cliworkflow

// workflow_run_prepare_errors_test.go covers PrepareWorkflowRun's default and
// refusal branches: the empty-root default, and every admission failure that
// must release the store it just opened.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestPrepareWorkflowRunDefaultsEmptyRootToWorkingDirectory pins the fallback
// for an unset --root: the workspace is the process working directory, so a
// workflow that lives there is discovered. An empty root passed through would
// resolve against nothing and report the workflow as missing.
func TestPrepareWorkflowRunDefaultsEmptyRootToWorkingDirectory(t *testing.T) {
	root := writeWorkflowTestWorkspace(t, nil)
	configPath := filepath.Join(root, "mivia.toml")
	t.Chdir(root)

	prepared, err := PrepareWorkflowRun("demo", "", configPath, []string{"task=do the thing"})
	if err != nil {
		t.Fatalf("PrepareWorkflowRun with an empty root = %v, want the cwd workspace", err)
	}
	t.Cleanup(prepared.CloseFn)
	if prepared.Compiled == nil || prepared.Compiled.Name != "demo" {
		t.Fatalf("compiled workflow = %+v, want demo", prepared.Compiled)
	}
	// The resolved root must be the working directory, not "".
	if prepared.Root == "" {
		t.Fatal("prepared.Root is empty; the root default did not resolve")
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gotRoot, err := filepath.EvalSymlinks(prepared.Root)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("prepared.Root = %q, want the working directory %q", gotRoot, wantRoot)
	}
}

// TestPrepareWorkflowRunSurfacesStoreOpenFailure pins that a store that cannot
// be opened aborts admission with the store's own error, rather than admitting
// a run with no durable ledger behind it.
func TestPrepareWorkflowRunSurfacesStoreOpenFailure(t *testing.T) {
	root := writeWorkflowTestWorkspace(t, nil)
	boom := errors.New("store is locked")
	prev := OpenContextStoreFunc
	t.Cleanup(func() { OpenContextStoreFunc = prev })
	OpenContextStoreFunc = func(string, config.SubagentConfig) (*storage.SQLite, error) { return nil, boom }

	prepared, err := PrepareWorkflowRun("demo", root, filepath.Join(root, "mivia.toml"), []string{"task=x"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store open failure", err)
	}
	if prepared != nil {
		t.Fatalf("prepared = %+v, want nil on the failure path", prepared)
	}
}

// TestPrepareWorkflowRunReleasesStoreOnAdmissionFailure pins the release
// contract every post-open refusal shares: discovery, compile, and input
// failures must all close the store they opened. A leaked handle keeps the
// workflow ledger locked against the next run.
// prepareWorkflowRunAdmissionFailureCase is one table row for
// TestPrepareWorkflowRunReleasesStoreOnAdmissionFailure, split out so the
// test function itself stays under the function-length cap.
type prepareWorkflowRunAdmissionFailureCase struct {
	name    string
	mutate  func(t *testing.T, root string)
	inputs  []string
	wantErr string
}

func prepareWorkflowRunAdmissionFailureCases() []prepareWorkflowRunAdmissionFailureCase {
	return []prepareWorkflowRunAdmissionFailureCase{
		{
			name: "workflows directory is unreadable",
			mutate: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".mivia", "workflows")
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				// A regular file where the directory belongs is a discovery
				// error, not an empty discovery.
				if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			inputs:  []string{"task=x"},
			wantErr: "workflows directory",
		},
		{
			name: "workflow does not compile",
			mutate: func(t *testing.T, root string) {
				body := `version = 1
name = "demo"
initial_step = "missing-step"
[inputs.task]
type = "string"
required = true
max_bytes = 16000
[[steps]]
id = "one"
kind = "agent"
agent = "worker"
`
				path := filepath.Join(root, ".mivia", "workflows", "demo.toml")
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			inputs:  []string{"task=x"},
			wantErr: "missing-step",
		},
		{
			name:    "required input is missing",
			mutate:  func(*testing.T, string) {},
			inputs:  nil,
			wantErr: `required workflow input "task" is missing`,
		},
		{
			name:    "input names an undeclared key",
			mutate:  func(*testing.T, string) {},
			inputs:  []string{"nosuch=x"},
			wantErr: `unknown workflow input "nosuch"`,
		},
	}
}

func TestPrepareWorkflowRunReleasesStoreOnAdmissionFailure(t *testing.T) {
	for _, tc := range prepareWorkflowRunAdmissionFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			runPrepareWorkflowRunAdmissionFailureCase(t, tc)
		})
	}
}

// runPrepareWorkflowRunAdmissionFailureCase drives one table row: it applies
// the fixture mutation, runs the refusal, and asserts the store handle
// PrepareWorkflowRun was given was closed on the failure path.
func runPrepareWorkflowRunAdmissionFailureCase(t *testing.T, tc prepareWorkflowRunAdmissionFailureCase) {
	t.Helper()
	root := writeWorkflowTestWorkspace(t, nil)
	tc.mutate(t, root)

	var opened int
	var handed *storage.SQLite
	prev := OpenContextStoreFunc
	t.Cleanup(func() { OpenContextStoreFunc = prev })
	OpenContextStoreFunc = func(r string, cfg config.SubagentConfig) (*storage.SQLite, error) {
		store, err := prev(r, cfg)
		if err == nil {
			opened++
			handed = store
		}
		return store, err
	}
	prepared, err := PrepareWorkflowRun("demo", root, filepath.Join(root, "mivia.toml"), tc.inputs)
	if err == nil {
		t.Fatalf("PrepareWorkflowRun accepted %s", tc.name)
	}
	if !strings.Contains(err.Error(), tc.wantErr) {
		t.Fatalf("err = %q, want it to contain %q", err.Error(), tc.wantErr)
	}
	if prepared != nil {
		t.Fatalf("prepared = %+v, want nil on the failure path", prepared)
	}
	if opened != 1 {
		t.Fatalf("store opened %d times, want 1 (the fixture never reached the store)", opened)
	}
	// The handle PrepareWorkflowRun was given must be closed: a live
	// handle still answers Count, a closed one refuses.
	if handed == nil {
		t.Fatal("the store seam was never asked for a handle")
	}
	if _, err := handed.Count(context.Background()); err == nil {
		t.Fatal("the store handle is still open after the refusal; closeFn did not run")
	}
}
