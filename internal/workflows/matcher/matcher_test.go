package matcher

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestMatchExactStatusAndOutput(t *testing.T) {
	transitions := []definition.Transition{
		{From: "review", To: "verify", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
		{From: "review", To: "implement", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "changes_requested"}}, Loop: "review_repair", MaxIterations: -1},
	}
	d, err := Match("review", "succeeded", map[string]any{"verdict": "changes_requested", "findings": []any{}}, transitions)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "matched" || d.ToStepID != "implement" || d.TransitionIndex != 1 {
		t.Fatalf("decision = %+v", d)
	}
	if d.Loop != "review_repair" || d.MaxIterations != -1 {
		t.Fatalf("loop fields = %+v", d)
	}
	if d.Selected["status"] != "succeeded" || d.Selected["verdict"] != "changes_requested" {
		t.Fatalf("selected = %+v", d.Selected)
	}
	if d.MatchDigest == "" || len(d.DecisionJSON) == 0 {
		t.Fatalf("missing digest or decision json: %+v", d)
	}
	var payload map[string]any
	if err := json.Unmarshal(d.DecisionJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["outcome"] != "matched" || payload["to_step_id"] != "implement" {
		t.Fatalf("decision json = %s", d.DecisionJSON)
	}
}

func TestMatchStatusOnly(t *testing.T) {
	transitions := []definition.Transition{
		{From: "implement", To: "review", Match: definition.MatchCriteria{Status: "succeeded"}},
	}
	d, err := Match("implement", "succeeded", map[string]any{"summary": "done"}, transitions)
	if err != nil {
		t.Fatal(err)
	}
	if d.ToStepID != "review" || d.TransitionIndex != 0 {
		t.Fatalf("decision = %+v", d)
	}
}

func TestMatchScalarNumberAndBool(t *testing.T) {
	transitions := []definition.Transition{
		{From: "gate", To: "ok", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"count": "2", "ok": "false"}}},
	}
	d, err := Match("gate", "succeeded", map[string]any{"count": float64(2), "ok": false}, transitions)
	if err != nil {
		t.Fatal(err)
	}
	if d.Selected["count"] != "2" || d.Selected["ok"] != "false" {
		t.Fatalf("selected = %+v", d.Selected)
	}
}

func TestMatchRejectsNonScalarOutputLeaf(t *testing.T) {
	transitions := []definition.Transition{
		{From: "gate", To: "ok", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"meta": "x"}}},
	}
	_, err := Match("gate", "succeeded", map[string]any{"meta": map[string]any{"k": "v"}}, transitions)
	if err == nil || !strings.Contains(err.Error(), "no matching transition") {
		t.Fatalf("err = %v, want no matching transition", err)
	}
}

func TestMatchZeroMatchFailClosed(t *testing.T) {
	transitions := []definition.Transition{
		{From: "review", To: "ok", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
	}
	d, err := Match("review", "succeeded", map[string]any{"verdict": "changes_requested"}, transitions)
	if err == nil {
		t.Fatal("expected zero-match error")
	}
	if d.Outcome != "zero_match" || d.TransitionIndex != -1 {
		t.Fatalf("decision = %+v", d)
	}
	if !strings.Contains(string(d.DecisionJSON), "zero_match") {
		t.Fatalf("decision json = %s", d.DecisionJSON)
	}
}

func TestMatchMultiMatchFailClosed(t *testing.T) {
	transitions := []definition.Transition{
		{From: "a", To: "b", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "a", To: "c", Match: definition.MatchCriteria{Status: "succeeded"}},
	}
	d, err := Match("a", "succeeded", map[string]any{"x": "1"}, transitions)
	if err == nil {
		t.Fatal("expected multi-match error")
	}
	if d.Outcome != "multi_match" || d.TransitionIndex != -1 {
		t.Fatalf("decision = %+v", d)
	}
	if !strings.Contains(err.Error(), "multiple matching") {
		t.Fatalf("err = %v", err)
	}
}

func TestMatchIgnoresOtherFromSteps(t *testing.T) {
	transitions := []definition.Transition{
		{From: "other", To: "x", Match: definition.MatchCriteria{Status: "succeeded"}},
		{From: "review", To: "verify", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved"}}},
	}
	d, err := Match("review", "succeeded", map[string]any{"verdict": "approved"}, transitions)
	if err != nil {
		t.Fatal(err)
	}
	if d.TransitionIndex != 1 || d.ToStepID != "verify" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestMatchDigestStable(t *testing.T) {
	criteria := definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"verdict": "approved", "z": "1"}}
	a := matchDigest(criteria)
	b := matchDigest(definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"z": "1", "verdict": "approved"}})
	if a == "" || a != b {
		t.Fatalf("digests differ: %q vs %q", a, b)
	}
}

func TestMatchEmptyStatusRejected(t *testing.T) {
	_, err := Match("a", "", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
