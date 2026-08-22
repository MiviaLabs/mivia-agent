package clichat

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
//	2. block/help/status overlay, and the queue manager popup (both own
//	   every key while open)
//	3. run dashboard - only while drawn AND the transcript has focus
//	4. focus cycling, block actions, and the global chords below
//	5. focus Scope: composer keys when composing, scrollback keys when reading
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
//
// KeyScope is the exported alias, for internal/legacytui's routing tests.
type KeyScope = keyScope
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
	// scopeWorkflows applies inside the /workflows manager.
	scopeWorkflows
	// scopeWelcome applies on the welcome screen.
	scopeWelcome
	// scopeHistory applies while the composer message-history picker is open.
	scopeHistory
	// scopeQueue applies while the queue manager popup is open (modal).
	scopeQueue
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
	case scopeWorkflows:
		return "workflows"
	case scopeWelcome:
		return "welcome"
	case scopeHistory:
		return "history"
	case scopeQueue:
		return "queue"
	default:
		return "global"
	}
}

// binding is one declared key. help == "" hides the row from /help (the key
// still exists; it is an alias or an internal affordance).
//
// Binding is the exported alias, for internal/legacytui's routing tests.
type Binding = binding
type binding struct {
	Keys  []string
	Scope keyScope
	Help  string
	Group string
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
	{Keys: []string{"enter"}, Scope: scopeComposer, Group: "Sending", Help: "Send message"},
	{Keys: []string{"alt+enter"}, Scope: scopeComposer, Group: "Sending", Help: "Insert newline"},
	{Keys: []string{"ctrl+up"}, Scope: scopeComposer, Group: "Sending", Help: "Open the message queue manager"},

	// ── Slash suggestions ────────────────────────────────────────────────
	{Keys: []string{"up", "ctrl+p"}, Scope: scopeSuggest, Group: "In slash suggestions", Help: "Previous command"},
	{Keys: []string{"down", "ctrl+n"}, Scope: scopeSuggest, Group: "In slash suggestions", Help: "Next command"},
	{Keys: []string{"tab"}, Scope: scopeSuggest, Group: "In slash suggestions", Help: "Insert selected command"},
	{Keys: []string{"enter"}, Scope: scopeSuggest, Group: "In slash suggestions", Help: "Insert, then run eligible built-ins"},
	{Keys: []string{"esc", "shift+tab"}, Scope: scopeSuggest, Group: "In slash suggestions", Help: "Dismiss"},
	{Keys: []string{"pgup", "pgdown", "home", "end"}, Scope: scopeSuggest, Group: "In slash suggestions", Help: "Dismiss and navigate"},

	// ── Message history picker ───────────────────────────────────────────
	{Keys: []string{"up", "ctrl+p"}, Scope: scopeHistory, Group: "In message history", Help: "Previous message"},
	{Keys: []string{"down", "ctrl+n"}, Scope: scopeHistory, Group: "In message history", Help: "Next message"},
	{Keys: []string{"enter", "tab"}, Scope: scopeHistory, Group: "In message history", Help: "Recall selected message"},
	{Keys: []string{"esc", "shift+tab"}, Scope: scopeHistory, Group: "In message history", Help: "Dismiss"},

	// ── Message queue manager (modal popup) ───────────────────────────────
	{Keys: []string{"up", "ctrl+p"}, Scope: scopeQueue, Group: "In message queue", Help: "Previous message"},
	{Keys: []string{"down", "ctrl+n"}, Scope: scopeQueue, Group: "In message queue", Help: "Next message"},
	{Keys: []string{"enter"}, Scope: scopeQueue, Group: "In message queue", Help: "Send the selected message now"},
	{Keys: []string{"d"}, Scope: scopeQueue, Group: "In message queue", Help: "Delete the selected message"},
	{Keys: []string{"e"}, Scope: scopeQueue, Group: "In message queue", Help: "Edit the selected message"},
	{Keys: []string{"esc", "q"}, Scope: scopeQueue, Group: "In message queue", Help: "Close"},

	// ── Cancel & quit ────────────────────────────────────────────────────
	{Keys: []string{"ctrl+c"}, Scope: scopeGlobal, Group: "Cancel & quit", Help: "Cancel the turn · at rest: copy, clear draft, or arm quit"},
	{Keys: []string{"ctrl+q"}, Scope: scopeGlobal, Group: "Cancel & quit", Help: "Quit immediately"},

	// ── Navigation ───────────────────────────────────────────────────────
	{Keys: []string{"tab", "shift+tab"}, Scope: scopeGlobal, Group: "Navigation", Help: "Cycle composer and history blocks"},
	{Keys: []string{"esc"}, Scope: scopeGlobal, Group: "Navigation", Help: "Back to composer, clear selection"},
	{Keys: []string{"pgup", "pgdown"}, Scope: scopeGlobal, Group: "Navigation", Help: "Page the transcript"},
	{Keys: []string{"home", "end"}, Scope: scopeScrollback, Group: "Navigation", Help: "Oldest message / back to latest"},
	{Keys: []string{"shift+home", "shift+end"}, Scope: scopeGlobal, Group: "Navigation", Help: "Same from the composer (where the terminal forwards them)"},
	{Keys: []string{"up", "down"}, Scope: scopeScrollback, Group: "Navigation", Help: "Scroll line by line (the run dashboard takes these while it is open)"},
	{Keys: []string{"enter", " "}, Scope: scopeScrollback, Group: "Navigation", Help: "Expand or collapse the selected block"},
	{Keys: []string{"o"}, Scope: scopeScrollback, Group: "Navigation", Help: "Open the selected block in the pager"},
	{Keys: []string{"j", "k"}, Scope: scopeScrollback, Group: "Navigation", Help: "Scroll inside the selected work group"},
	{Keys: []string{"ctrl+g"}, Scope: scopeGlobal, Group: "Navigation", Help: "Fleet detail (subagent activity)"},

	// ── Editing ──────────────────────────────────────────────────────────
	{Keys: []string{"left", "right"}, Scope: scopeComposer, Group: "Editing", Help: "Move the cursor"},
	{Keys: []string{"home", "end"}, Scope: scopeComposer, Group: "Editing", Help: "Start / end of line"},
	{Keys: []string{"ctrl+a"}, Scope: scopeComposer, Group: "Editing", Help: "Start of line"},
	{Keys: []string{"ctrl+e"}, Scope: scopeComposer, Group: "Editing", Help: "End of line"},
	{Keys: []string{"ctrl+left", "ctrl+right"}, Scope: scopeComposer, Group: "Editing", Help: "Word back / word forward (also alt+←/→)"},
	{Keys: []string{"ctrl+u", "ctrl+k"}, Scope: scopeComposer, Group: "Editing", Help: "Delete to line start / to line end"},
	{Keys: []string{"ctrl+w", "alt+backspace"}, Scope: scopeComposer, Group: "Editing", Help: "Delete the word before the cursor"},
	{Keys: []string{"ctrl+v"}, Scope: scopeGlobal, Group: "Editing", Help: "Paste into the composer (the terminal's own paste also works)"},

	// ── Copying ──────────────────────────────────────────────────────────
	{Keys: []string{"y"}, Scope: scopeScrollback, Group: "Copying", Help: "Copy the selected message"},
	{Keys: []string{"ctrl+y"}, Scope: scopeGlobal, Group: "Copying", Help: "Copy the selected message (any focus)"},
	{Keys: []string{"f2"}, Scope: scopeGlobal, Group: "Copying", Help: "Select mode: hand the mouse back to the terminal (also /select)"},

	// ── Panels ───────────────────────────────────────────────────────────
	{Keys: []string{"ctrl+t"}, Scope: scopeGlobal, Group: "Panels", Help: "Toggle live thinking"},
	{Keys: []string{"ctrl+r"}, Scope: scopeGlobal, Group: "Panels", Help: "Toggle the run dashboard"},
	{Keys: []string{"ctrl+l"}, Scope: scopeGlobal, Group: "Panels", Help: "Clear the screen"},

	// ── Run dashboard (drawn, transcript focused) ────────────────────────
	{Keys: []string{"up", "down"}, Scope: scopeDashboard, Group: "In the run dashboard", Help: "Move the run cursor"},

	// ── Overlay / dialogs ────────────────────────────────────────────────
	{Keys: []string{"esc", "q"}, Scope: scopeOverlay, Group: "In a dialog", Help: "Close"},
	{Keys: []string{"j", "k", "up", "down"}, Scope: scopeOverlay, Group: "In a dialog", Help: "Scroll"},
	{Keys: []string{"pgup", "pgdown", "b", "f", " "}, Scope: scopeOverlay, Group: "In a dialog", Help: "Page"},
	{Keys: []string{"g", "G", "home", "end"}, Scope: scopeOverlay, Group: "In a dialog", Help: "Top / bottom"},

	// ── Sessions manager ─────────────────────────────────────────────────
	{Keys: []string{"up", "down", "j", "k"}, Scope: scopeSessions, Group: "In /sessions", Help: "Move"},
	{Keys: []string{"enter"}, Scope: scopeSessions, Group: "In /sessions", Help: "Open the selected session"},
	{Keys: []string{"d"}, Scope: scopeSessions, Group: "In /sessions", Help: "Delete (confirm with y)"},
	{Keys: []string{"P"}, Scope: scopeSessions, Group: "In /sessions", Help: "Purge all (confirm with y)"},
	{Keys: []string{"home", "g"}, Scope: scopeSessions, Group: "In /sessions", Help: "First session"},
	{Keys: []string{"end", "G"}, Scope: scopeSessions, Group: "In /sessions", Help: "Last session"},
	{Keys: []string{"y", "n"}, Scope: scopeSessions, Group: "In /sessions", Help: "Confirm / cancel a delete or purge"},
	{Keys: []string{"esc", "q"}, Scope: scopeSessions, Group: "In /sessions", Help: "Close"},

	// ── Workflows manager ────────────────────────────────────────────────
	{Keys: []string{"up", "down", "j", "k"}, Scope: scopeWorkflows, Group: "In /workflows", Help: "Move"},
	{Keys: []string{"enter"}, Scope: scopeWorkflows, Group: "In /workflows", Help: "Print the run id and status"},
	{Keys: []string{"esc"}, Scope: scopeWorkflows, Group: "In /workflows", Help: "Close"},

	// ── Worktrees manager (shares navigation, enter, d, y/n, esc with /sessions) ─
	{Keys: []string{"b"}, Scope: scopeSessions, Group: "In /worktrees", Help: "Switch to main tree"},
	{Keys: []string{"c"}, Scope: scopeSessions, Group: "In /worktrees", Help: "Create new worktree"},

	// ── Welcome screen ───────────────────────────────────────────────────
	{Keys: []string{"up", "down", "j", "k"}, Scope: scopeWelcome, Group: "On the welcome screen", Help: "Pick a session (j/k only while the composer is empty)"},
	{Keys: []string{"pgup", "pgdown", "home", "end"}, Scope: scopeWelcome, Group: "On the welcome screen", Help: "Jump through the session list"},
	{Keys: []string{"ctrl+o"}, Scope: scopeWelcome, Group: "On the welcome screen", Help: "Continue the last session"},
	{Keys: []string{"enter"}, Scope: scopeWelcome, Group: "On the welcome screen", Help: "Open the selected session, or start typing for a new one"},
}

// validateKeyRegistry reports every structural problem with the registry: a
// key bound twice in one scope, a forbidden alias, or a row with no help and
// no reason to be hidden. Returns nil when the registry is sound.
func validateKeyRegistry(rs []binding) []error {
	var errs []error
	seen := map[string]string{} // "scope/key" -> group it was first seen in
	for _, b := range rs {
		if len(b.Keys) == 0 {
			errs = append(errs, fmt.Errorf("binding in group %q declares no keys", b.Group))
		}
		if b.Group == "" {
			errs = append(errs, fmt.Errorf("binding %v declares no group", b.Keys))
		}
		if b.Help == "" {
			errs = append(errs, fmt.Errorf("binding %v (%s) has no help text", b.Keys, b.Scope))
		}
		for _, k := range b.Keys {
			if why, bad := forbiddenKeys[k]; bad {
				errs = append(errs, fmt.Errorf("binding %q must not be bound: %s", k, why))
			}
			id := b.Scope.String() + "/" + k
			if prev, dup := seen[id]; dup {
				errs = append(errs, fmt.Errorf("key %q is bound twice in scope %s (%s and %s)", k, b.Scope, prev, b.Group))
			}
			seen[id] = b.Group
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}

// keyLabel renders a binding's keys the way /help shows them.
func keyLabel(b binding) string {
	pretty := make([]string, 0, len(b.Keys))
	for _, k := range b.Keys {
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
		if b.Help == "" {
			continue
		}
		if _, ok := byGroup[b.Group]; !ok {
			order = append(order, b.Group)
		}
		byGroup[b.Group] = append(byGroup[b.Group], helpItem{Key: keyLabel(b), Desc: b.Help})
	}
	sections := make([]helpSection, 0, len(order))
	for _, g := range order {
		sections = append(sections, helpSection{Title: g, Items: byGroup[g]})
	}
	return sections
}
