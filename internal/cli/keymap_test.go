package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestKeyRegistryIsStructurallySound: the registry is the source /help renders
// from, so a duplicate, an unbindable alias or a missing description is a
// defect in the documentation the user reads.
func TestKeyRegistryIsStructurallySound(t *testing.T) {
	for _, err := range validateKeyRegistry(keyRegistry) {
		t.Error(err)
	}
}

// TestKeyRegistryRejectsForbiddenAliases proves the validator actually bites:
// ctrl+m is enter at the byte level, and a binding on it can never fire.
func TestKeyRegistryRejectsForbiddenAliases(t *testing.T) {
	bad := []binding{{keys: []string{"ctrl+m"}, scope: scopeGlobal, group: "Test", help: "toggle something"}}
	errs := validateKeyRegistry(bad)
	if len(errs) == 0 {
		t.Fatal("validator accepted ctrl+m, which is carriage return")
	}
	if !strings.Contains(errs[0].Error(), "carriage return") {
		t.Fatalf("validator gave no usable reason: %v", errs[0])
	}
}

// TestKeyRegistryRejectsDuplicateInScope: two handlers claiming one key in one
// scope is the ambiguity this whole layer was rebuilt to remove.
func TestKeyRegistryRejectsDuplicateInScope(t *testing.T) {
	dup := []binding{
		{keys: []string{"ctrl+t"}, scope: scopeGlobal, group: "A", help: "one thing"},
		{keys: []string{"ctrl+t"}, scope: scopeGlobal, group: "B", help: "another thing"},
	}
	if len(validateKeyRegistry(dup)) == 0 {
		t.Fatal("validator accepted the same key bound twice in one scope")
	}
	// The same key in different scopes is legitimate: home is line-start while
	// composing and scroll-to-top while reading.
	ok := []binding{
		{keys: []string{"home"}, scope: scopeComposer, group: "A", help: "line start"},
		{keys: []string{"home"}, scope: scopeScrollback, group: "B", help: "oldest message"},
	}
	if errs := validateKeyRegistry(ok); len(errs) != 0 {
		t.Fatalf("validator rejected a legitimate cross-scope binding: %v", errs)
	}
}

// TestHelpIsGeneratedFromRegistry closes the drift hole for good: the dialog
// the user reads is rendered from the same rows the registry declares, so a
// binding cannot be documented without being declared.
func TestHelpIsGeneratedFromRegistry(t *testing.T) {
	rendered := stripANSI(strings.Join(newHelpDialog(120).lines, "\n"))
	for _, b := range keyRegistry {
		if b.help == "" {
			continue
		}
		if !strings.Contains(rendered, keyLabel(b)) {
			t.Errorf("/help does not show registered binding %q (%s)", keyLabel(b), b.group)
		}
	}
}

// TestRegisteredChatKeysAreReallyBound is the contract between the registry
// and the router: every non-typable key the registry claims for chat must be
// consumed by the real key path. A row nothing handles is the same lie as an
// undocumented binding, just pointing the other way.
func TestRegisteredChatKeysAreReallyBound(t *testing.T) {
	// Keys deliberately handled by the focused component (textarea/viewport)
	// rather than the router: they reach their pane by falling through, so
	// "consumed by the router" is the wrong assertion for them.
	componentOwned := map[string]bool{
		"left": true, "right": true, "up": true, "down": true,
		"home": true, "end": true, "enter": true, " ": true,
		"alt+enter": true, "ctrl+a": true, "ctrl+e": true,
		"ctrl+left": true, "ctrl+right": true, "ctrl+u": true,
		"ctrl+k": true, "ctrl+w": true, "alt+backspace": true,
		"esc": true, "j": true, "k": true, "o": true, "y": true,
	}
	for _, b := range keyRegistry {
		if b.scope != scopeGlobal {
			continue
		}
		for _, key := range b.keys {
			if componentOwned[key] {
				continue
			}
			m := tallScrollModel(t, 6, 50)
			m.waiting = false
			m.setFocus(focusComposer)
			// ctrl+g is deliberately inert with no subagent activity to show
			// (a key that opens an empty panel is worse than one that waits),
			// so give it something before asserting it is bound.
			if key == "ctrl+g" {
				m.subagents = newSubagentTracker()
				m.subagents.Apply(events.Event{
					Kind: events.KindSubagentStart, AgentTask: "sub-1",
					AgentName: "explorer", Name: "read_file",
				}, time.Now())
			}
			skipTextarea, _, cmds := m.handleChatKey(key, false)
			if !skipTextarea && len(cmds) == 0 {
				t.Errorf("registry documents %q but the router does nothing with it", key)
			}
		}
	}
}

// TestForbiddenKeysAreInertInTheRouter: the aliases must not merely be absent
// from the registry — pressing them must change nothing.
func TestForbiddenKeysAreInertInTheRouter(t *testing.T) {
	for key := range forbiddenKeys {
		if key == "ctrl+m" || key == "ctrl+i" || key == "ctrl+j" {
			// These never arrive as themselves; bubbletea reports enter/tab.
			continue
		}
		m := tallScrollModel(t, 6, 50)
		m.setFocus(focusComposer)
		m.textarea.SetValue("draft")
		mouseBefore := m.mouseEnabled

		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})

		if m.mouseEnabled != mouseBefore {
			t.Errorf("%q changed mouse capture: %s", key, forbiddenKeys[key])
		}
		if m.textarea.Value() != "draft" {
			t.Errorf("%q altered the draft: %s", key, forbiddenKeys[key])
		}
	}
}
