package conversation

import "testing"

// The panel row id for a dispatched task IS the id the tool mints for it, and
// it is also the id an operator's cancel routes on (remote_cancel_target.go).
// The two are derived independently - the tool from its own decode, the UI
// from the model's raw arguments - so they have to read the model's JSON the
// same way.
//
// encoding/json matches field names case-insensitively, so a task written with
// "ID" or "Tasks" decodes perfectly well for the tool. This side read those
// keys exact-cased: the row fell back to a positional "task-N" placeholder
// that no later progress, heartbeat or done event could ever match, so it sat
// at Step 0 and eventually rendered "stalled" - and a cancel aimed at it named
// an id nothing had registered, silently doing nothing while the subagent kept
// spending the batch's budget.
func TestDispatchRowIDsMatchTheDecoderSFold(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want []string
	}{
		"lowercase, unchanged": {
			map[string]any{"tasks": []any{map[string]any{"id": "alpha"}}},
			[]string{"call-9:alpha"},
		},
		"acronym-cased task id": {
			map[string]any{"tasks": []any{map[string]any{"ID": "alpha"}}},
			[]string{"call-9:alpha"},
		},
		"mixed spellings in one batch": {
			map[string]any{"tasks": []any{
				map[string]any{"ID": "alpha"},
				map[string]any{"id": "beta"},
			}},
			[]string{"call-9:alpha", "call-9:beta"},
		},
		"capitalised tasks array": {
			map[string]any{"Tasks": []any{map[string]any{"id": "alpha"}}},
			[]string{"call-9:alpha"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := dispatchTaskIDs("call-9", "dispatch_tasks", tc.args)
			if len(got) != len(tc.want) {
				t.Fatalf("ids = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ids[%d] = %q, want %q; a row id the tool never mints can "+
						"never be updated or cancelled", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestDispatchRowNamesMatchTheDecoderSFold is the label half of the same
// rule. The id now folds; the routed-agent name read exact-cased, so a task
// written {"Agent":"reviewer"} routed to reviewer for the tool and rendered a
// row with no agent label at all - an operator watching a batch could not tell
// which agent the row belonged to.
func TestDispatchRowNamesMatchTheDecoderSFold(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"lowercase":   {"tasks": []any{map[string]any{"id": "alpha", "agent": "reviewer"}}},
		"capitalised": {"tasks": []any{map[string]any{"id": "alpha", "Agent": "reviewer"}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, names := dispatchTaskIDsAndNames("call-9", "dispatch_tasks", args)
			if got := names["call-9:alpha"]; got != "reviewer" {
				t.Errorf("row name = %q, want %q; the tool routes this task to reviewer, "+
					"so the row has to say so", got, "reviewer")
			}
		})
	}
}

// TestFoldedArgPrefersTheExactSpelling pins the resolution order for an object
// carrying two folded spellings of one key. The tool refuses such a batch, so
// this only governs what the panel shows in the moment before the refusal
// arrives - but "whatever the map iterator reached first" is not an answer,
// and exact-match-first is the same precedence encoding/json applies.
func TestFoldedArgPrefersTheExactSpelling(t *testing.T) {
	m := map[string]any{"ID": "folded", "id": "exact", "Id": "folded too"}
	for i := 0; i < 20; i++ {
		if got := foldedArg(m, "id"); got != "exact" {
			t.Fatalf("foldedArg = %v, want the exactly-spelled key on every run", got)
		}
	}

	// With no exact spelling to prefer, the pick must still be the same one
	// every run - map order would otherwise make the row id vary between
	// renders, and that id is what a cancel routes on.
	folded := map[string]any{"ID": "upper", "Id": "mixed"}
	first := foldedArg(folded, "id")
	if first != "upper" {
		t.Fatalf("foldedArg = %v, want the first key in sorted order", first)
	}
	for i := 0; i < 20; i++ {
		if got := foldedArg(folded, "id"); got != first {
			t.Fatalf("foldedArg = %v then %v; the pick must not depend on map order", first, got)
		}
	}
}

// TestDispatchRowIDsStillFallBackWhenIDIsAbsent keeps the placeholder for the
// case it exists for: a task the model left unnamed. The tool refuses that
// batch outright, so the row is short-lived, but it must not collide.
func TestDispatchRowIDsStillFallBackWhenIDIsAbsent(t *testing.T) {
	got := dispatchTaskIDs("call-9", "dispatch_tasks", map[string]any{
		"tasks": []any{map[string]any{"prompt": "p"}, map[string]any{"prompt": "q"}},
	})
	want := []string{"task-1", "task-2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
