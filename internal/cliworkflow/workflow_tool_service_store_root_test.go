package cliworkflow

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestWorkflowToolSubagentConfigKeepsSessionStorePathLiteral pins the
// non-mutation contract of workflowToolSubagentConfig: applying the workspace
// store-root default must clone the caller's session Resolved config, never
// write the anchored absolute path back into it. A shared live Resolved that
// gains an expanded store path leaks into unrelated settings persistence,
// which would durably replace a configured "~/..." literal (and reveal the
// username) in .mivia/mivia.toml.
func TestWorkflowToolSubagentConfigKeepsSessionStorePathLiteral(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	res := &config.Resolved{StorePathSet: true}
	res.Subagents.StorePath = "~/.mivia/x.db"

	got := workflowToolSubagentConfig(root, res)

	if res.Subagents.StorePath != "~/.mivia/x.db" {
		t.Fatalf("input res.Subagents.StorePath mutated: got %q, want %q",
			res.Subagents.StorePath, "~/.mivia/x.db")
	}
	wantAnchored := filepath.Join(home, ".mivia", "x.db")
	if got.StorePath != wantAnchored {
		t.Fatalf("returned StorePath = %q, want expanded home-anchored %q",
			got.StorePath, wantAnchored)
	}
}

// TestWorkflowToolSubagentConfigUnkeyedDefaultKeepsInput covers the unkeyed
// default branch: when [subagents].store_path was not set, the function still
// returns the workspace-rooted context store for its own wiring while the
// input Resolved stays exactly as the caller left it.
func TestWorkflowToolSubagentConfigUnkeyedDefaultKeepsInput(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	res := &config.Resolved{StorePathSet: false}
	res.Subagents.StorePath = "/shared/global/context.db"
	before := *res

	got := workflowToolSubagentConfig(root, res)

	if !reflect.DeepEqual(*res, before) {
		t.Fatalf("input res mutated:\n got %#v\nwant %#v", *res, before)
	}
	if want := workspace.ContextStorePath(root); got.StorePath != want {
		t.Fatalf("returned StorePath = %q, want workspace default %q",
			got.StorePath, want)
	}
}
