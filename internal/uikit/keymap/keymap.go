// Package keymap is the keymap as data, not as code. One table yields
// the runtime dispatch, the generated help screen, and the collision
// test. Build spec section 4.6 requires this: a switch statement cannot
// generate its own help, and the two drift.
//
// This package lives under internal/uikit, so it must not import
// bubbletea. It deals in key strings, which is what
// bubbletea's Key.String() produces; the view layer does the conversion.
package keymap

import "sort"

// Context is the surface a binding applies to. A key may mean different
// things in different contexts, and the same key may appear once per
// context without collision.
type Context string

const (
	// ContextGlobal applies everywhere, in every screen and dialog.
	ContextGlobal Context = "global"
	// ContextComposer applies while the composer holds focus and no
	// completion menu is open.
	ContextComposer Context = "composer"
	// ContextCompletion applies while the completion menu is open. It is
	// separate from ContextComposer because the menu must claim Enter,
	// Esc and the arrows before the composer or the app see them.
	ContextCompletion Context = "completion"
	// ContextTranscript applies while a transcript block holds focus.
	ContextTranscript Context = "transcript"
	// ContextApproval applies while an approval prompt is pending.
	ContextApproval Context = "approval"
	// ContextDialog applies inside a modal dialog.
	ContextDialog Context = "dialog"
	// ContextPager applies inside the full-screen pager.
	ContextPager Context = "pager"
)

// ID names an action. The view layer switches on ID, never on a key
// string, so a rebind changes this table and nothing else.
type ID string

// Action identifiers.
const (
	IDCancel        ID = "cancel"
	IDQuit          ID = "quit"
	IDSend          ID = "send"
	IDNewline       ID = "newline"
	IDClearLine     ID = "clear-line"
	IDHelp          ID = "help"
	IDThemeDialog   ID = "theme-dialog"
	IDFocusNext     ID = "focus-next"
	IDFocusPrev     ID = "focus-prev"
	IDToggleBlock   ID = "toggle-block"
	IDExpandAll     ID = "expand-all"
	IDCollapseAll   ID = "collapse-all"
	IDCopyBlock     ID = "copy-block"
	IDOpenPager     ID = "open-pager"
	IDToggleReason  ID = "toggle-reasoning"
	IDApproveOnce   ID = "approve-once"
	IDApproveAlways ID = "approve-always"
	IDDenyOnce      ID = "deny-once"
	IDDenyAlways    ID = "deny-always"
	IDAcceptPrefix  ID = "accept-prefix"
	IDMenuNext      ID = "menu-next"
	IDMenuPrev      ID = "menu-prev"
	IDMenuAccept    ID = "menu-accept"
	IDMenuDismiss   ID = "menu-dismiss"
	IDDialogUp      ID = "dialog-up"
	IDDialogDown    ID = "dialog-down"
	IDDialogAccept  ID = "dialog-accept"
	IDDialogCancel  ID = "dialog-cancel"
	IDNextHunk      ID = "next-hunk"
	IDPrevHunk      ID = "prev-hunk"
)

// Binding is one row of the table.
type Binding struct {
	ID      ID
	Context Context
	Keys    []string
	Help    string
	// Hidden keeps a binding out of the generated help without removing
	// it from dispatch. Use it for alternate spellings of a key, never
	// to hide a binding a user must know about.
	Hidden bool
}

// Default returns the shipped keymap.
//
// Reserved keys are deliberately absent; see docs/design/ux-rules.md
// section 1. Notably Ctrl-S is XOFF and freezes the terminal, so the
// design's Ctrl-S reasoning toggle moved to Ctrl-R. Ctrl-M is
// byte-identical to Enter and is never bound. Ctrl-W is readline's
// word-rubout and is "close tab" in many emulators, so collapse-all
// moved off it too.
func Default() []Binding {
	return []Binding{
		// Global.
		{ID: IDCancel, Context: ContextGlobal, Keys: []string{"esc"}, Help: "cancel the turn, keep the text"},
		{ID: IDQuit, Context: ContextGlobal, Keys: []string{"ctrl+c"}, Help: "cancel; press twice to quit"},
		{ID: IDThemeDialog, Context: ContextGlobal, Keys: []string{"ctrl+t"}, Help: "theme"},
		{ID: IDOpenPager, Context: ContextGlobal, Keys: []string{"ctrl+o"}, Help: "open the pager"},
		{ID: IDToggleReason, Context: ContextGlobal, Keys: []string{"ctrl+r"}, Help: "show or hide reasoning"},

		// Composer.
		{ID: IDSend, Context: ContextComposer, Keys: []string{"enter"}, Help: "send"},
		{ID: IDNewline, Context: ContextComposer, Keys: []string{"ctrl+j"}, Help: "newline"},
		{ID: IDClearLine, Context: ContextComposer, Keys: []string{"ctrl+u"}, Help: "clear the line"},
		{ID: IDHelp, Context: ContextComposer, Keys: []string{"?"}, Help: "show this keymap (empty composer)"},
		{ID: IDFocusPrev, Context: ContextComposer, Keys: []string{"shift+tab"}, Help: "focus the newest block"},

		// Completion menu. It claims these before the composer.
		{ID: IDMenuAccept, Context: ContextCompletion, Keys: []string{"enter"}, Help: "accept"},
		{ID: IDAcceptPrefix, Context: ContextCompletion, Keys: []string{"tab"}, Help: "accept the common prefix"},
		{ID: IDMenuNext, Context: ContextCompletion, Keys: []string{"down"}, Help: "next"},
		{ID: IDMenuPrev, Context: ContextCompletion, Keys: []string{"up"}, Help: "previous"},
		{ID: IDMenuDismiss, Context: ContextCompletion, Keys: []string{"esc"}, Help: "dismiss"},

		// Transcript, scoped to the live window.
		{ID: IDFocusNext, Context: ContextTranscript, Keys: []string{"tab"}, Help: "focus the next block"},
		{ID: IDFocusPrev, Context: ContextTranscript, Keys: []string{"shift+tab"}, Help: "focus the previous block"},
		{ID: IDToggleBlock, Context: ContextTranscript, Keys: []string{" ", "enter"}, Help: "collapse or expand"},
		{ID: IDExpandAll, Context: ContextTranscript, Keys: []string{"ctrl+e"}, Help: "expand all"},
		{ID: IDCollapseAll, Context: ContextTranscript, Keys: []string{"ctrl+g"}, Help: "collapse all"},
		{ID: IDCopyBlock, Context: ContextTranscript, Keys: []string{"y"}, Help: "copy the block"},
		{ID: IDCancel, Context: ContextTranscript, Keys: []string{"esc"}, Help: "return to the composer"},

		// Approval. wireframes-panes.md section 7.
		{ID: IDApproveOnce, Context: ContextApproval, Keys: []string{"o", "enter"}, Help: "once"},
		{ID: IDApproveAlways, Context: ContextApproval, Keys: []string{"a"}, Help: "always"},
		{ID: IDDenyOnce, Context: ContextApproval, Keys: []string{"d", "esc"}, Help: "deny"},
		{ID: IDDenyAlways, Context: ContextApproval, Keys: []string{"D"}, Help: "deny always"},
		{ID: IDDenyAlways, Context: ContextApproval, Keys: []string{"shift+d"}, Hidden: true},

		// Dialogs.
		{ID: IDDialogUp, Context: ContextDialog, Keys: []string{"up"}, Help: "previous"},
		{ID: IDDialogDown, Context: ContextDialog, Keys: []string{"down"}, Help: "next"},
		{ID: IDDialogAccept, Context: ContextDialog, Keys: []string{"enter"}, Help: "apply"},
		{ID: IDDialogCancel, Context: ContextDialog, Keys: []string{"esc"}, Help: "cancel"},

		// Pager.
		{ID: IDNextHunk, Context: ContextPager, Keys: []string{"n"}, Help: "next hunk"},
		{ID: IDPrevHunk, Context: ContextPager, Keys: []string{"N"}, Help: "previous hunk"},
		{ID: IDDialogCancel, Context: ContextPager, Keys: []string{"esc"}, Help: "close"},
	}
}

// Map indexes bindings for dispatch and help generation.
type Map struct {
	bindings []Binding
	byKey    map[Context]map[string]ID
}

// New indexes the given bindings.
func New(bindings []Binding) *Map {
	m := &Map{bindings: bindings, byKey: map[Context]map[string]ID{}}
	for _, b := range bindings {
		if m.byKey[b.Context] == nil {
			m.byKey[b.Context] = map[string]ID{}
		}
		for _, k := range b.Keys {
			m.byKey[b.Context][k] = b.ID
		}
	}
	return m
}

// Match resolves a key string within one context.
func (m *Map) Match(ctx Context, key string) (ID, bool) {
	id, ok := m.byKey[ctx][key]
	return id, ok
}

// Collisions reports keys bound to more than one ID within a context.
// A collision is a defect: the later binding silently wins at runtime.
func (m *Map) Collisions() []string {
	seen := map[Context]map[string]ID{}
	var out []string
	for _, b := range m.bindings {
		if seen[b.Context] == nil {
			seen[b.Context] = map[string]ID{}
		}
		for _, k := range b.Keys {
			if prev, dup := seen[b.Context][k]; dup && prev != b.ID {
				out = append(out, string(b.Context)+" "+k+": "+string(prev)+" and "+string(b.ID))
				continue
			}
			seen[b.Context][k] = b.ID
		}
	}
	sort.Strings(out)
	return out
}

// HelpRow is one generated help line.
type HelpRow struct {
	Context Context
	Keys    string
	Help    string
}

// Help generates the help screen from the table. It is the only help
// source, so adding a binding updates the help with no second edit.
func (m *Map) Help() []HelpRow {
	order := []Context{
		ContextGlobal, ContextComposer, ContextCompletion,
		ContextTranscript, ContextApproval, ContextDialog, ContextPager,
	}
	var rows []HelpRow
	for _, ctx := range order {
		for _, b := range m.bindings {
			if b.Context != ctx || b.Hidden {
				continue
			}
			rows = append(rows, HelpRow{Context: ctx, Keys: joinKeys(b.Keys), Help: b.Help})
		}
	}
	return rows
}

func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += " / "
		}
		if k == " " {
			k = "space"
		}
		out += k
	}
	return out
}
