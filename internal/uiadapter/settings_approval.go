package uiadapter

import "github.com/MiviaLabs/mivia-agent/internal/config"

// approvalModeToView maps a normalized config.ApprovalPolicy* value back to
// the three settings-screen choices ("once" | "always" | "deny"). It is the
// read-side inverse of ports.SetApprovalDefault.Mode, which is written
// straight through to config.ApprovalsConfig.DefaultMode and applied live to
// the session via chat.Session.SetApprovalPolicy (see applyGeneral's
// ports.SetApprovalDefault case in settings.go).
func approvalModeToView(policy string) string {
	switch policy {
	case config.ApprovalPolicyAuto:
		return "always"
	case config.ApprovalPolicyDeny:
		return "deny"
	default:
		return "once"
	}
}
