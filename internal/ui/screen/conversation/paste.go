package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
)

// handlePaste forwards a bracketed-paste message to the composer's
// textarea. The textarea's own Update (bubbles/v2 textarea.go line
// ~1223) handles tea.PasteMsg by inserting the content at the cursor;
// the screen's only job is to deliver the message to it.
//
// Nothing else on the screen needs the message: the transcript renders
// streamed events, the approval prompt reads decisions, the menus
// consume keys. Dropping it here was the bug that made terminal paste
// look broken to the user (pin: TestPasteMsgInsertsIntoComposer and
// friends in paste_test.go).
//
// Modals still own the top of the stack while they are open
// (app.deliverTop routes input only to the top screen), so a paste that
// arrives during an open picker is forwarded to the picker's own
// Update, which has nothing to paste into. The picker is modal: closing
// it with esc lets the user re-paste into the composer.
func (s Screen) handlePaste(msg tea.PasteMsg) (app.Screen, tea.Cmd) {
	s.composer, _ = s.composer.Update(msg)
	return s, nil
}
