package cliorchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// dispatch_tasks used to publish additionalProperties:false at both levels and
// decode with DisallowUnknownFields, so ONE decorative field the model added
// ("description", "notes", "parallel") rejected the whole batch. On the SDK
// path that rejection happens inside decodeAndRun, before the tool runs: the
// call is counted toward the failure-spiral bound, and three of them stop the
// turn. A decoration the tool does not read is not a reason to refuse work.
//
// Permissive is not silent, though. A field that WOULD have changed routing
// stays a hard, named error (see TestRoutingSelectorsStayRejected), and a task
// whose prompt went missing - the shape a "promt" typo now produces, since the
// typo is no longer caught as an unknown field - is refused before it spawns.

// The two decoration tests that lived here (task-level and top-level) are
// subsumed by TestDecorationsSurviveTheNearMissCheck in near_miss_field_test.go,
// whose single request carries both and has to survive the harder rule.
// This file owns "unknown fields are ignored"; that file owns the carve-out.

// TestRoutingSelectorsStayRejected is the half permissiveness must not eat.
// handler/name/role are selectors from an older task API: silently ignoring
// one routes the task somewhere the model did not ask for, which is worse
// than refusing it. The error must NAME the field so the model can correct.
func TestRoutingSelectorsStayRejected(t *testing.T) {
	dispatch := routingTools(t)
	// "Handler" in mixed case too: encoding/json matches field names
	// case-insensitively, so a check that only knew the lowercase spelling
	// would let the selector through as an ignored decoration.
	for _, field := range []string{"handler", "name", "role", "model", "provider", "tools", "Handler"} {
		t.Run(field, func(t *testing.T) {
			args := `{"tasks":[{"id":"x","agent":"researcher","prompt":"work","` + field + `":"whatever"}]}`
			_, err := dispatch.Execute(context.Background(), json.RawMessage(args))
			if err == nil {
				t.Fatalf("%s was accepted and ignored; the task would run on a route the model did not select", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("error = %v, want the offending field named", err)
			}
			// The reserved-selector message, not the near-miss one: both name
			// the field, so without this the test no longer says WHICH gate
			// refused the call.
			if !strings.Contains(err.Error(), "route with") {
				t.Fatalf("error = %v, want the reserved-selector refusal naming "+
					"the replacement field", err)
			}
		})
	}
}

// TestBlankPromptIsRejected is the safety net permissiveness requires. With
// unknown fields ignored, a "promt" typo no longer fails the decode: it
// decodes to a task with an empty prompt, which would spawn a subagent with
// nothing to do and burn the batch's budget reporting nothing.
func TestBlankPromptIsRejected(t *testing.T) {
	dispatch := routingTools(t)
	for _, args := range []string{
		`{"tasks":[{"id":"x","promt":"work"}]}`,
		`{"tasks":[{"id":"x","prompt":"   "}]}`,
		`{"tasks":[{"id":"x"}]}`,
	} {
		_, err := dispatch.Execute(context.Background(), json.RawMessage(args))
		if err == nil {
			t.Fatalf("args %s: a task with no prompt was dispatched", args)
		}
		// The prompt-required refusal specifically: the near-miss guard also
		// produces a message containing "prompt", so the substring alone
		// cannot say which guard refused the call.
		if !strings.Contains(err.Error(), "prompt is required") {
			t.Fatalf("args %s: error = %v, want the prompt-required refusal", args, err)
		}
	}
}

// TestSchemaAdvertisesAdditionalProperties keeps the published schema honest:
// a provider-side validator must not reject what the tool now accepts.
func TestSchemaAdvertisesAdditionalProperties(t *testing.T) {
	params := routingTools(t).Parameters()
	if got := params["additionalProperties"]; got != true {
		t.Errorf("top-level additionalProperties = %v, want true", got)
	}
	item, ok := params["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	if !ok {
		t.Fatal("tasks.items is not an object schema")
	}
	if got := item["additionalProperties"]; got != true {
		t.Errorf("task item additionalProperties = %v, want true", got)
	}
}
