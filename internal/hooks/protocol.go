package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// errorsAs is a thin alias so exec.go states its intent without importing
// errors twice over; keeping it here also keeps exec.go's import list about
// execution.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// verdict is what one handler's execution means for the event.
type verdict struct {
	denied   bool
	reason   string
	context  string
	warnings []string
	// truncated reports that the hook's own output was cut at the capture
	// bound, so Run announces it even when the aggregate fits.
	truncated bool
}

// classify applies the wire protocol to one execution.
//
// Only exit 0 parses stdout as JSON, matching Claude Code and Codex. At exit 2
// the JSON is ignored and stderr is the reason, so a hook cannot block and
// return a contradictory body at the same time.
func classify(event Event, handler Handler, result execution) verdict {
	if result.noVerdict {
		return noVerdictOutcome(event, handler, result)
	}
	switch result.exitCode {
	case 0:
		return parseStdout(event, result)
	case 2:
		if event == EventPreToolUse {
			return verdict{denied: true, reason: blockReason(result)}
		}
		// A reactive event cannot block: the tool already ran. The script's
		// complaint is still surfaced, as a warning rather than a veto.
		return verdict{warnings: warnIf(result, "exited 2, which only blocks on PreToolUse")}
	default:
		// An unrecognised exit code — including exit 1, the universal shell
		// error — means the script did not produce a decision. Routing through
		// noVerdictOutcome lets OnTimeout decide, which defaults to block for
		// PreToolUse: a script that crashes for any reason must not silently
		// open the gate.
		result.noVerdict = true
		result.reason = fmt.Sprintf("hook %s exited %d without producing a decision", result.label(), result.exitCode)
		return noVerdictOutcome(event, handler, result)
	}
}

// noVerdictOutcome resolves a handler that produced no answer - killed by its
// timeout, or unable to start at all. Both are the same situation: the control
// did not run. OnTimeout decides, and it defaults to block on PreToolUse,
// because an attacker who can make a gate hang - and so can an ordinary flaky
// script or a typo in argv - would otherwise have disabled it.
func noVerdictOutcome(event Event, handler Handler, result execution) verdict {
	if event == EventPreToolUse && handler.OnTimeout == OnTimeoutBlock {
		return verdict{denied: true, reason: result.reason}
	}
	// The operator warning keeps the exact file; the model-visible reason above
	// carries the hook's name only.
	return verdict{warnings: []string{fmt.Sprintf("%s (%s)", result.reason, result.program)}}
}

func blockReason(result execution) string {
	reason := strings.TrimSpace(string(result.stderr))
	if reason == "" {
		reason = fmt.Sprintf("hook %s denied this call and gave no reason on stderr", result.label())
	}
	return bound(reason, maxReasonBytes)
}

// warnIf builds an OPERATOR warning. Unlike a block reason it keeps the full
// path, because the operator is the one who has to find the file.
func warnIf(result execution, what string) []string {
	warning := fmt.Sprintf("hook %s %s", result.program, what)
	if detail := strings.TrimSpace(string(result.stderr)); detail != "" {
		warning += ": " + bound(detail, maxReasonBytes)
	}
	return []string{warning}
}

// bound truncates on a RUNE boundary. Block reasons and hook context are
// model-visible text; a trailing partial rune is invalid UTF-8 in a payload the
// provider has to encode, and cutting at a byte index is how that happens.
func bound(text string, limit int) string {
	if len(text) <= limit {
		return truncateAtRuneBoundary(text, len(text))
	}
	return truncateAtRuneBoundary(text, limit) + fmt.Sprintf("\n... truncated at %d bytes", limit)
}

// truncateAtRuneBoundary returns at most limit bytes of s, ending on a complete
// rune. Input already carrying invalid UTF-8 is left as it is: this repairs
// cuts we made, it does not sanitise a hook's own bytes.
func truncateAtRuneBoundary(s string, limit int) string {
	if limit > len(s) {
		limit = len(s)
	}
	cut := s[:limit]
	if utf8.ValidString(cut) {
		return cut
	}
	for i := limit - 1; i >= 0 && limit-i < utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			if utf8.ValidString(s[:i]) {
				return s[:i]
			}
			return cut
		}
	}
	return cut
}

// preToolUseOutput mirrors Claude Code's nested PreToolUse shape exactly, so a
// hook script written for either harness works in the other.
type preToolUseOutput struct {
	HookSpecificOutput *struct {
		HookEventName            string          `json:"hookEventName"`
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
	// Decision is the OTHER events' flat shape. Seeing it here means the script
	// was written against the wrong contract, and reading it as absent would
	// turn its deny into an allow.
	Decision string `json:"decision"`
}

// reactiveOutput is the flat shape PostToolUse and Stop use in Claude and Codex.
type reactiveOutput struct {
	Decision          string          `json:"decision"`
	Reason            string          `json:"reason"`
	AdditionalContext string          `json:"additionalContext"`
	UpdatedInput      json.RawMessage `json:"updatedInput"`
}

func parseStdout(event Event, result execution) verdict {
	body := strings.TrimSpace(string(result.stdout))
	if body == "" {
		return verdict{}
	}
	if !strings.HasPrefix(body, "{") {
		// Plain text is ordinary hook output, not a malformed decision.
		return verdict{context: body, truncated: result.truncated}
	}
	out := parseReactive(result, body)
	if event == EventPreToolUse {
		out = parsePreToolUse(result, body)
	}
	out.truncated = out.truncated || result.truncated
	return out
}

func parsePreToolUse(result execution, body string) verdict {
	var parsed preToolUseOutput
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		// Never read as a decision: fall back to exit-code semantics and say so.
		return verdict{warnings: []string{fmt.Sprintf(
			"hook %s printed JSON that did not parse (%v); its exit code decided the call", result.program, err)}}
	}
	if parsed.HookSpecificOutput == nil {
		if parsed.Decision != "" {
			return verdict{denied: true, reason: fmt.Sprintf(
				"hook %s returned the flat {\"decision\":%q} shape, which belongs to PostToolUse and Stop. "+
					"PreToolUse uses hookSpecificOutput.permissionDecision; mivia denies rather than read an "+
					"unrecognised shape as permission", result.label(), parsed.Decision)}
		}
		return verdict{context: body}
	}
	out := parsed.HookSpecificOutput
	if len(out.UpdatedInput) > 0 {
		return verdict{denied: true, reason: fmt.Sprintf(
			"hook %s returned updatedInput, which mivia does not support: the invocation's input hash and "+
				"dedup fingerprint are computed before the hook runs, so rewriting arguments afterwards would "+
				"record input that was never executed", result.label())}
	}
	switch out.PermissionDecision {
	case "allow":
		return verdict{}
	case "deny":
		reason := strings.TrimSpace(out.PermissionDecisionReason)
		if reason == "" {
			reason = fmt.Sprintf("hook %s denied this call and gave no permissionDecisionReason", result.label())
		}
		return verdict{denied: true, reason: bound(reason, maxReasonBytes)}
	default:
		// ask and defer have no dispatcher-layer prompt to escalate to, and an
		// unknown value is a hook attempting a decision mivia cannot honour.
		// Either way the permissive branch is the wrong place to land.
		return verdict{denied: true, reason: fmt.Sprintf(
			"hook %s returned permissionDecision %q; mivia accepts allow and deny only, and denies rather "+
				"than treat an unsupported decision as permission", result.label(), out.PermissionDecision)}
	}
}

func parseReactive(result execution, body string) verdict {
	var parsed reactiveOutput
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return verdict{warnings: []string{fmt.Sprintf(
			"hook %s printed JSON that did not parse (%v); its output was discarded", result.program, err)}}
	}
	var out verdict
	if len(parsed.UpdatedInput) > 0 {
		out.warnings = append(out.warnings, fmt.Sprintf(
			"hook %s returned updatedInput, which mivia does not support; it was ignored", result.program))
	}
	var parts []string
	// decision:"block" on a reactive event does not undo the action; it feeds
	// the reason to the model, as it does in the field.
	if parsed.Decision == "block" && strings.TrimSpace(parsed.Reason) != "" {
		parts = append(parts, strings.TrimSpace(parsed.Reason))
	}
	if text := strings.TrimSpace(parsed.AdditionalContext); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 && parsed.Decision == "" && parsed.Reason == "" && parsed.AdditionalContext == "" {
		// JSON that carried none of the known fields is ordinary output.
		out.context = body
		return out
	}
	out.context = strings.Join(parts, "\n")
	return out
}
