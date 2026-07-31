package subagents

// Reserved handler names that agent definitions must not collide with.
// The CLI dispatcher registers these as Kind=Subagent handlers.
const (
	HandlerMultiStep = "multi_step"
	HandlerDelegate  = "delegate"
	HandlerOneshot   = "oneshot"
)

// ReservedHandlerNames is the single set used by agent catalogue validation
// and dispatcher registration. Callers must treat the map as read-only.
func ReservedHandlerNames() map[string]struct{} {
	return map[string]struct{}{
		HandlerMultiStep: {},
		HandlerDelegate:  {},
		HandlerOneshot:   {},
	}
}

// IsReservedHandler reports whether name is a built-in subagent handler.
func IsReservedHandler(name string) bool {
	_, ok := ReservedHandlerNames()[name]
	return ok
}
