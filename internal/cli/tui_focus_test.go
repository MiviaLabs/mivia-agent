package cli

import "testing"

func TestSidebarFocusCycleReturnsPrintableAndEscapeToComposer(t *testing.T) {
	for _, key := range []string{"x", "esc"} {
		focus, consumed := routeFocusKey(focusSidebar, key)
		if focus != focusComposer {
			t.Errorf("routeFocusKey(sidebar, %q) focus = %v, want composer", key, focus)
		}
		wantConsumed := key == "esc"
		if consumed != wantConsumed {
			t.Errorf("routeFocusKey(sidebar, %q) consumed = %t, want %t", key, consumed, wantConsumed)
		}
	}
}

func TestSetFocusRejectsHiddenSidebar(t *testing.T) {
	m := newReadyChatModel(30, 50)
	m.sessionsSidebar = newSessionsSidebar()
	m.setFocus(focusSidebar)
	if m.focus != focusComposer {
		t.Fatalf("hidden sidebar focus = %v, want composer", m.focus)
	}
}

func TestVisibleSidebarFocusCycleIncludesSidebar(t *testing.T) {
	m := newReadyChatModel(50, 100)
	m.sessionsSidebar = newSessionsSidebar()

	if got := m.nextTUIFocus(focusComposer, false); got != focusScrollback {
		t.Fatalf("composer next focus = %v, want scrollback", got)
	}
	if got := m.nextTUIFocus(focusScrollback, false); got != focusSidebar {
		t.Fatalf("scrollback next focus = %v, want sidebar", got)
	}
}
