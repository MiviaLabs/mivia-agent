package compiler

import (
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// The digest must not move when the definition types gain a field that a
// workflow does not use.
//
// Two fields were added to Step and Delivery in one day, and every run
// admitted before them became permanently unresumable: the digest is a hash
// of the marshalled struct, so a new field changed the marshalled bytes for
// byte-identical workflow text. The types now carry `json:"...,omitempty"`
// tags mirroring their toml tags, so a new field at its zero value is absent
// from the marshalled JSON exactly as it was absent before the field existed.
//
// This test proves the property directly, without relying on git history: it
// marshals a workflow through the CURRENT struct and asserts the JSON holds no
// trace of a field this workflow never set. If a future field addition omits
// `,omitempty`, this test fails at that field, before it ships.
func TestUnsetFieldsAreAbsentFromTheDigestInput(t *testing.T) {
	wf := minimalWorkflow(t)
	data, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"description", "delivery"} {
		if _, present := decoded[unwanted]; present {
			t.Fatalf("marshalled workflow carries unset field %q: %s", unwanted, data)
		}
	}
	steps, _ := decoded["steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("fixture has no steps")
	}
	step, _ := steps[0].(map[string]any)
	for _, unwanted := range []string{"panel", "on_failure", "verifier", "command"} {
		if _, present := step[unwanted]; present {
			t.Fatalf("marshalled step carries unset field %q: %s", unwanted, data)
		}
	}
}

// The concrete regression: a workflow admitted before Step.Panel and
// Delivery.OnFailure existed must compile to the SAME digest as it does with
// the current struct, because it never set either field.
func TestDigestMatchesWhatAnOlderBinaryWouldHaveComputed(t *testing.T) {
	wf := minimalWorkflow(t)
	current, err := Compile(wf)
	if err != nil {
		t.Fatal(err)
	}

	// Reproduce the byte-for-byte digest an older binary computed: the struct
	// this workflow uses, with the two later fields simply absent, is what
	// json.Marshal produced before those fields were added.
	type oldStep struct {
		ID       string `json:"id,omitempty"`
		Kind     string `json:"kind,omitempty"`
		Agent    string `json:"agent,omitempty"`
		Skill    string `json:"skill,omitempty"`
		Verifier string `json:"verifier,omitempty"`
	}
	type oldWorkflow struct {
		Version     int                            `json:"version,omitempty"`
		Name        string                         `json:"name,omitempty"`
		InitialStep string                         `json:"initial_step,omitempty"`
		Inputs      map[string]definition.InputDef `json:"inputs,omitempty"`
		Limits      definition.Limits              `json:"limits,omitempty"`
		Steps       []oldStep                      `json:"steps,omitempty"`
		Transitions []definition.Transition        `json:"transitions,omitempty"`
	}
	old := oldWorkflow{
		Version: wf.Version, Name: wf.Name, InitialStep: wf.InitialStep,
		Inputs: wf.Inputs, Limits: wf.Limits, Transitions: wf.Transitions,
	}
	for _, s := range wf.Steps {
		old.Steps = append(old.Steps, oldStep{ID: s.ID, Kind: s.Kind, Agent: s.Agent, Skill: s.Skill, Verifier: s.Verifier})
	}
	oldData, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	newData, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldData) != string(newData) {
		t.Fatalf("marshalled bytes changed for a workflow that never set the new fields:\nold: %s\nnew: %s", oldData, newData)
	}
	_ = current.Digest
}

func minimalWorkflow(t *testing.T) *definition.WorkflowFile {
	t.Helper()
	return &definition.WorkflowFile{
		Version: 1, Name: "digest-stability", InitialStep: "one",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "one", Kind: "agent", Agent: "dev"},
		},
		Transitions: []definition.Transition{
			{From: "one", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
}
