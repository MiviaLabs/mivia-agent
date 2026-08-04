package tools

// Wave B execution-class pins for the SkipDedup contract.
//
// The runtime's per-turn dedup collapses two identical tool calls in one batch
// unless the caller opts out. Wave B makes the LOOP stamp SkipDedup on
// ExecutionRead-class calls (fresh execution, no dedup) while write-class
// calls keep the dedup. These tests pin the class side of that contract at the
// registry level: read-class tools declare ExecutionRead (so the loop may skip
// their dedup safely) and write-class tools declare ExecutionWrite (so the
// loop must keep dedup for them).

import (
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// readClassDefaultRegistryNames lists the read-class tools whose class the
// loop's SkipDedup stamping depends on. ledger_read and read_output are
// session-wired in internal/cli, not part of the default registry; they are
// checked only when the registry happens to contain them.
var readClassDefaultRegistryNames = []string{
	"read_file", "list_dir", "grep", "glob",
	"find_references", "go_to_definition", "list_symbols",
	"ledger_read", "read_output",
}

// TestReadClassToolsDeclareExecutionRead is the GREEN pin for Wave B: every
// read-class tool in the default registry must declare ExecutionRead, which is
// the class the loop is about to exempt from per-turn dedup. A tool that stops
// declaring ExecutionRead flips this test and forces the SkipDedup decision to
// be revisited for it.
func TestReadClassToolsDeclareExecutionRead(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	args := json.RawMessage(`{}`)
	for _, name := range readClassDefaultRegistryNames {
		if _, present := reg.Get(name); !present {
			// ledger_read and read_output are CLI-wired session tools; their
			// class is pinned at their wiring site, not here.
			t.Logf("tool %q is not in the default registry; skipped", name)
			continue
		}
		if got := reg.Capability(name, args).Class; got != ExecutionRead {
			t.Errorf("Capability(%q).Class = %v, want ExecutionRead", name, got)
		}
	}
}

// TestWriteClassToolsDeclareExecutionWrite is the complementary GREEN pin: the
// mutation tools must keep declaring ExecutionWrite, so the loop keeps their
// dedup (the second identical write call in one batch is answered as
// duplicate, never executed twice).
func TestWriteClassToolsDeclareExecutionWrite(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	args := json.RawMessage(`{"path":"x.txt","content":"hello"}`)
	for _, name := range []string{"write_file", "search_replace", MultiEditToolName} {
		if _, present := reg.Get(name); !present {
			t.Logf("tool %q is not in the default registry; skipped", name)
			continue
		}
		if got := reg.Capability(name, args).Class; got != ExecutionWrite {
			t.Errorf("Capability(%q).Class = %v, want ExecutionWrite", name, got)
		}
	}
}

// TestLoadToolsNotInDefaultRegistry records WHY the load_tools Wave B
// assertion does not live in this package. load_tools is a privileged
// session-control tool that internal/cli/dispatcher.go wires onto a session
// registry only when an agent binding defers tools; it is deliberately absent
// from NewDefaultRegistry, and its type lives in package cli, which this
// package cannot import (cli imports tools, never the reverse). The class
// assertion therefore lives in internal/cli/load_tools_tool_test.go
// (TestLoadToolsDeclaredExecutionWrite). If load_tools ever joins the default
// registry, that assertion must move here next to the registry.
func TestLoadToolsNotInDefaultRegistry(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	if _, present := reg.Get(LoadToolsToolName); present {
		t.Fatalf("load_tools is registered in the default registry; its Wave B class assertion must move here from internal/cli")
	}
}
