package uiadapter_test

// AR-1 consumer half: this file decodes the golden fixtures under
// internal/cliorchestrate/testdata/ through the real reconstruction entry
// point, PopulateFromToolCalls.
//
//   - task_result_envelope_contract.json is the CURRENT shape (final
//     report plus tool_calls_ref); the cliorchestrate producer test pins
//     the same bytes, so drift fails on one side of the pair.
//   - tool_calls_contract.json is the FROZEN legacy inline tool_calls
//     shape old persisted sessions still carry. No producer emits it
//     anymore; this package alone pins that decode so old sessions keep
//     reconstructing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// contractFixturePath anchors on the module root, the pattern
// internal/clichat/feature_delivery_contract_test.go (committedWorkflowRoot)
// already uses for cross-package file reads from tests: go test runs with
// the package directory as working directory, so "../.." is the module root.
func contractFixturePath(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	path := filepath.Join(root, "internal", "cliorchestrate", "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("golden fixture not found at %s: %v", path, err)
	}
	return path
}

func readContractFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(contractFixturePath(t, "tool_calls_contract.json"))
	if err != nil {
		t.Fatalf("read golden tool_calls fixture: %v", err)
	}
	return raw
}

// contractWireMessages wraps fixture rows in the dispatch_tasks envelope the
// way the persisted tool call carries it: arguments name one task; output is
// the encoded result array with the "tool_calls" array inline.
func contractWireMessages(toolCallsJSON []byte) []ports.Message {
	return []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_wire",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-contract","prompt":"dispatch the contract tasks","agent":"researcher"}]}`,
					Output:    `[{"task_id":"task-contract","status":"completed","output":"contract pinned output","tool_calls":` + string(toolCallsJSON) + `}]`,
				},
			},
		},
	}
}

// wantContractToolCalls is what every row in the fixture must reconstruct to.
// The incomplete tail row keeps its input but carries no output text.
var wantContractToolCalls = []ports.ToolCall{
	{ID: "call_grep_01", Name: "grep", Arguments: `{"pattern":"func load"}`, Output: "3 matches in 2 files"},
	{ID: "call_read_02", Name: "read"},
	{ID: "call_bash_03", Name: "bash", Arguments: `{"cmd":"go build ./..."}`, Output: ""},
}

// assertReconstruction runs PopulateFromToolCalls and checks the full
// reconstructed message pair and all decoded tool-call fields.
func assertReconstruction(t *testing.T, msgs []ports.Message) {
	t.Helper()
	threads := uiadapter.NewSubagentThreads()
	uiadapter.PopulateFromToolCalls(threads, msgs)

	conv, ok := threads.Thread("call_dispatch_wire:task-contract")
	if !ok || conv == nil {
		t.Fatal("expected thread for task-contract")
	}
	hist := conv.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 history messages (prompt + output), got %d", len(hist))
	}
	if hist[0].Text != "dispatch the contract tasks" {
		t.Errorf("prompt mismatch: got %q", hist[0].Text)
	}
	if hist[1].Text != "contract pinned output" {
		t.Errorf("output text mismatch: got %q", hist[1].Text)
	}
	if !reflect.DeepEqual(hist[1].ToolCalls, wantContractToolCalls) {
		t.Errorf("decoded tool_calls drifted from fixture values:\n  got:  %+v\n  want: %+v", hist[1].ToolCalls, wantContractToolCalls)
	}
}

// TestPopulateFromToolCalls_RefEnvelopeContractGoldenFixture pins the
// consumer side of the CURRENT envelope shape to the shared golden fixture:
// the final report text reconstructs, the tool_calls_ref key is tolerated,
// and no tool-call rows are fabricated from it.
func TestPopulateFromToolCalls_RefEnvelopeContractGoldenFixture(t *testing.T) {
	raw, err := os.ReadFile(contractFixturePath(t, "task_result_envelope_contract.json"))
	if err != nil {
		t.Fatalf("read golden envelope fixture: %v", err)
	}
	threads := uiadapter.NewSubagentThreads()
	uiadapter.PopulateFromToolCalls(threads, []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_dispatch_ref",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-contract","prompt":"dispatch the contract tasks","agent":"researcher"}]}`,
					Output:    string(raw),
				},
			},
		},
	})

	conv, ok := threads.Thread("call_dispatch_ref:task-contract")
	if !ok || conv == nil {
		t.Fatal("expected thread for task-contract")
	}
	hist := conv.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 history messages (prompt + output), got %d", len(hist))
	}
	if hist[1].Text != "contract pinned output" {
		t.Errorf("output text mismatch: got %q", hist[1].Text)
	}
	if len(hist[1].ToolCalls) != 0 {
		t.Errorf("ref-only envelope must not fabricate tool-call rows, got %+v", hist[1].ToolCalls)
	}
}

// TestPopulateFromToolCalls_WireContractGoldenFixture pins the LEGACY
// (frozen) inline tool_calls decode old persisted sessions still need, then
// proves DC-14 tolerance: unknown keys inside a row object must be ignored
// without changing reconstruction.
func TestPopulateFromToolCalls_WireContractGoldenFixture(t *testing.T) {
	fixture := readContractFixture(t)

	assertReconstruction(t, contractWireMessages(fixture))

	t.Run("unknown row keys are ignored (DC-14)", func(t *testing.T) {
		var generic []map[string]any
		if err := json.Unmarshal(fixture, &generic); err != nil {
			t.Fatalf("decode fixture as generic rows: %v", err)
		}
		for i, row := range generic {
			row["future_extension_key"] = "ignored"
			row["future_nested"] = map[string]any{"nested": float64(i)}
		}
		mutated, err := json.Marshal(generic)
		if err != nil {
			t.Fatalf("re-marshal mutated rows: %v", err)
		}
		// Reconstruction must stay identical when unknown keys ride along.
		assertReconstruction(t, contractWireMessages(mutated))
	})
}
