// Package keymap is the keymap as data, not as code. One table yields
// the runtime dispatch, the generated help screen, and the collision
// test. Build spec section 4.6 requires this: a switch statement cannot
// generate its own help, and the two drift.
//
// This package lives under internal/uikit, so it must not import
// bubbletea. It deals in key strings, which is what
// bubbletea's Key.String() produces; the view layer does the conversion.
package keymap

import (
	"sort"
	"strings"
)

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
	// ContextFiles applies while the files panel's list holds focus.
	ContextFiles Context = "files"
	// ContextSettings applies inside the full-screen settings modal. It
	// does not cascade from ContextGlobal - like ContextPager, the
	// settings screen is a self-contained modal that consults only its
	// own context (docs/design/settings-screen.md §6).
	ContextSettings Context = "settings"
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
	IDScrollUp      ID = "scroll-up"
	IDScrollDown    ID = "scroll-down"
	IDScrollTop     ID = "scroll-top"
	IDScrollBottom  ID = "scroll-bottom"
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

	// Transcript mode (the pager). One ID per less-compatible action, so
	// the help screen names every key the pager answers to
	// (docs/design/cockpit-research.md rule 6.2).
	IDSearchStart    ID = "search-start"
	IDSearchNext     ID = "search-next"
	IDSearchPrev     ID = "search-prev"
	IDPagerRowUp     ID = "pager-row-up"
	IDPagerRowDown   ID = "pager-row-down"
	IDPagerTop       ID = "pager-top"
	IDPagerBottom    ID = "pager-bottom"
	IDPagerPromptUp  ID = "pager-prompt-up"
	IDPagerPromptDn  ID = "pager-prompt-down"
	IDPagerHalfUp    ID = "pager-half-up"
	IDPagerHalfDown  ID = "pager-half-down"
	IDPagerFullUp    ID = "pager-full-up"
	IDPagerFullDown  ID = "pager-full-down"
	IDLeavePager     ID = "leave-pager"
	IDDumpScrollback ID = "dump-scrollback"
	IDEditTranscript ID = "edit-transcript"

	// Files panel (the touched-files pane beside the conversation).
	IDPanelToggle    ID = "panel-toggle"
	IDFileToggleView ID = "file-toggle-view"
	IDFileOpen       ID = "file-open"

	// Universal Command Palette (Ctrl+P / Ctrl+X).
	IDPalette ID = "command-palette"

	// Queue manager overlay (Ctrl+Up).
	IDQueueDialog ID = "queue-dialog"

	// Settings screen. IDSettingsDialog is the global key that opens it
	// (f2 - see docs/design/settings-screen.md §6 for why not ctrl+g);
	// the rest are ContextSettings-scoped.
	IDSettingsDialog    ID = "settings-dialog"
	IDSettingsUp        ID = "settings-up"
	IDSettingsDown      ID = "settings-down"
	IDSettingsPaneLeft  ID = "settings-pane-left"
	IDSettingsPaneRight ID = "settings-pane-right"
	IDSettingsSelect    ID = "settings-select"
	IDSettingsNew       ID = "settings-new"
	IDSettingsDelete    ID = "settings-delete"
	IDSettingsToggle    ID = "settings-toggle"
	IDSettingsTrigger   ID = "settings-trigger"
	IDSettingsFilter    ID = "settings-filter"
	IDSettingsReveal    ID = "settings-reveal"
	IDSettingsBack      ID = "settings-back"
	IDSettingsHelp      ID = "settings-help"

	// Blackboard & agent messaging center.
	IDBlackboardDialog ID = "blackboard-dialog"
)

// Binding is one row of the table.
type Binding struct {
	ID      ID
	Context Context
	Keys    []string
	Help    string
	// Short is the one-word label the compact footer hint uses. A binding
	// without one never appears in a hint; the full Help text is unchanged.
	Short string
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
	return append(append([]Binding{
		// Global.
		{ID: IDCancel, Context: ContextGlobal, Keys: []string{"esc"}, Help: "cancel the turn, keep the text"},
		{ID: IDQuit, Context: ContextGlobal, Keys: []string{"ctrl+c"}, Help: "cancel; press twice to quit", Short: "quit"},
		{ID: IDThemeDialog, Context: ContextGlobal, Keys: []string{"ctrl+t"}, Help: "theme"},
		{ID: IDOpenPager, Context: ContextGlobal, Keys: []string{"ctrl+o"}, Help: "open the pager", Short: "transcript"},
		{ID: IDToggleReason, Context: ContextGlobal, Keys: []string{"ctrl+r"}, Help: "show or hide reasoning"},
		{ID: IDPalette, Context: ContextGlobal, Keys: []string{"ctrl+p", "ctrl+x"}, Help: "command palette", Short: "palette"},
		{ID: IDQueueDialog, Context: ContextGlobal, Keys: []string{"ctrl+up"}, Help: "queue manager", Short: "queue"},
		// ctrl+b drives the files panel: open it focused, hand focus
		// back to the composer, close it.
		{ID: IDPanelToggle, Context: ContextGlobal, Keys: []string{"ctrl+b"}, Help: "open, focus, or close the sidebar", Short: "sidebar"},
		// f2, not ctrl+g: ctrl+g is already IDCollapseAll in
		// ContextTranscript and would only be PARTIALLY free (rule
		// 1.4 forbids a hint that is not true in every state); f2 is
		// unbound everywhere and rule 1.1 permits function keys.
		{ID: IDSettingsDialog, Context: ContextGlobal, Keys: []string{"f2"}, Help: "settings", Short: "settings"},
		{ID: IDBlackboardDialog, Context: ContextGlobal, Keys: []string{"f3"}, Help: "blackboard & agent messages", Short: "blackboard"},

		// Scrolling. The cockpit owns the surface, so the application
		// scrolls: the terminal has no scrollback of its own to offer
		// (docs/design/cockpit-research.md section 3).
		{ID: IDScrollUp, Context: ContextGlobal, Keys: []string{"pgup"}, Help: "scroll up half a screen"},
		{ID: IDScrollDown, Context: ContextGlobal, Keys: []string{"pgdown"}, Help: "scroll down half a screen"},
		{ID: IDScrollTop, Context: ContextGlobal, Keys: []string{"ctrl+home"}, Help: "jump to the start"},
		{ID: IDScrollBottom, Context: ContextGlobal, Keys: []string{"ctrl+end"}, Help: "jump to the newest output"},

		// Composer.
		{ID: IDSend, Context: ContextComposer, Keys: []string{"enter"}, Help: "send"},
		{ID: IDNewline, Context: ContextComposer, Keys: []string{"shift+enter", "alt+enter"}, Help: "newline"},
		{ID: IDClearLine, Context: ContextComposer, Keys: []string{"ctrl+u"}, Help: "clear the line"},
		{ID: IDHelp, Context: ContextComposer, Keys: []string{"?"}, Help: "show this keymap (empty composer)", Short: "help"},
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
		// "space", not " ": bubbletea/v2 Key.String reports the space bar
		// as the word. A literal " " here silently never matches.
		{ID: IDToggleBlock, Context: ContextTranscript, Keys: []string{"space", "enter"}, Help: "collapse or expand"},
		{ID: IDExpandAll, Context: ContextTranscript, Keys: []string{"ctrl+e"}, Help: "expand all"},
		{ID: IDCollapseAll, Context: ContextTranscript, Keys: []string{"ctrl+g"}, Help: "collapse all"},
		{ID: IDCopyBlock, Context: ContextTranscript, Keys: []string{"y"}, Help: "copy the block"},
		{ID: IDCancel, Context: ContextTranscript, Keys: []string{"esc"}, Help: "return to the composer"},

		// Approval. wireframes-panes.md section 7. up/down (and the less
		// spellings k/j) scroll the inline diff preview; the decision keys
		// are letters the arrows never collide with.
		{ID: IDApproveOnce, Context: ContextApproval, Keys: []string{"o", "enter"}, Help: "once"},
		{ID: IDApproveAlways, Context: ContextApproval, Keys: []string{"a"}, Help: "always"},
		{ID: IDDenyOnce, Context: ContextApproval, Keys: []string{"d", "esc"}, Help: "deny"},
		{ID: IDDenyAlways, Context: ContextApproval, Keys: []string{"D"}, Help: "deny always"},
		{ID: IDDenyAlways, Context: ContextApproval, Keys: []string{"shift+d"}, Hidden: true},
		{ID: IDScrollUp, Context: ContextApproval, Keys: []string{"up", "k"}, Help: "scroll the diff preview one line up"},
		{ID: IDScrollDown, Context: ContextApproval, Keys: []string{"down", "j"}, Help: "scroll the diff preview one line down"},

		// Dialogs.
		{ID: IDDialogUp, Context: ContextDialog, Keys: []string{"up"}, Help: "previous"},
		{ID: IDDialogDown, Context: ContextDialog, Keys: []string{"down"}, Help: "next"},
		{ID: IDDialogAccept, Context: ContextDialog, Keys: []string{"enter"}, Help: "apply"},
		{ID: IDDialogCancel, Context: ContextDialog, Keys: []string{"esc"}, Help: "cancel"},
	}, pagerBindings()...), append(filesBindings(), settingsBindings()...)...)
}

// settingsBindings is the full-screen settings modal's section. It does
// not cascade to ContextGlobal (see ContextSettings's doc comment), so
// every key the screen answers to - including "back" - is bound here,
// the same self-contained shape ContextPager already uses.
func settingsBindings() []Binding {
	return []Binding{
		{ID: IDSettingsUp, Context: ContextSettings, Keys: []string{"up", "k"}, Help: "previous"},
		{ID: IDSettingsDown, Context: ContextSettings, Keys: []string{"down", "j"}, Help: "next"},
		{ID: IDSettingsPaneLeft, Context: ContextSettings, Keys: []string{"left", "h", "shift+tab"}, Help: "focus the section list"},
		{ID: IDSettingsPaneRight, Context: ContextSettings, Keys: []string{"right", "l", "tab"}, Help: "focus the detail pane"},
		{ID: IDSettingsSelect, Context: ContextSettings, Keys: []string{"enter"}, Help: "open or edit", Short: "edit"},
		{ID: IDSettingsNew, Context: ContextSettings, Keys: []string{"n"}, Help: "new", Short: "new"},
		{ID: IDSettingsDelete, Context: ContextSettings, Keys: []string{"x"}, Help: "delete", Short: "delete"},
		{ID: IDSettingsToggle, Context: ContextSettings, Keys: []string{"space"}, Help: "toggle enabled"},
		// Automations-only today: fires a manual run and opens a live
		// watch on it. Harmless no-op on any other section (their
		// handleKey switches do not have a "t" case).
		{ID: IDSettingsTrigger, Context: ContextSettings, Keys: []string{"t"}, Help: "trigger a manual run", Short: "trigger"},
		{ID: IDSettingsFilter, Context: ContextSettings, Keys: []string{"/"}, Help: "filter", Short: "filter"},
		{ID: IDSettingsReveal, Context: ContextSettings, Keys: []string{"ctrl+r"}, Help: "reveal the focused secret value"},
		{ID: IDSettingsBack, Context: ContextSettings, Keys: []string{"esc"}, Help: "back", Short: "back"},
		{ID: IDSettingsHelp, Context: ContextSettings, Keys: []string{"?"}, Help: "show this keymap", Short: "help"},
	}
}

// pagerBindings is the pager (transcript mode) section of Default,
// split out only to keep Default inside the function-size budget. Keys
// follow less, because less is the muscle memory every terminal user
// already has (cockpit-research.md rule 6.2). ctrl+u and ctrl+d are
// readline keys: readline owns the line editor, and a pager is not one.
// less itself binds ctrl+d as half a page down. ctrl+s stays unbound -
// it is XOFF in any context (ux-rules.md rule 1.2).
func pagerBindings() []Binding {
	return []Binding{
		{ID: IDSearchStart, Context: ContextPager, Keys: []string{"/"}, Help: "search the conversation", Short: "search"},
		{ID: IDSearchNext, Context: ContextPager, Keys: []string{"n"}, Help: "next match"},
		{ID: IDSearchPrev, Context: ContextPager, Keys: []string{"N"}, Help: "previous match"},
		{ID: IDPagerRowUp, Context: ContextPager, Keys: []string{"k", "up"}, Help: "one row up"},
		{ID: IDPagerRowDown, Context: ContextPager, Keys: []string{"j", "down"}, Help: "one row down"},
		{ID: IDPagerTop, Context: ContextPager, Keys: []string{"g", "home"}, Help: "jump to the start"},
		{ID: IDPagerBottom, Context: ContextPager, Keys: []string{"G", "end"}, Help: "jump to the newest output"},
		{ID: IDPagerPromptUp, Context: ContextPager, Keys: []string{"{"}, Help: "previous user prompt"},
		{ID: IDPagerPromptDn, Context: ContextPager, Keys: []string{"}"}, Help: "next user prompt"},
		{ID: IDPagerHalfUp, Context: ContextPager, Keys: []string{"ctrl+u"}, Help: "half page up"},
		{ID: IDPagerHalfDown, Context: ContextPager, Keys: []string{"ctrl+d"}, Help: "half page down"},
		{ID: IDPagerFullUp, Context: ContextPager, Keys: []string{"ctrl+b", "b"}, Help: "full page up"},
		{ID: IDPagerFullDown, Context: ContextPager, Keys: []string{"ctrl+f", "space"}, Help: "full page down"},
		{ID: IDLeavePager, Context: ContextPager, Keys: []string{"ctrl+o", "esc", "q"}, Help: "return to the composer", Short: "back"},
		{ID: IDDumpScrollback, Context: ContextPager, Keys: []string{"["}, Help: "write the transcript to terminal scrollback", Short: "scrollback"},
		{ID: IDEditTranscript, Context: ContextPager, Keys: []string{"v"}, Help: "open the transcript in $VISUAL or $EDITOR", Short: "editor"},
	}
}

// filesBindings is the files panel's section. The panel is a pane
// beside the conversation, not a tab takeover: arrows and the less
// spellings move the LIST selection while the panel holds focus, d
// flips the content dialog between diff and source, ctrl+d/ctrl+u
// scroll that content by half pages, and esc peels a filter away
// before it hands focus back to the composer. Enter and plain typing
// are the picker's own handling (select, filter), documented here so
// the generated help states them.
func filesBindings() []Binding {
	return []Binding{
		{ID: IDPagerRowUp, Context: ContextFiles, Keys: []string{"up", "k"}, Help: "previous file"},
		{ID: IDPagerRowDown, Context: ContextFiles, Keys: []string{"down", "j"}, Help: "next file"},
		{ID: IDFileOpen, Context: ContextFiles, Keys: []string{"enter"}, Help: "open the diff (typing filters the list)"},
		{ID: IDPagerHalfDown, Context: ContextFiles, Keys: []string{"ctrl+d"}, Help: "scroll the content half a page down"},
		{ID: IDPagerHalfUp, Context: ContextFiles, Keys: []string{"ctrl+u"}, Help: "scroll the content half a page up"},
		{ID: IDFileToggleView, Context: ContextFiles, Keys: []string{"d"}, Help: "diff or source (in the dialog)", Short: "diff"},
		{ID: IDCancel, Context: ContextFiles, Keys: []string{"esc"}, Help: "clear the filter, then return to the composer"},
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
		ContextFiles, ContextSettings,
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

// Hint renders a compact one-line key hint ("? help  ctrl+o transcript")
// from the same table that feeds Help, so the persistent footer hint and
// the help screen cannot drift. Each named ID uses its first key and its
// Short label; an ID with no Short label is skipped, and an unknown ID is
// skipped rather than reported: hints are chrome, not dispatch.
func (m *Map) Hint(ids ...ID) string {
	var parts []string
	for _, want := range ids {
		for _, b := range m.bindings {
			if b.ID != want || b.Hidden || b.Short == "" {
				continue
			}
			parts = append(parts, b.Keys[0]+":"+b.Short)
			break
		}
	}
	return strings.Join(parts, "  ")
}

func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += " / "
		}
		out += k
	}
	return out
}
