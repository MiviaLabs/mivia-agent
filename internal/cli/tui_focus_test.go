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

// TestWorkflowsSidebarFocusCycle includes the right sidebar in the cycle when
// it is visible: composer -> scrollback -> left -> right -> composer.
func TestWorkflowsSidebarFocusCycle(t *testing.T) {
	m := newReadyChatModel(50, 100)
	m.sessionsSidebar = newSessionsSidebar()
	m.workflowsSidebar = newWorkflowsSidebar()

	want := []tuiFocus{focusComposer, focusScrollback, focusSidebar, focusWorkflowsSidebar}
	for i := 0; i < len(want); i++ {
		current := want[i]
		next := want[(i+1)%len(want)]
		if got := m.nextTUIFocus(current, false); got != next {
			t.Errorf("forward from %v = %v, want %v", current, got, next)
		}
		if got := m.nextTUIFocus(next, true); got != current {
			t.Errorf("reverse from %v = %v, want %v", next, got, current)
		}
	}
}

// TestWorkflowsSidebarFocusCycleHiddenRightSidebar pins that a hidden right
// sidebar drops out of the cycle: composer -> scrollback -> left -> composer.
func TestWorkflowsSidebarFocusCycleHiddenRightSidebar(t *testing.T) {
	m := newReadyChatModel(50, 100)
	m.sessionsSidebar = newSessionsSidebar()
	m.workflowsSidebar = newWorkflowsSidebar()
	// A narrow terminal hides the right sidebar while the left stays visible.
	m.width = 90

	if got := m.nextTUIFocus(focusSidebar, false); got != focusComposer {
		t.Fatalf("hidden right sidebar: next from left = %v, want composer", got)
	}
}

// TestSetFocusClampsHiddenWorkflowsSidebar pins that focus on a hidden right
// sidebar clamps to the composer.
func TestSetFocusClampsHiddenWorkflowsSidebar(t *testing.T) {
	m := newReadyChatModel(50, 100)
	m.workflowsSidebar = newWorkflowsSidebar()
	m.width = 30 // too narrow for the right sidebar

	m.setFocus(focusWorkflowsSidebar)
	if m.focus != focusComposer {
		t.Fatalf("hidden workflows sidebar focus = %v, want composer", m.focus)
	}
}

// TestSetFocusWorkflowsSidebarVisible pins that a visible right sidebar
// accepts focus.
func TestSetFocusWorkflowsSidebarVisible(t *testing.T) {
	m := newReadyChatModel(50, 100)
	m.workflowsSidebar = newWorkflowsSidebar()

	m.setFocus(focusWorkflowsSidebar)
	if m.focus != focusWorkflowsSidebar {
		t.Fatalf("workflows sidebar focus = %v, want focusWorkflowsSidebar", m.focus)
	}
}

// TestRouteFocusKeyWorkflowsSidebar pins esc and printable input behavior
// from the workflows sidebar: esc returns to the composer consumed, printable
// input returns unconsumed.
func TestRouteFocusKeyWorkflowsSidebar(t *testing.T) {
	for _, key := range []string{"x", "esc"} {
		focus, consumed := routeFocusKey(focusWorkflowsSidebar, key)
		if focus != focusComposer {
			t.Errorf("routeFocusKey(workflows, %q) focus = %v, want composer", key, focus)
		}
		wantConsumed := key == "esc"
		if consumed != wantConsumed {
			t.Errorf("routeFocusKey(workflows, %q) consumed = %t, want %t", key, consumed, wantConsumed)
		}
	}
}
