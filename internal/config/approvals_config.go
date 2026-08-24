package config

import "strings"

// Approval policies.
const (
	ApprovalPolicyWriteOnly = "write-only"
	ApprovalPolicyAuto      = "auto"
	ApprovalPolicyAlways    = "always"
	ApprovalPolicyNever     = "never"
)

// ApprovalsConfig controls tool execution approval policies.
type ApprovalsConfig struct {
	Policy string `toml:"policy"`
}

// ApprovalPolicy returns the normalized approval policy ("write-only", "auto", or "always").
func (a ApprovalsConfig) ApprovalPolicy() string {
	switch strings.ToLower(strings.TrimSpace(a.Policy)) {
	case "auto", "never", "none", "yolo":
		return ApprovalPolicyAuto
	case "always", "paranoid":
		return ApprovalPolicyAlways
	case "write-only", "writes", "default", "":
		return ApprovalPolicyWriteOnly
	default:
		return ApprovalPolicyWriteOnly
	}
}

// IsAuto reports whether the approval policy is auto (YOLO mode).
func (a ApprovalsConfig) IsAuto() bool {
	return a.ApprovalPolicy() == ApprovalPolicyAuto
}
