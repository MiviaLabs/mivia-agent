package cliworkflow

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestInputKeyContainingEqualsIsRejected closes a key-confusion hole.
//
// startCLI flattens the model's inputs map to "k=v" strings and
// parseWorkflowInputs re-splits on the FIRST "=", so the key "task=INJECTED"
// with value "payload" became the flag "task=INJECTED=payload", which parsed
// back as key "task" with value "INJECTED=payload". A key that must be
// refused as unknown instead SATISFIED a required input with caller-chosen
// content. workflow_run's schema sets additionalProperties:true on inputs, so
// arbitrary keys reach this path.
func TestInputKeyContainingEqualsIsRejected(t *testing.T) {
	if _, err := inputsToRawFlags(map[string]any{"task=INJECTED": "payload"}); err == nil {
		t.Fatal("inputsToRawFlags accepted a key containing '=', which re-binds to a different declared input")
	} else if !strings.Contains(err.Error(), "task=INJECTED") {
		t.Fatalf("error %q does not name the offending key", err)
	}

	// The whole point is that it must not silently become a valid "task".
	flags, err := inputsToRawFlags(map[string]any{"task": "real"})
	if err != nil {
		t.Fatalf("inputsToRawFlags(valid key): %v", err)
	}
	defs := map[string]definition.InputDef{"task": {Type: "string", Required: true}}
	values, _, err := parseWorkflowInputs(flags, defs)
	if err != nil {
		t.Fatalf("parseWorkflowInputs: %v", err)
	}
	if values["task"] != "real" {
		t.Fatalf("values[task] = %v, want \"real\"", values["task"])
	}
}

// TestInputKeyMustNotBeBlank keeps the other malformed-key shape refused at
// the same seam rather than deeper in the parser.
func TestInputKeyMustNotBeBlank(t *testing.T) {
	for _, key := range []string{"", "   "} {
		if _, err := inputsToRawFlags(map[string]any{key: "v"}); err == nil {
			t.Errorf("inputsToRawFlags accepted the blank key %q", key)
		}
	}
}

// TestInvocationSnapshotMatchesAdmissionForNonStrings pins the idempotency
// contract for object and array inputs.
//
// Admission records json.Marshal(v) as the snapshot value, but
// invocationInputsMatchRun compared fmt.Sprint(v) - "map[a:b]" against
// {"a":"b"}. The two never agree, so a byte-identical keyed retry after a
// process restart was refused as "bound to run X with different inputs", and
// the run became unreachable under its own invocation key. That is the exact
// wrong-run-continuation failure the guard exists to prevent, inverted.
func TestInvocationSnapshotMatchesAdmissionForNonStrings(t *testing.T) {
	cases := map[string]struct {
		def    definition.InputDef
		inputs map[string]any
	}{
		"object": {
			def:    definition.InputDef{Type: "object", Required: true},
			inputs: map[string]any{"cfg": map[string]any{"a": "b"}},
		},
		"array": {
			def:    definition.InputDef{Type: "array", Required: true},
			inputs: map[string]any{"cfg": []any{"a", "b"}},
		},
		"boolean": {
			def:    definition.InputDef{Type: "boolean", Required: true},
			inputs: map[string]any{"cfg": true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			flags, err := inputsToRawFlags(tc.inputs)
			if err != nil {
				t.Fatalf("inputsToRawFlags: %v", err)
			}
			_, admitted, err := parseWorkflowInputs(flags, map[string]definition.InputDef{"cfg": tc.def})
			if err != nil {
				t.Fatalf("parseWorkflowInputs: %v", err)
			}
			compare, err := invocationInputSnapshot(tc.inputs)
			if err != nil {
				t.Fatalf("invocationInputSnapshot: %v", err)
			}
			if workflowledger.InputDigest(admitted) != workflowledger.InputDigest(compare) {
				t.Fatalf("admission snapshot %v and invocation snapshot %v hash differently; an identical keyed retry would be refused", admitted, compare)
			}
		})
	}
}
