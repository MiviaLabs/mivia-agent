package keymap

import (
	"strings"
	"testing"
)

// reservedKeys must never be bound IN ANY CONTEXT. docs/design/ux-rules.md
// section 1: the terminal or the tty line discipline owns each one, and no
// context escapes it.
//
// The readline keys in that same section - ctrl+u, ctrl+e, ctrl+k, ctrl+r -
// are deliberately absent here. Readline owns the LINE EDITOR, so those are
// reserved inside the composer and free outside it, or free anywhere they
// keep readline's own meaning. ctrl+u is bound to clear-the-line, which is
// exactly what readline does with it.
var reservedKeys = map[string]string{
	"ctrl+s":  "XOFF: output freezes and the session looks hung",
	"ctrl+q":  "XON: the user's only recovery from ctrl+s",
	"ctrl+z":  "VSUSP: job control",
	"ctrl+\\": "VQUIT: the last-resort kill",
	"ctrl+v":  "VLNEXT: literal-next",
	"ctrl+d":  "EOF",
	"ctrl+w":  "readline word-rubout; close-tab in many emulators",
	"ctrl+a":  "readline beginning-of-line; the tmux prefix",
	"ctrl+k":  "readline kill-line",
	"ctrl+m":  "byte-identical to Enter",
	"ctrl+b":  "the GNU screen prefix",
}

func TestDefaultBindsNoReservedKey(t *testing.T) {
	for _, b := range Default() {
		for _, k := range b.Keys {
			if why, bad := reservedKeys[strings.ToLower(k)]; bad {
				t.Errorf("binding %s/%s uses reserved key %q (%s)", b.Context, b.ID, k, why)
			}
		}
	}
}

func TestDefaultHasNoCollisions(t *testing.T) {
	if got := New(Default()).Collisions(); len(got) != 0 {
		t.Errorf("collisions in the default keymap:\n%s", strings.Join(got, "\n"))
	}
}

func TestCollisionsDetectsADuplicate(t *testing.T) {
	m := New([]Binding{
		{ID: IDSend, Context: ContextComposer, Keys: []string{"enter"}},
		{ID: IDNewline, Context: ContextComposer, Keys: []string{"enter"}},
	})
	got := m.Collisions()
	if len(got) != 1 || !strings.Contains(got[0], "enter") {
		t.Errorf("got %v, want one collision naming enter", got)
	}
}

func TestSameKeyInDifferentContextsIsNotACollision(t *testing.T) {
	m := New([]Binding{
		{ID: IDSend, Context: ContextComposer, Keys: []string{"enter"}},
		{ID: IDMenuAccept, Context: ContextCompletion, Keys: []string{"enter"}},
	})
	if got := m.Collisions(); len(got) != 0 {
		t.Errorf("got %v, want none: the contexts are distinct", got)
	}
}

func TestSameIDBoundTwiceInOneContextIsNotACollision(t *testing.T) {
	// An alternate spelling of one action is not a conflict.
	m := New([]Binding{
		{ID: IDDenyAlways, Context: ContextApproval, Keys: []string{"D"}},
		{ID: IDDenyAlways, Context: ContextApproval, Keys: []string{"shift+d"}, Hidden: true},
	})
	if got := m.Collisions(); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestMatchResolvesWithinContextOnly(t *testing.T) {
	m := New(Default())

	if id, ok := m.Match(ContextApproval, "d"); !ok || id != IDDenyOnce {
		t.Errorf("approval d = %v/%v, want deny-once", id, ok)
	}
	if id, ok := m.Match(ContextApproval, "D"); !ok || id != IDDenyAlways {
		t.Errorf("approval D = %v/%v, want deny-always", id, ok)
	}
	// The same key means something else in another context, and nothing
	// where it is unbound.
	if id, ok := m.Match(ContextComposer, "enter"); !ok || id != IDSend {
		t.Errorf("composer enter = %v/%v, want send", id, ok)
	}
	if id, ok := m.Match(ContextCompletion, "enter"); !ok || id != IDMenuAccept {
		t.Errorf("completion enter = %v/%v, want menu-accept", id, ok)
	}
	if _, ok := m.Match(ContextPager, "y"); ok {
		t.Error("expected y to be unbound in the pager")
	}
}

func TestMatchUnknownContext(t *testing.T) {
	if _, ok := New(Default()).Match(Context("nope"), "enter"); ok {
		t.Error("expected no match in an unknown context")
	}
}

func TestHelpIsGeneratedFromTheTable(t *testing.T) {
	base := New(Default()).Help()
	if len(base) == 0 {
		t.Fatal("expected generated help rows")
	}

	// The proof that the table is the single source: add a binding and
	// the help grows, with no second edit anywhere.
	extended := New(append(Default(), Binding{
		ID: ID("invented"), Context: ContextGlobal, Keys: []string{"f9"}, Help: "invented",
	})).Help()
	if len(extended) != len(base)+1 {
		t.Errorf("got %d rows, want %d: help must come from the table", len(extended), len(base)+1)
	}
}

// TestHelpCoversEveryBindingAndContext pins the completeness of Help
// against its hardcoded context order. Dropping a context from that
// order removes a whole section from the help, and a row-count check on
// one context cannot see it.
func TestHelpCoversEveryBindingAndContext(t *testing.T) {
	rows := New(Default()).Help()

	wantRows, wantCtx := 0, map[Context]bool{}
	for _, b := range Default() {
		if b.Hidden {
			continue
		}
		wantRows++
		wantCtx[b.Context] = true
	}
	if len(rows) != wantRows {
		t.Errorf("help has %d rows, want one per visible binding (%d)", len(rows), wantRows)
	}

	gotCtx := map[Context]bool{}
	for _, r := range rows {
		gotCtx[r.Context] = true
	}
	for ctx := range wantCtx {
		if !gotCtx[ctx] {
			t.Errorf("context %q has bindings but no help rows", ctx)
		}
	}
}

func TestHelpOmitsHiddenBindings(t *testing.T) {
	rows := New(Default()).Help()
	for _, r := range rows {
		if r.Keys == "shift+d" {
			t.Error("hidden alternate spelling leaked into the help")
		}
	}
}

func TestHelpRendersSpaceReadably(t *testing.T) {
	rows := New([]Binding{
		{ID: IDToggleBlock, Context: ContextTranscript, Keys: []string{" ", "enter"}, Help: "toggle"},
	}).Help()
	if len(rows) != 1 || rows[0].Keys != "space / enter" {
		t.Errorf("got %+v, want the space key spelled out", rows)
	}
}

// TestComposerAndTranscriptShareTabWithoutCollision pins the resolution
// of the documented Tab conflict: Tab completes in the completion
// context and moves focus in the transcript context, so the two never
// contend within one context.
func TestComposerAndTranscriptShareTabWithoutCollision(t *testing.T) {
	m := New(Default())
	if id, ok := m.Match(ContextCompletion, "tab"); !ok || id != IDAcceptPrefix {
		t.Errorf("completion tab = %v/%v, want accept-prefix", id, ok)
	}
	if id, ok := m.Match(ContextTranscript, "tab"); !ok || id != IDFocusNext {
		t.Errorf("transcript tab = %v/%v, want focus-next", id, ok)
	}
	if _, ok := m.Match(ContextComposer, "tab"); ok {
		t.Error("tab must be unbound in the bare composer context")
	}
}
