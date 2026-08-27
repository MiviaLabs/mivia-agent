package delivery

import "testing"

func TestRenderDeterministicBindings(t *testing.T) {
	got, err := Render("task={{ inputs.task }} evidence={{ evidence.plan }}", map[string]any{"task": "build"}, map[string]any{"plan": map[string]any{"ok": true}}, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got != `task=build evidence={"ok":true}` {
		t.Fatalf("rendered = %q", got)
	}
}

func TestRenderRejectsMissingAndOversizedBindings(t *testing.T) {
	if _, err := Render("{{ inputs.missing }}", nil, nil, 10, 100); err == nil {
		t.Fatal("missing binding was accepted")
	}
	if _, err := Render("{{ inputs.task }}", map[string]any{"task": "12345"}, nil, 4, 100); err == nil {
		t.Fatal("oversized binding was accepted")
	}
}

func TestRenderRejectsUnrestrictedSyntax(t *testing.T) {
	if _, err := Render("{{ steps.secret.output }}", nil, nil, 100, 100); err == nil {
		t.Fatal("unrestricted binding was accepted")
	}
}
