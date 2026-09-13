package clichat

// The pending-message queue: what happens to text typed while the agent is
// working. Queued input is held, then sent when the turn ends (or force-sent
// on an empty Enter). Slash commands run locally as they come off the queue.
//
// The queue is three index-aligned slices on TUIModel
// (pendingQueue / pendingQueueLabels / pendingSkillTurns). Every mutation
// goes through queueRemoveAt/queueInsertAt/queueAppend (tui_queue_manager.go)
// so the alignment cannot drift, and sendQueuedItem owns the canonical
// dispatch shared by the turn-end drain and the queue manager's steer.
