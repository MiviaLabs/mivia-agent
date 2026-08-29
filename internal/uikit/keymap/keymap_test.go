package keymap

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// reservedKeys must never be bound IN ANY CONTEXT. docs/design/ux-rules.md
// section 1: the terminal or the tty line discipline owns each one, and no
// context escapes it.
//
// The readline and multiplexer keys in that same section - ctrl+u, ctrl+e,
// ctrl+k, ctrl+r, ctrl+d, ctrl+b - are deliberately absent here. Readline
// owns the LINE EDITOR, so those are reserved inside the composer and free
// outside it, or free anywhere they keep readline's own meaning. ctrl+u is
// bound to clear-the-line in the composer, which is exactly what readline
// does with it. In the pager (transcript mode) ctrl+u, ctrl+d, ctrl+b and
// ctrl+f follow less: less itself binds them as half/full page motion, and
// a pager is not a line editor, so no readline gesture is lost. GNU screen
// intercepts ctrl+b before the app sees it, which makes the binding inert
// for screen users, not harmful - and the pager keeps modifier-free
// alternates (b, space) for the same actions. Amended 2026-08-19 with the
// transcript-mode keymap; see ux-rules.md section 10.
var reservedKeys = map[string]string{
	"ctrl+s":  "XOFF: output freezes and the session looks hung",
	"ctrl+q":  "XON: the user's only recovery from ctrl+s",
	"ctrl+z":  "VSUSP: job control",
	"ctrl+\\": "VQUIT: the last-resort kill",
	"ctrl+v":  "VLNEXT: literal-next",
	"ctrl+w":  "readline word-rubout; close-tab in many emulators",
	"ctrl+a":  "readline beginning-of-line; the tmux prefix",
	"ctrl+k":  "readline kill-line",
	"ctrl+m":  "byte-identical to Enter",
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

// TestSpaceIsBoundByItsReportedName pins the spelling against the real
// key event. bubbletea/v2 Key.String reports the space bar as "space", so
// a binding of " " matches nothing and the toggle key is silently dead.
func TestSpaceIsBoundByItsReportedName(t *testing.T) {
	reported := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}.String()
	m := New(Default())
	if id, ok := m.Match(ContextTranscript, reported); !ok || id != IDToggleBlock {
		t.Errorf("Match(transcript, %q) = (%q,%v), want the toggle binding", reported, id, ok)
	}
	if _, ok := m.Match(ContextTranscript, " "); ok {
		t.Error("a literal space still resolves; the binding must use the reported name")
	}
	rows := m.Help()
	found := false
	for _, r := range rows {
		if strings.Contains(r.Keys, "space") {
			found = true
		}
	}
	if !found {
		t.Error("the generated help does not spell the space key")
	}
}

// TestPagerKeysMatchRealKeyEvents pins every punctuation binding in the
// pager against the strings real KeyPressMsg values report. {, }, [ and /
// are printable glyphs, and a binding spelled by guesswork - shift+[,
// leftbracket - would silently never match.
func TestPagerKeysMatchRealKeyEvents(t *testing.T) {
	m := New(Default())
	cases := []struct {
		msg  tea.KeyPressMsg
		want ID
	}{
		{tea.KeyPressMsg{Code: '{'}, IDPagerPromptUp},
		{tea.KeyPressMsg{Code: '}'}, IDPagerPromptDn},
		{tea.KeyPressMsg{Code: '['}, IDDumpScrollback},
		{tea.KeyPressMsg{Code: 'v'}, IDEditTranscript},
		{tea.KeyPressMsg{Code: '/'}, IDSearchStart},
		{tea.KeyPressMsg{Code: 'n'}, IDSearchNext},
		{tea.KeyPressMsg{Code: 'N'}, IDSearchPrev},
		{tea.KeyPressMsg{Code: 'g'}, IDPagerTop},
		{tea.KeyPressMsg{Code: 'G'}, IDPagerBottom},
		{tea.KeyPressMsg{Code: 'q'}, IDLeavePager},
		{tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, IDPagerHalfUp},
		{tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, IDPagerHalfDown},
		{tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, IDPagerFullUp},
		{tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}, IDPagerFullDown},
		{tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, IDPagerFullDown},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, IDLeavePager},
		{tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, IDLeavePager},
	}
	for _, c := range cases {
		if id, ok := m.Match(ContextPager, c.msg.String()); !ok || id != c.want {
			t.Errorf("Match(pager, %q) = (%q,%v), want %q", c.msg.String(), id, ok, c.want)
		}
	}
}

// TestPagerHelpIsGeneratedFromTheTable proves the transcript-mode keys land
// in the help with no second edit: rule 6.2's whole table is visible.
func TestPagerHelpIsGeneratedFromTheTable(t *testing.T) {
	rows := New(Default()).Help()
	pager := 0
	for _, r := range rows {
		if r.Context == ContextPager {
			pager++
		}
	}
	if pager != 16 {
		t.Errorf("pager help has %d rows, want 16 (one per visible pager binding)", pager)
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

func TestQueueDialogBinding(t *testing.T) {
	m := New(Default())
	if id, ok := m.Match(ContextGlobal, "ctrl+up"); !ok || id != IDQueueDialog {
		t.Errorf("global ctrl+up = %v/%v, want %v", id, ok, IDQueueDialog)
	}
}

// TestHintIsGeneratedFromTheTable pins the compact footer hint: it must
// come from the same table as the help screen, use each binding's first
// key, and skip IDs that have no Short label rather than print junk.
func TestHintIsGeneratedFromTheTable(t *testing.T) {
	m := New(Default())
	got := m.Hint(IDHelp, IDOpenPager, IDQuit)
	if want := "?:help  ctrl+o:transcript  ctrl+c:quit"; got != want {
		t.Errorf("Hint = %q, want %q", got, want)
	}
	if got := m.Hint(IDCancel); got != "" {
		t.Errorf("Hint(cancel) = %q, want empty: no Short label", got)
	}
	if got := m.Hint(ID("no-such-id")); got != "" {
		t.Errorf("Hint(unknown) = %q, want empty", got)
	}
}

// TestForceSendBindings pins the force-send rows: ctrl+enter and f4 in
// the composer (interrupt the current turn - f4 is the degradation-safe
// alternate, since not every terminal answers bubbletea's
// KittyKeyboard/modifyOtherKeys request), f/F in the dialog (force send
// the selected queued item), all resolving to IDForceSend with no
// collision against the surrounding table.
func TestForceSendBindings(t *testing.T) {
	m := New(Default())
	if id, ok := m.Match(ContextComposer, "ctrl+enter"); !ok || id != IDForceSend {
		t.Errorf("composer ctrl+enter = %v/%v, want %v", id, ok, IDForceSend)
	}
	if id, ok := m.Match(ContextComposer, "f4"); !ok || id != IDForceSend {
		t.Errorf("composer f4 = %v/%v, want %v", id, ok, IDForceSend)
	}
	if id, ok := m.Match(ContextDialog, "f"); !ok || id != IDForceSend {
		t.Errorf("dialog f = %v/%v, want %v", id, ok, IDForceSend)
	}
	if id, ok := m.Match(ContextDialog, "F"); !ok || id != IDForceSend {
		t.Errorf("dialog F = %v/%v, want %v", id, ok, IDForceSend)
	}
	if got := New(Default()).Collisions(); len(got) != 0 {
		t.Errorf("collisions after adding force-send:\n%s", strings.Join(got, "\n"))
	}
}

// TestForceSendAppearsInHelp proves the new rows render through the
// generated help, not a second hardcoded list.
func TestForceSendAppearsInHelp(t *testing.T) {
	rows := New(Default()).Help()
	var composer, dialog int
	for _, r := range rows {
		if r.Help == "force send now, interrupting the current turn (f4 works in any terminal)" && r.Context == ContextComposer {
			composer++
		}
		if r.Help == "force send the selected queued message" && r.Context == ContextDialog {
			dialog++
		}
	}
	if composer != 1 {
		t.Errorf("expected exactly one composer force-send help row, got %d", composer)
	}
	if dialog != 1 {
		t.Errorf("expected exactly one dialog force-send help row, got %d", dialog)
	}
}

// TestKeymapPackageImportsNoBubbletea pins the package doc's rule: this
// package deals in key strings, not bubbletea types, so nothing under
// internal/uikit may import bubbletea. A source scan catches a
// reintroduced import even though the compiler would too - it fails
// fast and names the offending file without needing a build.
func TestKeymapPackageImportsNoBubbletea(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, e.Name(), src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "bubbletea") || strings.Contains(path, "bubbles") {
				t.Errorf("%s imports %q: internal/uikit must not depend on bubbletea", e.Name(), path)
			}
		}
	}
}
