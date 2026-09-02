package config

import "strings"

// Approval policies. There are exactly three effective states, matching
// the TUI settings screen's three choices ("once", "always", "deny"):
//   - ApprovalPolicyWriteOnly ("once"): prompt for every write/external tool call.
//   - ApprovalPolicyAuto ("always"): auto-approve every tool call, no prompt.
//   - ApprovalPolicyDeny ("deny"): auto-deny every gated tool call, no prompt.
//
// ApprovalPolicyAlways is kept ONLY as a legacy input alias for the old
// [approvals] policy = "always" key, which meant "prompt for every call,
// including reads" (paranoid mode) - a different concept from the
// settings-screen's "always" ("accept always"/auto-approve). The two
// vocabularies collide on the bare word "always" with opposite meanings,
// which is why DefaultMode and Policy are normalized by two different
// functions below (NormalizeDefaultMode vs NormalizeApprovalPolicy) instead
// of one shared switch.
const (
	ApprovalPolicyWriteOnly = "write-only"
	ApprovalPolicyAuto      = "auto"
	ApprovalPolicyAlways    = "always"
	ApprovalPolicyDeny      = "deny"
)

// NormalizeApprovalPolicy returns the normalized approval policy
// ("write-only", "auto", "always", or "deny") for the legacy [approvals]
// policy field and the --approval-policy CLI flag, both of which speak the
// write-only/auto/always vocabulary.
//
// An unset value normalizes to ApprovalPolicyWriteOnly, the CONSERVATIVE
// fallback - not to auto. This comment used to claim auto and describe the
// fresh-mivia.toml case, which this function is not what decides: that is
// ApprovalsConfig.ApprovalPolicy below, "the ONE place that default lives",
// and it never calls this with an empty string. The distinction is
// deliberate and stated there - this function also normalizes the policy of
// an ALREADY-RUNNING session (Session.ToggleYOLO's zero-value field), where
// "unset" must not silently mean "accept every tool".
func NormalizeApprovalPolicy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto", "never", "none", "yolo":
		return ApprovalPolicyAuto
	case "always", "paranoid", "all":
		return ApprovalPolicyAlways
	case "deny", "deny_always", "never-approve":
		return ApprovalPolicyDeny
	case "write-only", "writes", "once", "default", "":
		return ApprovalPolicyWriteOnly
	default:
		return ApprovalPolicyWriteOnly
	}
}

// NormalizeDefaultMode returns the normalized approval policy for the TUI
// settings screen's "approval default" vocabulary ("once" | "always" |
// "deny", config key [approvals] default_mode). Unlike
// NormalizeApprovalPolicy, "always" here means "accept always" (auto
// approve) - it normalizes to ApprovalPolicyAuto, not ApprovalPolicyAlways.
// The default fallback for an unset value is ApprovalPolicyAuto, matching
// the shipped default of accepting all tools out of the box.
func NormalizeDefaultMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "always", "auto", "yolo", "accept_always", "accept-always":
		return ApprovalPolicyAuto
	case "deny", "deny_always":
		return ApprovalPolicyDeny
	case "once", "write-only", "writes", "":
		return ApprovalPolicyWriteOnly
	default:
		return ApprovalPolicyWriteOnly
	}
}

// IsAutoPolicy reports whether the policy string represents auto-approval (YOLO mode).
func IsAutoPolicy(raw string) bool {
	return NormalizeApprovalPolicy(raw) == ApprovalPolicyAuto
}

// IsAlwaysPolicy reports whether the policy string represents always-prompt
// (the legacy "paranoid" policy vocabulary, not the settings-screen
// "always"/accept-always choice).
func IsAlwaysPolicy(raw string) bool {
	return NormalizeApprovalPolicy(raw) == ApprovalPolicyAlways
}

// IsDenyPolicy reports whether the policy string represents auto-deny (every
// gated tool call is rejected without a prompt).
func IsDenyPolicy(raw string) bool {
	return NormalizeApprovalPolicy(raw) == ApprovalPolicyDeny
}

// ApprovalsConfig controls tool execution approval policies. DefaultMode is
// the single source of truth for the TUI settings screen ("once" | "always"
// | "deny") and is what session construction resolves into
// Session.ApprovalPolicy. Policy is kept only as a legacy input alias for
// DefaultMode's write-only/auto/always/deny vocabulary; when both are set,
// DefaultMode wins.
type ApprovalsConfig struct {
	Policy      string `toml:"policy"`
	DefaultMode string `toml:"default_mode"`
}

// ApprovalPolicy returns the normalized approval policy ("write-only",
// "auto", "always", or "deny"), preferring DefaultMode (settings-screen
// vocabulary) over the legacy Policy field (write-only/auto/always
// vocabulary). An unset config (both fields empty) resolves to
// ApprovalPolicyAuto: accept all tools by default - this is the ONE place
// that default lives; NormalizeApprovalPolicy/NormalizeDefaultMode keep
// their own conservative (write-only) empty-string fallback because they
// are also used for already-running sessions (e.g. Session.ToggleYOLO's
// zero-value field), where "unset" must not silently mean "auto".
func (a ApprovalsConfig) ApprovalPolicy() string {
	switch {
	case strings.TrimSpace(a.DefaultMode) != "":
		return NormalizeDefaultMode(a.DefaultMode)
	case strings.TrimSpace(a.Policy) != "":
		return NormalizeApprovalPolicy(a.Policy)
	default:
		return ApprovalPolicyAuto
	}
}

// IsAuto reports whether the approval policy is auto (YOLO mode).
func (a ApprovalsConfig) IsAuto() bool {
	return IsAutoPolicy(a.ApprovalPolicy())
}
