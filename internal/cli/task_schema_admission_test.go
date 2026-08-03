package cli

// Schema admission happens before a task costs anything: an inadmissible
// schema or an input the schema rejects must refuse the whole call, on both
// dispatch_tasks and spawn_agent.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestDispatchTasksRefusesInputItsSchemaRejects(t *testing.T) {
	dispatch, _ := routingTools(t)
	_, err := dispatch.Execute(context.Background(), json.RawMessage(`{"tasks":[{
		"id":"t1","agent":"researcher","prompt":"work",
		"input_schema":{"type":"string","minLength":100}
	}]}`))
	if err == nil || !strings.Contains(err.Error(), "dispatch_tasks: task \"t1\"") {
		t.Fatalf("err = %v, want the task named in an input refusal", err)
	}
}

func TestDispatchTasksRefusesAnInadmissibleSchema(t *testing.T) {
	dispatch, _ := routingTools(t)
	_, err := dispatch.Execute(context.Background(), json.RawMessage(`{"tasks":[{
		"id":"t1","agent":"researcher","prompt":"work",
		"output_schema":{"$ref":"https://example.com/s.json"}
	}]}`))
	if err == nil || !strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("err = %v, want an output_schema admission refusal", err)
	}
}

func TestSpawnAgentRefusesInadmissibleAndRejectedSchemas(t *testing.T) {
	_, spawn := routingTools(t)
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "schema-test"})

	_, err := spawn.Execute(ctx, json.RawMessage(`{"tasks":[{
		"id":"s1","agent":"researcher","prompt":"work",
		"output_schema":{"$ref":"https://example.com/s.json"}
	}]}`))
	if err == nil || !strings.Contains(err.Error(), "spawn_agent: task \"s1\"") {
		t.Fatalf("err = %v, want the task named in an admission refusal", err)
	}

	_, err = spawn.Execute(ctx, json.RawMessage(`{"tasks":[{
		"id":"s2","agent":"researcher","prompt":"work",
		"input_schema":{"type":"string","minLength":100}
	}]}`))
	if err == nil || !strings.Contains(err.Error(), "spawn_agent: task \"s2\"") {
		t.Fatalf("err = %v, want the task named in an input refusal", err)
	}
}
