package cli

// The queue manager: a modal popup above the composer that shows the pending
// message queue (pendingQueue / pendingQueueLabels / pendingSkillTurns) in
// send order and lets the user steer (send now), delete, or edit any queued
// item. It is a modal with popup placement: while open it consumes every key
// and swallows paste/mouse (INV-TUI-29), ctrl+c/ctrl+q close-then-act
// (INV-TUI-26), and the composer stays visible below it.

// queuedItem is one entry of the pending queue: a single-item view over the
// three index-aligned slices. It doubles as the edit memory for the queue
// manager's edit flow, carrying the composer draft/cursor to restore on
// esc/cancel.
type queuedItem struct {
	index       int    // original queue index (esc-restore target)
	sent        string // text actually sent to the model ("" for skill turns)
	display     string // transcript/label text (skill labels differ from sent)
	skill       *skillSlashSpec
	savedDraft  string // composer draft before edit began (restore target)
	savedCursor int    // caret column within the saved draft's original line
	savedRow    int    // caret line of the saved draft (multi-line restore)
}

// queueMgrState is the open/selection state of the queue manager popup.
type queueMgrState struct {
	open     bool
	selected int
}
