package workflow

// CLI startup helpers moved from internal/cli/chat and internal/cli so the
// workflow domain can call them through static imports (was: nil seam vars
// wired by internal/cli's init).

import (
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// applyPrivacyPolicy installs the process-wide privacy settings.
//
// Tool-argument redaction is opt-in and read from BOTH [privacy] and [tools]
// so either TOML path works. The redaction policy is nil when the workspace
// configured no patterns, which redacts nothing - see rule 10.
func applyPrivacyPolicyImpl(res *config.Resolved) {
	tools.SetRedactToolArgs(res.Privacy.RedactToolArgs || res.Tools.RedactToolArgs)
	redact.SetPolicy(res.RedactionPolicy)
	applyContextLimits(res)
}

// applyContextLimits installs the operator's durable ceilings process-wide.
// It sits beside the redaction policy deliberately: both are workspace policy
// this binary must not invent, and a process that configures neither runs
// uncapped and unredacted rather than under a compiled-in guess.
func applyContextLimits(res *config.Resolved) {
	state.SetLimits(state.Limits{
		SourceEventBytes:        res.Context.MaxSourceEventBytes,
		CheckpointBytes:         res.Context.MaxCheckpointBytes,
		CommitEvents:            res.Context.MaxCommitEvents,
		CommitEventBytes:        res.Context.MaxCommitEventBytes,
		SessionStateBytes:       res.Context.MaxSessionStateBytes,
		ExportBytes:             res.Context.MaxExportBytes,
		SummaryMetadataBytes:    res.Context.SummaryMetadataBytes,
		CheckpointMetadataBytes: res.Context.CheckpointMetadataBytes,
	})
}

func LogMCPWarnings(w io.Writer, res *config.Resolved) {
	if w == nil || res == nil {
		return
	}
	for _, warn := range res.MCPWarnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
}

// messagingDisallowed is the opt-out set for the baseline messaging
// injection: names that must NOT be re-added to an already-scoped registry.
//
// It takes the AGENT rather than a name list on purpose. Both call sites used
// to pass agent.DisallowedTools - the agent file's own list - so an
// operator's mandatory_tool_denylist entry for post_message was stripped by
// applyToolPolicy, excluded by AuthorizedAgentTools, dropped by
// ScopedRegistry, and then put straight back by the injection. Taking the
// agent means there is no second list a caller can reach for:
// EffectiveDenylist carries the agent's own denials AND the operator's.
func MessagingDisallowed(agent agents.ResolvedAgent) map[string]struct{} {
	out := map[string]struct{}{}
	for _, name := range agent.EffectiveDenylist {
		out[name] = struct{}{}
	}
	// DisallowedTools is a subset of EffectiveDenylist as resolve builds it;
	// included explicitly so a hand-built ResolvedAgent that sets only the
	// former still opts out rather than silently gaining post_message.
	for _, name := range agent.DisallowedTools {
		out[name] = struct{}{}
	}
	return out
}

// sliceErrors converts a []string of error messages to a single error
// if non-empty, or nil.
func sliceErrorsImpl(context string, errs []string) error {
	if len(errs) > 0 {
		return fmt.Errorf("%s: %s", context, strings.Join(errs, "; "))
	}
	return nil
}

// flagValue returns the value of the first occurrence of any named flag,
// plus the remaining tokens. The space form requires a following value token
// that is not itself a flag: a missing or dash-prefixed value is a caller
// error (DC-9 fail-open), so it is refused with "%s requires a value" instead
// of silently swallowing the next flag as a value. The "=" form stays
// permissive so values that legitimately start with "-" remain expressible as
// --name=--value. The found bool reports whether any name matched.
func FlagValue(args []string, names ...string) (string, []string, bool, error) {
	out := make([]string, 0, len(args))
	var val string
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return "", nil, found, fmt.Errorf("%s requires a value", n)
				}
				val = args[i+1]
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				val = strings.TrimPrefix(a, n+"=")
				found = true
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, a)
		}
	}
	return val, out, found, nil
}

// flagVar is like flagValue but for repeatable string flags. Each occurrence
// of any name collects one value. Supports both "--flag VALUE" and
// "--flag=VALUE". Like flagValue it refuses a missing or dash-prefixed space
// value instead of swallowing a following flag (DC-9).
func FlagVar(args []string, names ...string) ([]string, []string, bool, error) {
	var vals []string
	rest := make([]string, 0, len(args))
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return nil, nil, found, fmt.Errorf("%s requires a value", n)
				}
				vals = append(vals, args[i+1])
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				vals = append(vals, strings.TrimPrefix(a, n+"="))
				found = true
				matched = true
				break
			}
		}
		if !matched {
			rest = append(rest, a)
		}
	}
	return vals, rest, found, nil
}

// Override points so tests can stub policy installation and error joining.
var (
	ApplyPrivacyPolicy = applyPrivacyPolicyImpl
	SliceErrors        = sliceErrorsImpl
)
