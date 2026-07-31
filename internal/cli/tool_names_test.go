package cli

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

func TestToolAndHandlerNameConsts(t *testing.T) {
	// Wire contracts: a typo in a const value must fail here before it reaches the model.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"handlerMultiStep", handlerMultiStep, "multi_step"},
		{"handlerDelegate", handlerDelegate, "delegate"},
		{"handlerOneshot", handlerOneshot, "oneshot"},
		{"toolDispatchTasks", toolDispatchTasks, "dispatch_tasks"},
		{"toolSpawnAgent", toolSpawnAgent, "spawn_agent"},
		{"toolJoinRun", toolJoinRun, "join_run"},
		{"toolInspectAgents", toolInspectAgents, "inspect_agents"},
		{"toolCancelRun", toolCancelRun, "cancel_run"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestBuiltinHandlerNamesOrder(t *testing.T) {
	want := []string{"multi_step", "delegate", "oneshot"}
	if !reflect.DeepEqual(builtinHandlerNames, want) {
		t.Fatalf("builtinHandlerNames = %#v, want %#v", builtinHandlerNames, want)
	}
	// Consts must be the same values the ordered list advertises.
	if builtinHandlerNames[0] != handlerMultiStep ||
		builtinHandlerNames[1] != handlerDelegate ||
		builtinHandlerNames[2] != handlerOneshot {
		t.Fatal("builtinHandlerNames must be built from the handler consts")
	}
}

func TestInjectHandlerEnumNameAndHandlerProps(t *testing.T) {
	for _, prop := range []string{"name", "handler"} {
		result := newTaskSchemaMap(prop)
		injectHandlerEnum(result, prop, nil)
		got := enumFromSchema(t, result, prop)
		want := []string{"multi_step", "delegate", "oneshot"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("prop %q enum = %#v, want %#v", prop, got, want)
		}
	}
}

func TestInjectHandlerEnumAppendsSkills(t *testing.T) {
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{
		Name:        "fake-skill",
		Description: "test skill",
	}); err != nil {
		t.Fatal(err)
	}
	result := newTaskSchemaMap("handler")
	injectHandlerEnum(result, "handler", reg)
	got := enumFromSchema(t, result, "handler")
	want := []string{"multi_step", "delegate", "oneshot", "fake-skill"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enum with skill = %#v, want %#v", got, want)
	}
}

func TestInjectHandlerEnumAliasSafe(t *testing.T) {
	before := append([]string(nil), builtinHandlerNames...)
	result := newTaskSchemaMap("name")
	injectHandlerEnum(result, "name", nil)
	enum := enumFromSchema(t, result, "name")
	// Mutating the injected slice must not alter the shared ordered list.
	enum = append(enum, "should-not-leak")
	_ = enum
	if !reflect.DeepEqual(builtinHandlerNames, before) {
		t.Fatalf("builtinHandlerNames mutated after inject/append: %#v", builtinHandlerNames)
	}
	// Also mutate via the schema map's slice if it aliases the shared backing array.
	props := result["properties"].(map[string]any)
	tasks := props["tasks"].(map[string]any)
	items := tasks["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	target := itemProps["name"].(map[string]any)
	injected := target["enum"].([]string)
	if len(injected) > 0 {
		injected[0] = "mutated"
	}
	if builtinHandlerNames[0] != "multi_step" {
		t.Fatalf("shared list element mutated via injected enum alias: %#v", builtinHandlerNames)
	}
}

// newTaskSchemaMap builds the nested shape Parameters() produces before enum injection.
func newTaskSchemaMap(prop string) map[string]any {
	return map[string]any{
		"properties": map[string]any{
			"tasks": map[string]any{
				"items": map[string]any{
					"properties": map[string]any{
						prop: map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
	}
}

func enumFromSchema(t *testing.T, result map[string]any, prop string) []string {
	t.Helper()
	props, ok := result["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing properties")
	}
	tasks, ok := props["tasks"].(map[string]any)
	if !ok {
		t.Fatal("missing tasks")
	}
	items, ok := tasks["items"].(map[string]any)
	if !ok {
		t.Fatal("missing items")
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing item properties")
	}
	target, ok := itemProps[prop].(map[string]any)
	if !ok {
		t.Fatalf("missing prop %q", prop)
	}
	enum, ok := target["enum"].([]string)
	if !ok {
		t.Fatalf("prop %q enum not []string: %T", prop, target["enum"])
	}
	return enum
}
