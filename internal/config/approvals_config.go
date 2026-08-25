package config

import "strings"

// Approval policies.
const (
	ApprovalPolicyWriteOnly = "write-only"
	ApprovalPolicyAuto      = "auto"
	ApprovalPolicyAlways    = "always"
	ApprovalPolicyNever     = "never"
)

// NormalizeApprovalPolicy returns the normalized approval policy ("write-only", "auto", or "always").
func NormalizeApprovalPolicy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto", "never", "none", "yolo":
		return ApprovalPolicyAuto
	case "always", "paranoid", "all":
		return ApprovalPolicyAlways
	case "write-only", "writes", "default", "":
		return ApprovalPolicyWriteOnly
	default:
		return ApprovalPolicyWriteOnly
	}
}

// IsAutoPolicy reports whether the policy string represents auto-approval (YOLO mode).
func IsAutoPolicy(raw string) bool {
	return NormalizeApprovalPolicy(raw) == ApprovalPolicyAuto
}

// IsAlwaysPolicy reports whether the policy string represents always-prompt.
func IsAlwaysPolicy(raw string) bool {
	return NormalizeApprovalPolicy(raw) == ApprovalPolicyAlways
}

// ApprovalsConfig controls tool execution approval policies.
type ApprovalsConfig struct {
	Policy      string `toml:"policy"`
	DefaultMode string `toml:"default_mode"`
}

// ApprovalPolicy returns the normalized approval policy ("write-only", "auto", or "always").
func (a ApprovalsConfig) ApprovalPolicy() string {
	return NormalizeApprovalPolicy(a.Policy)
}

// IsAuto reports whether the approval policy is auto (YOLO mode).
func (a ApprovalsConfig) IsAuto() bool {
	return IsAutoPolicy(a.Policy)
}
