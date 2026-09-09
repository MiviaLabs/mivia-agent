package cliorchestrate

// Schema admission happens before a task costs anything: an inadmissible
// schema or an input the schema rejects must refuse the whole call, on both
// dispatch_tasks and spawn_agent.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatchTasksRefusesInputItsSchemaRejects(t *testing.T) {
	dispatch := routingTools(t)
	_, err := dispatch.Execute(context.Background(), json.RawMessage(`{"tasks":[{
		"id":"t1","agent":"researcher","prompt":"work",
		"input_schema":{"type":"string","minLength":100}
	}]}`))
	if err == nil || !strings.Contains(err.Error(), "dispatch_tasks: task \"t1\"") {
		t.Fatalf("err = %v, want the task named in an input refusal", err)
	}
}

func TestDispatchTasksRefusesAnInadmissibleSchema(t *testing.T) {
	dispatch := routingTools(t)
	_, err := dispatch.Execute(context.Background(), json.RawMessage(`{"tasks":[{
		"id":"t1","agent":"researcher","prompt":"work",
		"output_schema":{"$ref":"https://example.com/s.json"}
	}]}`))
	if err == nil || !strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("err = %v, want an output_schema admission refusal", err)
	}
}

func TestDispatchTasksRawDecoderRejectsInvalidAgentFieldsAndDuplicateKeys(t *testing.T) {
	dispatch := routingTools(t)
	cases := []string{
		`{"tasks":null}`,
		`{"tasks":[]}`,
		`{"tasks":[{"id":"t1","prompt":"work","timeout_seconds":null}]}`,
		`{"tasks":[{"id":"t1","prompt":"work","budget":null}]}`,
		`{"tasks":[{"id":"t1","prompt":"work","agent":null}]}`,
		`{"tasks":[{"id":"t1","prompt":"work","skill":null}]}`,
		`{"tasks":[{"id":"t1","prompt":"work","agent":1}]}`,
		`{"tasks":[{"id":"t1","prompt":"work","skill":1}]}`,
		`{"tasks":[{"id":"t1","prompt":"work","agent":"a","agent":"b"}]}`,
		`{"tasks":[{"id":"t1","prompt":"work","skill":"a","skill":"b"}]}`,
		`{"tasks":[{"id":"t1","prompt":"work"}],"tasks":[]}`,
		`{"tasks":[{"id":"t1","prompt":"work"}]} trailing`,
		`{"tasks":[{"id":"t1","prompt":"work"}`,
	}
	for _, raw := range cases {
		if _, err := dispatch.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("Execute(%s) accepted invalid JSON", raw)
		}
	}
}
