package cli

// Wave B reclassifies load_tools from ExecutionRead to ExecutionWrite.
//
// load_tools stages deferred-tool admission: it mutates the session's own
// tool surface, so the loop must schedule it as a write (serialized, and the
// per-turn dedup kept) rather than as a read (fresh, dedup-skipped). Staging
// the same names twice must not be silently collapsed into one logical call,
// and the mutation must never race a sibling surface change.
//
// The assertion lives HERE, not in internal/tools/capability_dedups_test.go,
// because load_tools is CLI-wired: internal/cli/dispatcher.go registers the
// privileged loadToolsTool on a session registry only when an agent binding
// defers tools, so it is absent from tools.NewDefaultRegistry, and its type is
// unexported in package cli (internal/tools cannot import internal/cli - the
// dependency is one-way). internal/tools/capability_dedups_test.go records
// this placement decision in TestLoadToolsNotInDefaultRegistry.

import (
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestLoadToolsDeclaredExecutionWrite pins the load_tools Capability class.
//
// RED on the current code: loadToolsTool.Capability returns ExecutionRead
// (the pre-Wave-B contract). Wave B flips it to ExecutionWrite.
func TestLoadToolsDeclaredExecutionWrite(t *testing.T) {
	tool := &loadToolsTool{}
	if got := tool.Capability(json.RawMessage(`{}`)).Class; got != tools.ExecutionWrite {
		t.Fatalf("load_tools Capability class = %v, want ExecutionWrite", got)
	}
}
