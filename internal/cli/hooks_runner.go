package cli

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// hookPolicyFuncs delegates to composition.HookPolicyFuncs, wiring in this
// session's live hook state. Thin wrapper only: the hook-execution logic
// (nil-when-unconfigured contract, verdict parsing, run recording) lives in
// internal/composition/dispatcher.go now; this func only supplies the
// session accessors composition needs and holds no logic of its own.
func hookPolicyFuncs(workspaceRoot string) (
	func(context.Context, runtime.Request) runtime.HookVerdict,
	func(context.Context, runtime.Request, runtime.Result) runtime.HookResult,
) {
	return composition.HookPolicyFuncs(composition.DispatcherInput{
		WorkspaceRoot:    workspaceRoot,
		HooksConfigured:  hooksession.Configured(),
		HookGroups:       func() []hooks.Group { return hooksession.Current().RunnableGroups() },
		NoteHookWarnings: func(w []string) { hooksession.Current().NoteRunWarnings(w) },
	})
}
