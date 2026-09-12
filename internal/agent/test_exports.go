package agent

// test_exports.go exposes loop internals a cross-package parity test needs.
// foldedTaskArg must keep resolving model-authored keys exactly the way the
// operator surface's foldedArg (internal/ui/screen/conversation/events.go)
// does; the comment on foldedTaskArg states that contract, and this export
// lets a test in the consumer's package pin it instead of trusting prose.

// FoldedTaskArgForTest reports foldedTaskArg's pick for key in m.
func FoldedTaskArgForTest(m map[string]any, key string) any {
	return foldedTaskArg(m, key)
}
