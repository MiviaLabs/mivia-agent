package cliorchestrate

// AR-1: the per-task result envelope is the wire contract between this
// package (producers dispatchTaskResult / modelTaskResult) and uiadapter
// (consumer, encodedTaskResult). INV-TUI-29 forbids the import, so golden
// fixtures connect the two type sets:
//
//   - testdata/task_result_envelope_contract.json pins the CURRENT shape:
//     the final report plus references (tool_calls_ref), with no inline
//     tool_calls array. Produced here through the real encodeResults path;
//     uiadapter's consumer test decodes the same bytes.
//   - testdata/tool_calls_contract.json is the FROZEN legacy inline
//     tool_calls shape that old persisted sessions still carry. No producer
//     emits it anymore; only uiadapter's legacy-decode test reads it.
//
// If a tag name, tag option, or field type drifts on either side, one of
// the two package's tests fails.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// envelopeContractFixturePath is relative to this package directory: go test
// sets the working directory to the package under test.
const envelopeContractFixturePath = "testdata/task_result_envelope_contract.json"

// canonicalEnvelope drives the REAL production path (encodeResults) with
// fixed inputs, so the pinned bytes belong to the actual encoder pipeline,
// not to a hand mirror of it. Every input is constant, so the produced
// bytes (including the content-addressed tool_calls_ref digest) are stable.
func canonicalEnvelope(t *testing.T) string {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	steps := []subagents.ToolCallStep{
		{ToolCallID: "call_grep_01", Name: "grep", Kind: "start", Input: `{"pattern":"func load"}`},
		{ToolCallID: "call_grep_01", Name: "grep", Kind: "end", Output: "3 matches in 2 files"},
	}
	ref := storeToolCallSteps(t, repo, steps)
	tasks := []ledger.TaskSnapshot{{
		TaskID: "task-contract", Status: "completed",
		AgentName: "researcher", ToolCallsRef: ref,
	}}
	results := []subagents.Result{{
		TaskID: "task-contract", Status: "completed",
		Output: json.RawMessage(`"contract pinned output"`),
	}}
	tool := &dispatchTasksTool{repo: repo, cfg: config.SubagentConfig{InlineOutputBytes: 4096}}
	return tool.encodeResults(tasks, results)
}

// TestTaskResultEnvelopeWireContractGoldenBytes pins the encoder output to
// the committed golden fixture byte for byte. On mismatch the failure prints
// the full produced line; regeneration is copying that printed line into the
// fixture file (same compact format, no trailing newline).
func TestTaskResultEnvelopeWireContractGoldenBytes(t *testing.T) {
	got := canonicalEnvelope(t)
	want, err := os.ReadFile(envelopeContractFixturePath)
	if err != nil {
		t.Fatalf("read golden fixture %s: %v\n  produced: %s", envelopeContractFixturePath, err, got)
	}
	if got != string(want) {
		t.Fatalf("task result envelope drifted from golden fixture\n  fixture:  %s\n  produced: %s", want, got)
	}
	if !strings.Contains(got, `"tool_calls_ref":`) {
		t.Fatalf("envelope carries no tool_calls_ref: %s", got)
	}
	if strings.Contains(got, `"tool_calls":`) {
		t.Fatalf("envelope still carries an inline tool_calls array: %s", got)
	}
}
