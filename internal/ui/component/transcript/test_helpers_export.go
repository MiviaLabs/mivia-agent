package transcript

// PushBlockForTest exposes pushBlock to other packages' tests. Local to
// this package (pushBlock is unexported) so cross-package tests can build
// a Model whose focused block carries a field combination no production
// event handler ever produces on its own - for example Kind !=
// KindToolStart paired with Header.State == "running": handleToolStart is
// the only writer of State == "running", and it always pairs that with
// Kind == KindToolStart, so ordinary event-driven construction can never
// separate the two. A defensive guard clause that checks both fields
// independently (internal/ui/screen/conversation's cancelFocusedToolCall,
// among others) needs exactly that otherwise-unreachable combination to
// prove each of its checks has an independent effect.
//
// Precedent: internal/workflows/localengine/test_helpers_export.go,
// internal/cliorchestrate/test_exports.go.
func PushBlockForTest(m Model, b Block) Model {
	m, _ = m.pushBlock(b)
	return m
}
