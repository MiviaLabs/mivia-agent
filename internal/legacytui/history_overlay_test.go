package legacytui

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"reflect"
	"testing"
)

// equalStrings reports whether two string slices are deeply equal.
func equalStrings(a, b []string) bool {
	return reflect.DeepEqual(a, b)
}

func TestHistoryOverlaySentHistoryAppend(t *testing.T) {
	m := newReadyChatModel(24, 80)

	m.appendSentHistory("hello")
	m.appendSentHistory("world")
	want := []string{"hello", "world"}
	if !equalStrings(m.sentHistory, want) {
		t.Fatalf("appendSentHistory: expected %v, got %v", want, m.sentHistory)
	}

	// Empty strings are skipped: length must not change.
	before := len(m.sentHistory)
	m.appendSentHistory("")
	if len(m.sentHistory) != before {
		t.Fatalf("appendSentHistory: empty string should be skipped; length changed from %d to %d", before, len(m.sentHistory))
	}
}

func TestHistoryOverlaySentHistoryDedupeLast(t *testing.T) {
	m := newReadyChatModel(24, 80)

	for _, s := range []string{"a", "a", "b", "a"} {
		m.appendSentHistory(s)
	}
	want := []string{"a", "b", "a"} // only the immediately previous entry is deduped
	if !equalStrings(m.sentHistory, want) {
		t.Fatalf("appendSentHistory dedupe: expected %v, got %v", want, m.sentHistory)
	}
}

func TestHistoryOverlaySentHistoryCap(t *testing.T) {
	m := newReadyChatModel(24, 80)

	for i := 0; i < cli.MaxHistorySize+5; i++ {
		m.appendSentHistory(fmt.Sprintf("entry-%d", i))
	}
	if len(m.sentHistory) != cli.MaxHistorySize {
		t.Fatalf("appendSentHistory cap: expected len == %d, got %d", cli.MaxHistorySize, len(m.sentHistory))
	}
	if len(m.sentHistory) > 0 && m.sentHistory[0] != "entry-5" {
		t.Fatalf("appendSentHistory cap: expected oldest entries trimmed so sentHistory[0] == %q, got %q", "entry-5", m.sentHistory[0])
	}
}

func TestHistoryOverlayEntriesNewestFirst(t *testing.T) {
	m := newReadyChatModel(24, 80)

	// Empty sentHistory yields empty entries.
	if got := m.historyEntries(); len(got) != 0 {
		t.Fatalf("historyEntries: expected empty entries for empty sentHistory, got %v", got)
	}

	m.sentHistory = []string{"first", "second", "third"}
	want := []string{"third", "second", "first"}
	if got := m.historyEntries(); !equalStrings(got, want) {
		t.Fatalf("historyEntries: expected %v, got %v", want, got)
	}
}

func TestHistoryOverlayOpenClose(t *testing.T) {
	m := newReadyChatModel(24, 80)

	m.openHistory()
	if !m.history.Open || m.history.Selected != 0 {
		t.Fatalf("openHistory: expected open==true && selected==0, got open=%v selected=%d", m.history.Open, m.history.Selected)
	}

	m.closeHistory()
	if m.history.Open || m.history.Selected != 0 {
		t.Fatalf("closeHistory: expected open==false && selected==0, got open=%v selected=%d", m.history.Open, m.history.Selected)
	}
}

// ---------------------------------------------------------------------------
// Wave 3 (RED): handleHistoryKey key matrix.
//
// Contract being locked in:
//   - Trigger ('up'/'ctrl+p', popup closed): only at composer origin in chat
//     mode with history available and no suggest/modal overlay. The popup
//     opens at selected==0 and the key is NOT consumed -> (false, false, nil).
//   - While open: 'up'/'ctrl+p' move toward older entries and stop at the
//     oldest; 'down'/'ctrl+n' move toward newer (and dismiss at the newest);
//     'enter'/'tab' recall the selected entry, replacing the draft; 'esc'/
//     'shift+tab' dismiss keeping the draft; any other key dismisses and
//     passes through. All consumed keys return (true, true, nil).
// ---------------------------------------------------------------------------

// TestHistoryOverlayKeyOpenOnUpAtOriginNotConsumed: 'up' at the composer
// origin with history available opens the picker at the newest entry without
// consuming the key.
func TestHistoryOverlayKeyOpenOnUpAtOriginNotConsumed(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"one", "two"}
	m.textarea.SetValue("")
	m.textarea.SetCursor(0)

	// Precondition: cursor at the very start of the draft (line 0, col 0).
	if m.textarea.Line() != 0 || m.textarea.LineInfo().ColumnOffset != 0 || m.textarea.LineInfo().RowOffset != 0 {
		t.Fatalf("precondition: cursor must be at origin, got line=%d col=%d row=%d",
			m.textarea.Line(), m.textarea.LineInfo().ColumnOffset, m.textarea.LineInfo().RowOffset)
	}

	handled, skipView, _ := m.handleHistoryKey("up")
	if handled != false || skipView != false {
		t.Fatalf("open trigger: expected (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if !m.history.Open || m.history.Selected != 0 {
		t.Fatalf("open trigger: expected history open with selected==0, got open=%v selected=%d", m.history.Open, m.history.Selected)
	}
}

// TestHistoryOverlayKeyNoOpenOnEmptyHistory: no history, no popup.
func TestHistoryOverlayKeyNoOpenOnEmptyHistory(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	// sentHistory is empty.

	handled, skipView, _ := m.handleHistoryKey("up")
	if handled != false || skipView != false {
		t.Fatalf("empty history: expected (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("empty history: popup must not open")
	}
}

// TestHistoryOverlayKeyNoOpenOnMidLine: cursor not at column 0 -> no popup.
func TestHistoryOverlayKeyNoOpenOnMidLine(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"one"}
	m.textarea.SetValue("abc")
	m.textarea.SetCursor(2)

	if m.textarea.Line() != 0 || m.textarea.LineInfo().ColumnOffset == 0 {
		t.Fatalf("precondition: cursor must be mid-line, got line=%d col=%d",
			m.textarea.Line(), m.textarea.LineInfo().ColumnOffset)
	}

	handled, skipView, _ := m.handleHistoryKey("up")
	if handled != false || skipView != false {
		t.Fatalf("mid-line: expected (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("mid-line: popup must not open")
	}
}

// TestHistoryOverlayKeyNoOpenOnLineTwo: cursor on a later line -> no popup.
// Note: bubbles v1.0.0 SetCursor(col) clamps within the current row, so the
// cursor already lands on line 2 (row 1) after SetValue("ab\ncd"); SetCursor(3)
// keeps it there.
func TestHistoryOverlayKeyNoOpenOnLineTwo(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"one"}
	m.textarea.SetValue("ab\ncd")
	m.textarea.SetCursor(3)

	if m.textarea.Line() == 0 {
		t.Fatal("precondition: cursor must be on line 2 (Line() > 0)")
	}

	handled, skipView, _ := m.handleHistoryKey("up")
	if handled != false || skipView != false {
		t.Fatalf("line two: expected (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("line two: popup must not open")
	}
}

// TestHistoryOverlayKeyNoOpenWhenSuggestOpen: the slash-suggest popup owns the
// trigger keys -> no history popup.
func TestHistoryOverlayKeyNoOpenWhenSuggestOpen(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"one"}
	m.suggest.open = true

	handled, skipView, _ := m.handleHistoryKey("up")
	if handled != false || skipView != false {
		t.Fatalf("suggest open: expected (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("suggest open: popup must not open")
	}
}

// TestHistoryOverlayKeyNoOpenInWelcomeMode: history picker is chat-mode only.
func TestHistoryOverlayKeyNoOpenInWelcomeMode(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.mode = modeWelcome
	m.sentHistory = []string{"one"}

	handled, skipView, _ := m.handleHistoryKey("up")
	if handled != false || skipView != false {
		t.Fatalf("welcome mode: expected (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("welcome mode: popup must not open")
	}
}

// TestHistoryOverlayKeyDownDoesNotOpen: 'down' with the popup closed never
// opens it.
func TestHistoryOverlayKeyDownDoesNotOpen(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"one"}

	handled, skipView, _ := m.handleHistoryKey("down")
	if handled != false || skipView != false {
		t.Fatalf("down with popup closed: expected (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("down with popup closed: popup must not open")
	}
}

// TestHistoryOverlayKeyNavigateStopAtOldest: 'up' walks toward older entries
// and stops at the oldest, staying consumed and open.
func TestHistoryOverlayKeyNavigateStopAtOldest(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"oldest", "middle", "newest"} // entries: newest, middle, oldest
	m.openHistory()

	for want := 1; want <= 2; want++ {
		handled, skipView, _ := m.handleHistoryKey("up")
		if handled != true || skipView != true {
			t.Fatalf("up #%d: expected consumed (true, true, nil), got (handled=%v skipView=%v)", want, handled, skipView)
		}
		if m.history.Selected != want {
			t.Fatalf("up #%d: expected selected==%d, got %d", want, want, m.history.Selected)
		}
	}

	// Third up must stop at the oldest entry (index 2 of 3).
	handled, skipView, _ := m.handleHistoryKey("up")
	if handled != true || skipView != true {
		t.Fatalf("up at oldest: expected consumed (true, true, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Selected != 2 {
		t.Fatalf("up at oldest: expected selected to stop at 2, got %d", m.history.Selected)
	}
	if !m.history.Open {
		t.Fatal("up at oldest: popup must stay open")
	}
}

// TestHistoryOverlayKeyDownAtNewestDismisses: 'down' on the newest entry
// dismisses the popup, still consumed.
func TestHistoryOverlayKeyDownAtNewestDismisses(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"older", "newer"} // entries: newer, older
	m.openHistory()
	if !m.history.Open || m.history.Selected != 0 {
		t.Fatalf("precondition: popup must be open at newest, got open=%v selected=%d", m.history.Open, m.history.Selected)
	}

	handled, skipView, _ := m.handleHistoryKey("down")
	if handled != true || skipView != true {
		t.Fatalf("down at newest: expected consumed (true, true, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("down at newest: popup must close")
	}
}

// TestHistoryOverlayKeyDownMovesTowardNewest: 'down' from the oldest walks
// back toward the newest entry, staying open.
func TestHistoryOverlayKeyDownMovesTowardNewest(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"oldest", "middle", "newest"}
	m.openHistory()
	m.handleHistoryKey("up")
	m.handleHistoryKey("up")
	if m.history.Selected != 2 {
		t.Fatalf("precondition: expected selected==2 after two ups, got %d", m.history.Selected)
	}

	handled, skipView, _ := m.handleHistoryKey("down")
	if handled != true || skipView != true {
		t.Fatalf("down from oldest: expected consumed (true, true, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Selected != 1 {
		t.Fatalf("down from oldest: expected selected==1, got %d", m.history.Selected)
	}
	if !m.history.Open {
		t.Fatal("down from oldest: popup must stay open")
	}
}

// TestHistoryOverlayKeyEnterRecallsAndReplacesDraft: 'enter' recalls the
// selected entry, replacing (not appending to) the draft, and closes.
func TestHistoryOverlayKeyEnterRecallsAndReplacesDraft(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"old", "recent"} // entries: recent, old
	m.textarea.SetValue("draft")
	m.openHistory()
	if !m.history.Open || m.history.Selected != 0 {
		t.Fatalf("precondition: popup must be open at newest, got open=%v selected=%d", m.history.Open, m.history.Selected)
	}

	handled, skipView, _ := m.handleHistoryKey("enter")
	if handled != true || skipView != true {
		t.Fatalf("enter: expected consumed (true, true, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("enter: popup must close")
	}
	if got := m.textarea.Value(); got != "recent" {
		t.Fatalf("enter: draft must be REPLACED by the selected entry, got %q", got)
	}
}

// TestHistoryOverlayKeyEscDismissesKeepsDraft: 'esc' dismisses without
// touching the draft.
func TestHistoryOverlayKeyEscDismissesKeepsDraft(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"one"}
	m.textarea.SetValue("draft")
	m.openHistory()

	handled, skipView, _ := m.handleHistoryKey("esc")
	if handled != true || skipView != true {
		t.Fatalf("esc: expected consumed (true, true, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("esc: popup must close")
	}
	if got := m.textarea.Value(); got != "draft" {
		t.Fatalf("esc: draft must be untouched, got %q", got)
	}
}

// TestHistoryOverlayKeyOtherKeyClosesAndPassesThrough: any other key dismisses
// the popup and passes the key through unconsumed.
func TestHistoryOverlayKeyOtherKeyClosesAndPassesThrough(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"one"}
	m.openHistory()
	if !m.history.Open {
		t.Fatal("precondition: popup must be open")
	}

	handled, skipView, _ := m.handleHistoryKey("a")
	if handled != false || skipView != false {
		t.Fatalf("other key: expected pass-through (false, false, nil), got (handled=%v skipView=%v)", handled, skipView)
	}
	if m.history.Open {
		t.Fatal("other key: popup must close")
	}
}

// ---------------------------------------------------------------------------
// Wave 7: the message-history picker's keymap registration.
//
// Contract being locked in:
//   - cli.ScopeHistory declares exactly four rows whose keys are exactly
//     {up, ctrl+p, down, ctrl+n, enter, tab, esc, shift+tab}, each once, all
//     with help text and a group (the same shape /help renders).
//   - Every key those rows advertise is really handled by handleHistoryKey
//     while the picker is open: navigation keys move the selection, down/
//     ctrl+n dismiss at the newest entry, enter/tab recall the selected
//     message into the draft, esc/shift+tab dismiss. None is a phantom
//     binding (INV-TUI-23/27 direction: registry -> router).
// ---------------------------------------------------------------------------

// TestHistoryOverlayScopeRegistered: the registry row set for the history
// picker is exactly the four documented rows - each of the eight advertised
// keys appears exactly once, and every row carries help text and a group.
func TestHistoryOverlayScopeRegistered(t *testing.T) {
	var rows []cli.Binding
	for _, b := range cli.KeyRegistry {
		if b.Scope == cli.ScopeHistory {
			rows = append(rows, b)
		}
	}
	if len(rows) != 4 {
		t.Fatalf("cli.ScopeHistory must declare exactly 4 rows, got %d", len(rows))
	}

	wantKeys := map[string]bool{
		"up": true, "ctrl+p": true,
		"down": true, "ctrl+n": true,
		"enter": true, "tab": true,
		"esc": true, "shift+tab": true,
	}
	seen := map[string]bool{}
	for _, b := range rows {
		if b.Help == "" {
			t.Errorf("row %v (%s) has empty help text", b.Keys, b.Scope)
		}
		if b.Group == "" {
			t.Errorf("row %v (%s) has empty group", b.Keys, b.Scope)
		}
		for _, k := range b.Keys {
			if !wantKeys[k] {
				t.Errorf("unexpected registered history key %q", k)
			}
			if seen[k] {
				t.Errorf("history key %q is registered more than once", k)
			}
			seen[k] = true
		}
	}
	for k := range wantKeys {
		if !seen[k] {
			t.Errorf("history key %q is not registered in cli.ScopeHistory", k)
		}
	}
}

// TestHistoryOverlayRegisteredKeysReallyBound: every key the registry
// advertises for cli.ScopeHistory must actually do real work through
// handleHistoryKey while the picker is open - navigate, dismiss at the
// newest, recall into the draft, or close. A documented binding nothing
// handles is the same lie as an undocumented one, just pointed the other way.
func TestHistoryOverlayRegisteredKeysReallyBound(t *testing.T) {
	for _, b := range cli.KeyRegistry {
		if b.Scope != cli.ScopeHistory {
			continue
		}
		for _, key := range b.Keys {
			m := newReadyChatModel(24, 80)
			m.setFocus(cli.FocusComposer)
			m.sentHistory = []string{"one", "two"} // entries: two, one
			m.textarea.SetValue("")
			m.textarea.SetCursor(0)

			// Open the picker via the real trigger: 'up' at the composer
			// origin (the trigger itself is not consumed).
			m.handleHistoryKey("up")
			if !m.history.Open || m.history.Selected != 0 {
				t.Fatalf("%s: precondition: picker must be open at the newest entry, got open=%v selected=%d",
					key, m.history.Open, m.history.Selected)
			}

			handled, _, _ := m.handleHistoryKey(key)

			switch key {
			case "up", "ctrl+p":
				// Moves toward older entries, stays open, consumed.
				if !handled {
					t.Errorf("%s: navigate key must be consumed while the picker is open", key)
				}
				if !m.history.Open || m.history.Selected != 1 {
					t.Errorf("%s: expected selection to move to index 1 and stay open, got open=%v selected=%d",
						key, m.history.Open, m.history.Selected)
				}
			case "down", "ctrl+n":
				// At the newest entry (selected==0) it dismisses, consumed.
				if !handled {
					t.Errorf("%s: dismiss-at-newest key must be consumed while the picker is open", key)
				}
				if m.history.Open {
					t.Errorf("%s: expected the picker to close on %s at the newest entry", key, key)
				}
			case "enter", "tab":
				// Recalls the selected entry ("two") into the draft and closes.
				if !handled {
					t.Errorf("%s: recall key must be consumed while the picker is open", key)
				}
				if m.history.Open || m.textarea.Value() != "two" {
					t.Errorf("%s: expected recall to close the picker and restore %q, got open=%v draft=%q",
						key, "two", m.history.Open, m.textarea.Value())
				}
			case "esc", "shift+tab":
				// Dismisses, consumed.
				if !handled {
					t.Errorf("%s: dismiss key must be consumed while the picker is open", key)
				}
				if m.history.Open {
					t.Errorf("%s: expected the picker to close on %s", key, key)
				}
			default:
				t.Errorf("%s: registered history key falls outside the known categories", key)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Wave 8: lifecycle wiring for the sent-message history overlay.
//
// Contract being locked in:
//   - startAIWithDisplay appends the SENT text to sentHistory synchronously,
//     but only for real user sends (sent == display, non-empty). Rendered
//     skill bodies (sent != display) and empty sent bodies never append.
//   - Consecutive duplicate sends collapse into one entry (appendSentHistory
//     last-entry dedupe).
//   - /clear, beginNewSession, and a successful session load reset sentHistory
//     and close the popup.
//   - A focus change away from the composer closes the popup.
//
// The sibling production wiring is in place; all six tests pass and must keep
// passing. Tests 2 (SkillBodyNeverAppends), 3 (DedupeOnSend) and 6
// (FocusChangeCloses) are behavior guards against regressions.
// ---------------------------------------------------------------------------

// TestHistoryOverlayLifecycleSendAppends: a real send through
// startAIWithDisplay records the sent text in sentHistory. The append is
// synchronous at the top of the function; the worker goroutine talks to the
// stub completer and may finish before or after the assertion.
func TestHistoryOverlayLifecycleSendAppends(t *testing.T) {
	m := newReadyChatModel(24, 80)
	// The worker goroutine startAIWithDisplay spawns must have a completer to
	// talk to; the stub answers immediately so it cannot panic mid-test.
	m.session.Completer = welcomeStubCompleter{}

	m.startAIWithDisplay("hello", "hello")

	want := []string{"hello"}
	if !equalStrings(m.sentHistory, want) {
		t.Fatalf("startAIWithDisplay must append the sent text: got %v, want %v", m.sentHistory, want)
	}
}

// TestHistoryOverlayLifecycleSkillBodyNeverAppends: a rendered skill body
// (sent != display) and an empty sent body must never reach sentHistory.
func TestHistoryOverlayLifecycleSkillBodyNeverAppends(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.session.Completer = welcomeStubCompleter{}

	m.startAIWithDisplay("rendered skill prompt", "⚙ /skill args")
	if len(m.sentHistory) != 0 {
		t.Fatalf("skill render must not append to sentHistory, got %v", m.sentHistory)
	}

	m.startAIWithDisplay("", "label")
	if len(m.sentHistory) != 0 {
		t.Fatalf("empty sent body must not append to sentHistory, got %v", m.sentHistory)
	}
}

// TestHistoryOverlayLifecycleDedupeOnSend: sending the same text twice keeps a
// single entry (last-entry dedupe via appendSentHistory).
func TestHistoryOverlayLifecycleDedupeOnSend(t *testing.T) {
	m := newReadyChatModel(24, 80)

	m.appendSentHistory("x")
	m.appendSentHistory("x")

	want := []string{"x"}
	if !equalStrings(m.sentHistory, want) {
		t.Fatalf("duplicate sends must dedupe to a single entry: got %v, want %v", m.sentHistory, want)
	}
}

// TestHistoryOverlayLifecycleClearResets: /clear empties sentHistory and
// closes the history popup.
func TestHistoryOverlayLifecycleClearResets(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.sentHistory = []string{"a"}
	m.history.Open = true

	if !m.handleSlash("/clear") {
		t.Fatal("/clear must be handled")
	}

	if len(m.sentHistory) != 0 {
		t.Fatalf("/clear must reset sentHistory, got %v", m.sentHistory)
	}
	if m.history.Open {
		t.Fatal("/clear must close the history popup")
	}
}

// TestHistoryOverlayLifecycleNewSessionResets: beginNewSession empties
// sentHistory for a fresh conversation.
func TestHistoryOverlayLifecycleNewSessionResets(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.sentHistory = []string{"a"}

	m.beginNewSession()

	if len(m.sentHistory) != 0 {
		t.Fatalf("beginNewSession must reset sentHistory, got %v", m.sentHistory)
	}
}

// TestHistoryOverlayLifecycleFocusChangeCloses: moving focus away from the
// composer closes the history popup.
func TestHistoryOverlayLifecycleFocusChangeCloses(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.setFocus(cli.FocusComposer)
	m.sentHistory = []string{"a"}
	m.textarea.SetValue("")
	m.textarea.SetCursor(0)

	m.handleHistoryKey("up")
	if !m.history.Open {
		t.Fatal("precondition: popup must be open at the composer origin")
	}

	m.setFocus(cli.FocusScrollback)

	if m.history.Open {
		t.Fatal("focus change away from the composer must close the history popup")
	}
}

// TestHistoryOverlayLifecyclePasteCloses: bracketed paste mutates the draft,
// so it must dismiss the history picker - otherwise the next Enter recalls
// over (and silently discards) the freshly pasted text.
func TestHistoryOverlayLifecyclePasteCloses(t *testing.T) {
	m := newReadyChatModel(24, 80)
	m.sentHistory = []string{"old message"}
	m.textarea.SetValue("")
	m.textarea.SetCursor(0)

	m.handleHistoryKey("up")
	if !m.history.Open {
		t.Fatal("precondition: popup must be open at the composer origin")
	}

	_, _ = m.Update(pasteMsg("pasted text"))

	if m.history.Open {
		t.Fatal("bracketed paste must close the history popup")
	}
	if got := m.textarea.Value(); got != "pasted text" {
		t.Fatalf("paste must land in the draft, got %q", got)
	}
}

// TestHistoryOverlayLifecycleLoadSlashResets: /load is a session switch, so a
// successful load must clear sentHistory and close the popup; a FAILED load
// must leave the previous session's history untouched.
func TestHistoryOverlayLifecycleLoadSlashResets(t *testing.T) {
	m := newSmokeModel(t)
	m.session.SessionDir = t.TempDir()
	m.sentHistory = []string{"alpha"}
	m.history.Open = true

	if !m.handleTuiSessionStoreSlash("/load", []string{"/load", "never-saved"}) {
		t.Fatal("/load was not handled")
	}
	if len(m.sentHistory) != 1 || !m.history.Open {
		t.Fatal("a failed /load must not reset the previous session's history")
	}

	if err := m.session.Save("sess-b"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !m.handleTuiSessionStoreSlash("/load", []string{"/load", "sess-b"}) {
		t.Fatal("/load was not handled")
	}
	if len(m.sentHistory) != 0 || m.history.Open {
		t.Fatalf("a successful /load must reset sentHistory and close the popup, got %v open=%v", m.sentHistory, m.history.Open)
	}
}
