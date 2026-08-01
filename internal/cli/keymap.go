package cli

// The keymap registry: one declaration of every key the TUI binds, and the
// only source /help renders from.
//
// The defect this exists to make impossible: help and bindings drifting apart.
// /help used to render the classic REPL's key list - Ctrl+U kill line, Ctrl+D
// exit, Tab completion, none of which the TUI implements - while the test
// guarding its honesty asserted on a string nothing rendered. A hand-written
// second copy of the bindings is always one edit away from lying.
//
// Precedence, highest first. A surface that owns the screen consumes every
// key, including ones bound below it:
//
//	1. sessions dialog (and its confirm prompt)
//	2. block/help/status overlay
//	3. run dashboard - only while drawn AND the transcript has focus
//	4. focus cycling, block actions, and the global chords below
//	5. focus scope: composer keys when composing, scrollback keys when reading
//	6. the focused component itself (textarea keymap / viewport keymap)
//
// Bracketed paste is deliberately not a row here: it arrives as a message
// flag, not a key string (bubbletea wraps pasted runes in "[...]" so no
// binding can match), and is routed before key dispatch - see routePastedInput.

import (
	"fmt"
	"sort"
	"strings"
)

// keyScope is where a binding applies.
type keyScope uint8

const (
	// scopeGlobal applies in chat mode regardless of which pane has focus.
	scopeGlobal keyScope = iota
	// scopeComposer applies while the composer has focus.
	scopeComposer
	// scopeSuggest applies while the slash suggestion popup is open in the
	// composer. It takes precedence over normal composer navigation.
	scopeSuggest
	// scopeScrollback applies while the transcript has focus.
	scopeScrollback
	// scopeDashboard applies while the run dashboard is drawn and the
	// transcript side has focus - it takes the arrow keys from the transcript
	// in that state, which is why it is a scope of its own.
	scopeDashboard
	// scopeOverlay applies inside the block/help/status pager.
	scopeOverlay
	// scopeSessions applies inside the /sessions manager.
	scopeSessions
	// scopeWelcome applies on the welcome screen.
	scopeWelcome
)

func (s keyScope) String() string {
	switch s {
	case scopeComposer:
		return "composer"
	case scopeSuggest:
		return "suggestions"
	case scopeScrollback:
		return "scrollback"
	case scopeDashboard:
		return "dashboard"
	case scopeOverlay:
		return "overlay"
	case scopeSessions:
		return "sessions"
	case scopeWelcome:
		return "welcome"
	default:
		return "global"
	}
}

// binding is one declared key. help == "" hides the row from /help (the key
// still exists; it is an alias or an internal affordance).
type binding struct {
	keys  []string
	scope keyScope
	help  string
	group string
}

// forbiddenKeys can never be bound: at the byte level they are other keys.
// bubbletea aliases KeyCtrlM to KeyEnter (0x0D is carriage return), KeyCtrlI
// to KeyTab (0x09) and KeyCtrlJ to newline (0x0A), so a branch on these
// strings is unreachable from a real terminal while appearing to work in
// tests - which is exactly how /help came to advertise "Ctrl+M toggle mouse"
// for a chord that actually sent the draft.
var forbiddenKeys = map[string]string{
	"ctrl+m": "0x0D is carriage return - bubbletea reports it as enter",
	"ctrl+i": "0x09 is tab - bubbletea reports it as tab",
	"ctrl+j": "0x0A is newline - indistinguishable from enter in most terminals",
	"ctrl+s": "XOFF - freezes output wherever software flow control survives raw mode",
}

// keyRegistry declares every binding the TUI honours. Order within a group is
// the order /help renders.
var keyRegistry = []binding{
	// ── Sending ──────────────────────────────────────────────────────────
	{keys: []string{"enter"}, scope: scopeComposer, group: "Sending", help: "Send message"},
	{keys: []string{"alt+enter"}, scope: scopeComposer, group: "Sending", help: "Insert newline"},

	// ── Slash suggestions ────────────────────────────────────────────────
	{keys: []string{"up", "ctrl+p"}, scope: scopeSuggest, group: "In slash suggestions", help: "Previous command"},
	{keys: []string{"down", "ctrl+n"}, scope: scopeSuggest, group: "In slash suggestions", help: "Next command"},
	{keys: []string{"tab"}, scope: scopeSuggest, group: "In slash suggestions", help: "Insert selected command"},
	{keys: []string{"enter"}, scope: scopeSuggest, group: "In slash suggestions", help: "Insert, then run eligible built-ins"},
	{keys: []string{"esc", "shift+tab"}, scope: scopeSuggest, group: "In slash suggestions", help: "Dismiss"},
	{keys: []string{"pgup", "pgdown", "home", "end"}, scope: scopeSuggest, group: "In slash suggestions", help: "Dismiss and navigate"},

	// ── Cancel & quit ────────────────────────────────────────────────────
	{keys: []string{"ctrl+c"}, scope: scopeGlobal, group: "Cancel & quit", help: "Cancel the turn · at rest: copy, clear draft, or arm quit"},
	{keys: []string{"ctrl+q"}, scope: scopeGlobal, group: "Cancel & quit", help: "Quit immediately"},

	// ── Navigation ───────────────────────────────────────────────────────
	{keys: []string{"tab", "shift+tab"}, scope: scopeGlobal, group: "Navigation", help: "Cycle composer and history blocks"},
	{keys: []string{"esc"}, scope: scopeGlobal, group: "Navigation", help: "Back to composer, clear selection"},
	{keys: []string{"pgup", "pgdown"}, scope: scopeGlobal, group: "Navigation", help: "Page the transcript"},
	{keys: []string{"home", "end"}, scope: scopeScrollback, group: "Navigation", help: "Oldest message / back to latest"},
	{keys: []string{"shift+home", "shift+end"}, scope: scopeGlobal, group: "Navigation", help: "Same from the composer (where the terminal forwards them)"},
	{keys: []string{"up", "down"}, scope: scopeScrollback, group: "Navigation", help: "Scroll line by line (the run dashboard takes these while it is open)"},
	{keys: []string{"enter", " "}, scope: scopeScrollback, group: "Navigation", help: "Expand or collapse the selected block"},
	{keys: []string{"o"}, scope: scopeScrollback, group: "Navigation", help: "Open the selected block in the pager"},
	{keys: []string{"j", "k"}, scope: scopeScrollback, group: "Navigation", help: "Scroll inside the selected work group"},
	{keys: []string{"ctrl+g"}, scope: scopeGlobal, group: "Navigation", help: "Fleet detail (subagent activity)"},

	// ── Editing ──────────────────────────────────────────────────────────
	{keys: []string{"left", "right"}, scope: scopeComposer, group: "Editing", help: "Move the cursor"},
	{keys: []string{"home", "end"}, scope: scopeComposer, group: "Editing", help: "Start / end of line"},
	{keys: []string{"ctrl+a"}, scope: scopeComposer, group: "Editing", help: "Start of line"},
	{keys: []string{"ctrl+e"}, scope: scopeComposer, group: "Editing", help: "End of line"},
	{keys: []string{"ctrl+left", "ctrl+right"}, scope: scopeComposer, group: "Editing", help: "Word back / word forward (also alt+←/→)"},
	{keys: []string{"ctrl+u", "ctrl+k"}, scope: scopeComposer, group: "Editing", help: "Delete to line start / to line end"},
	{keys: []string{"ctrl+w", "alt+backspace"}, scope: scopeComposer, group: "Editing", help: "Delete the word before the cursor"},
	{keys: []string{"ctrl+v"}, scope: scopeGlobal, group: "Editing", help: "Paste into the composer (the terminal's own paste also works)"},

	// ── Copying ──────────────────────────────────────────────────────────
	{keys: []string{"y"}, scope: scopeScrollback, group: "Copying", help: "Copy the selected message"},
	{keys: []string{"ctrl+y"}, scope: scopeGlobal, group: "Copying", help: "Copy the selected message (any focus)"},
	{keys: []string{"f2"}, scope: scopeGlobal, group: "Copying", help: "Select mode: hand the mouse back to the terminal (also /select)"},

	// ── Panels ───────────────────────────────────────────────────────────
	{keys: []string{"ctrl+t"}, scope: scopeGlobal, group: "Panels", help: "Toggle live thinking"},
	{keys: []string{"ctrl+r"}, scope: scopeGlobal, group: "Panels", help: "Toggle the run dashboard"},
	{keys: []string{"ctrl+l"}, scope: scopeGlobal, group: "Panels", help: "Clear the screen"},

	// ── Run dashboard (drawn, transcript focused) ────────────────────────
	{keys: []string{"up", "down"}, scope: scopeDashboard, group: "In the run dashboard", help: "Move the run cursor"},

	// ── Overlay / dialogs ────────────────────────────────────────────────
	{keys: []string{"esc", "q"}, scope: scopeOverlay, group: "In a dialog", help: "Close"},
	{keys: []string{"j", "k", "up", "down"}, scope: scopeOverlay, group: "In a dialog", help: "Scroll"},
	{keys: []string{"pgup", "pgdown", "b", "f", " "}, scope: scopeOverlay, group: "In a dialog", help: "Page"},
	{keys: []string{"g", "G", "home", "end"}, scope: scopeOverlay, group: "In a dialog", help: "Top / bottom"},

	// ── Sessions manager ─────────────────────────────────────────────────
	{keys: []string{"up", "down", "j", "k"}, scope: scopeSessions, group: "In /sessions", help: "Move"},
	{keys: []string{"enter"}, scope: scopeSessions, group: "In /sessions", help: "Open the selected session"},
	{keys: []string{"d"}, scope: scopeSessions, group: "In /sessions", help: "Delete (confirm with y)"},
	{keys: []string{"P"}, scope: scopeSessions, group: "In /sessions", help: "Purge all (confirm with y)"},
	{keys: []string{"home", "g"}, scope: scopeSessions, group: "In /sessions", help: "First session"},
	{keys: []string{"end", "G"}, scope: scopeSessions, group: "In /sessions", help: "Last session"},
	{keys: []string{"y", "n"}, scope: scopeSessions, group: "In /sessions", help: "Confirm / cancel a delete or purge"},
	{keys: []string{"esc", "q"}, scope: scopeSessions, group: "In /sessions", help: "Close"},

	// ── Welcome screen ───────────────────────────────────────────────────
	{keys: []string{"up", "down", "j", "k"}, scope: scopeWelcome, group: "On the welcome screen", help: "Pick a session (j/k only while the composer is empty)"},
	{keys: []string{"pgup", "pgdown", "home", "end"}, scope: scopeWelcome, group: "On the welcome screen", help: "Jump through the session list"},
	{keys: []string{"ctrl+o"}, scope: scopeWelcome, group: "On the welcome screen", help: "Continue the last session"},
	{keys: []string{"enter"}, scope: scopeWelcome, group: "On the welcome screen", help: "Open the selected session, or start typing for a new one"},
}

// validateKeyRegistry reports every structural problem with the registry: a
// key bound twice in one scope, a forbidden alias, or a row with no help and
// no reason to be hidden. Returns nil when the registry is sound.
func validateKeyRegistry(rs []binding) []error {
	var errs []error
	seen := map[string]string{} // "scope/key" -> group it was first seen in
	for _, b := range rs {
		if len(b.keys) == 0 {
			errs = append(errs, fmt.Errorf("binding in group %q declares no keys", b.group))
		}
		if b.group == "" {
			errs = append(errs, fmt.Errorf("binding %v declares no group", b.keys))
		}
		if b.help == "" {
			errs = append(errs, fmt.Errorf("binding %v (%s) has no help text", b.keys, b.scope))
		}
		for _, k := range b.keys {
			if why, bad := forbiddenKeys[k]; bad {
				errs = append(errs, fmt.Errorf("binding %q must not be bound: %s", k, why))
			}
			id := b.scope.String() + "/" + k
			if prev, dup := seen[id]; dup {
				errs = append(errs, fmt.Errorf("key %q is bound twice in scope %s (%s and %s)", k, b.scope, prev, b.group))
			}
			seen[id] = b.group
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}

// keyLabel renders a binding's keys the way /help shows them.
func keyLabel(b binding) string {
	pretty := make([]string, 0, len(b.keys))
	for _, k := range b.keys {
		pretty = append(pretty, prettyKeyName(k))
	}
	return strings.Join(pretty, " / ")
}

// prettyKeyName turns a bubbletea key string into its help spelling.
func prettyKeyName(k string) string {
	switch k {
	case " ":
		return "Space"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case "ctrl+left":
		return "Ctrl+←"
	case "ctrl+right":
		return "Ctrl+→"
	case "pgup":
		return "PgUp"
	case "pgdown":
		return "PgDn"
	}
	parts := strings.Split(k, "+")
	for i, p := range parts {
		switch p {
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		default:
			// Single letters are shown as the user types them ("y", "G"), but
			// a chord's letter is capitalised ("Ctrl+R") the way keycaps read.
			if len(parts) > 1 && p != "" {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			} else if len(p) > 1 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
	}
	return strings.Join(parts, "+")
}

// keyHelpSections renders the registry as help content, preserving the
// declaration order of both groups and rows.
func keyHelpSections(rs []binding) []helpSection {
	var order []string
	byGroup := map[string][]helpItem{}
	for _, b := range rs {
		if b.help == "" {
			continue
		}
		if _, ok := byGroup[b.group]; !ok {
			order = append(order, b.group)
		}
		byGroup[b.group] = append(byGroup[b.group], helpItem{key: keyLabel(b), desc: b.help})
	}
	sections := make([]helpSection, 0, len(order))
	for _, g := range order {
		sections = append(sections, helpSection{title: g, items: byGroup[g]})
	}
	return sections
}
