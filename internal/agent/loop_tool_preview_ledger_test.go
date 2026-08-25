package agent

import "testing"

// A structured-JSON tool result must survive the operator preview cut
// whole: a truncation mid-string breaks the operator UI's JSON parse and
// forces a raw-envelope dump instead of a formatted preview
// (internal/ui/render.FormatLedgerOutput / FormatDispatchTasksOutput).
func TestRedactToolOutputForTool_StructuredJSONToolsGetEditTier(t *testing.T) {
	body := `{"status":"ok","ref":"ref:output:deadbeef","content":"` + string(make([]byte, 1000)) + `"}`
	for _, name := range []string{"ledger_read", "read_output", "dispatch_tasks"} {
		got := redactToolOutputForTool(name, body)
		if len(got) != len(body) {
			t.Errorf("%s: expected the full %d-byte body under the edit-tool preview tier, got %d bytes", name, len(body), len(got))
		}
	}
}

// An unlisted tool must keep the tighter default cap - this pins the
// existing behavior so a future addition to the edit tier is deliberate,
// not an accidental widening of every tool's preview budget.
func TestRedactToolOutputForTool_UnlistedToolKeepsDefaultTier(t *testing.T) {
	body := string(make([]byte, 1000))
	got := redactToolOutputForTool("grep", body)
	if len(got) != defaultToolPreviewMaxBytes {
		t.Errorf("grep: expected the %d-byte default cap, got %d bytes", defaultToolPreviewMaxBytes, len(got))
	}
}
