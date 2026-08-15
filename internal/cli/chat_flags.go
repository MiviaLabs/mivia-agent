package cli

// chatFlags extracts boolean chat-invocation flags from a raw argument list.
// Unrecognised tokens are passed through in rest.
func chatFlags(args []string) (noTools, plainUI, staleBypass, jsonMode, quiet, fullDisk bool, rest []string) {
	for _, arg := range args {
		switch arg {
		case "--no-tools":
			noTools = true
		case "--plain":
			plainUI = true
		case "--bypass-hook-trust":
			// Accepted and ignored. The flag existed to run hooks that were
			// never confirmed; there is no confirmation to bypass any more.
			// Rejecting it would break the CI configs it was written for, and
			// those are the runs least able to explain a startup failure.
			staleBypass = true
		case "--json":
			// Reframes line-mode's stdout as NDJSON (chunk/done/cancelled/
			// error events - see ndjsonEvent) instead of raw streamed text.
			// Only valid for the non-interactive piped-stdin path;
			// runConfiguredChatOnce rejects it for the TUI/classic-REPL and
			// one-shot -p paths.
			jsonMode = true
		case "--quiet":
			// Suppress informational startup notices on stderr: the limits
			// summary, the lifecycle-hooks armed notice, the diagnostics
			// commands line, and the one-shot/REPL banner. Genuine config
			// warnings and workflow session-recovery diagnostics still print.
			quiet = true
		case "--full-disk":
			// Lifts the workspace confinement: file tools may read/write
			// anywhere on the filesystem, not only under --workspace.
			// Operator-invocation only — never settable from workspace config.
			fullDisk = true
		default:
			rest = append(rest, arg)
		}
	}
	return noTools, plainUI, staleBypass, jsonMode, quiet, fullDisk, rest
}
