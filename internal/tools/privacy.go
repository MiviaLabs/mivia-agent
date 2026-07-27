package tools

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// EnvRedactToolArgs enables argument redaction when set to a truthy value
// (1, true, yes, on). Set to 0/false/no/off to force off. Unset = config default.
const EnvRedactToolArgs = "MIVIA_REDACT_TOOL_ARGS"

// redactToolArgs is package policy: when true, run_command hides argv and agent
// event previews scrub argument bodies. Default false (operator-visible args).
var redactToolArgs atomic.Bool

// SetRedactToolArgs enables or disables tool-argument redaction for this process.
func SetRedactToolArgs(on bool) {
	redactToolArgs.Store(on)
}

// RedactToolArgs reports whether tool arguments should be redacted in model/UI output.
func RedactToolArgs() bool {
	return redactToolArgs.Load()
}

// ApplyRedactToolArgsEnv overrides the current setting from MIVIA_REDACT_TOOL_ARGS
// when the variable is set. Returns whether the env var was present.
func ApplyRedactToolArgsEnv() bool {
	v, ok := os.LookupEnv(EnvRedactToolArgs)
	if !ok {
		return false
	}
	SetRedactToolArgs(parseTruthyEnv(v))
	return true
}

func parseTruthyEnv(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "1", "true", "yes", "on", "y", "t":
		return true
	case "0", "false", "no", "off", "n", "f", "":
		return false
	default:
		// Non-empty unknown → treat as true only if strconv.ParseBool succeeds.
		b, err := strconv.ParseBool(v)
		return err == nil && b
	}
}

// FormatArgv joins argv for operator-visible display (shell-safe quoting).
func FormatArgv(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	parts := make([]string, len(argv))
	for i, a := range argv {
		if needsQuote(a) {
			parts[i] = strconv.Quote(a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r <= ' ' || r == '"' || r == '\'' || r == '\\' {
			return true
		}
	}
	return false
}
