package definition

// hostFailure classifies a verifier that could not run. The check detail
// must say WHY (missing bubblewrap, module baseline, git worktree init,
// sandbox command stderr) — bounded and redacted — because a repair agent
// or operator acts on the cause, not on a placeholder.
func hostFailure(err error) *commandFailure {
	detail := "host verifier setup failed"
	if err != nil {
		detail = detail + ": " + boundedDiagnostic([]byte(err.Error()))
	}
	return &commandFailure{class: "host", detail: detail, err: err}
}
