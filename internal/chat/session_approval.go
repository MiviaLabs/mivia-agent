package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// SetApprovalPolicy sets the active approval policy in a thread-safe manner.
func (s *Session) SetApprovalPolicy(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ApprovalPolicy = config.NormalizeApprovalPolicy(p)
	if s.BaseApprovalPolicy == "" {
		s.BaseApprovalPolicy = s.ApprovalPolicy
	}
}

// ApprovalPolicyValue returns the currently active approval policy in a thread-safe manner.
func (s *Session) ApprovalPolicyValue() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ApprovalPolicy == "" {
		return config.ApprovalPolicyWriteOnly
	}
	return s.ApprovalPolicy
}

// SetBaseApprovalPolicy records the baseline configured approval policy.
func (s *Session) SetBaseApprovalPolicy(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BaseApprovalPolicy = config.NormalizeApprovalPolicy(p)
	if s.ApprovalPolicy == "" {
		s.ApprovalPolicy = s.BaseApprovalPolicy
	}
}

// BaseApprovalPolicyValue returns the baseline configured approval policy.
func (s *Session) BaseApprovalPolicyValue() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.BaseApprovalPolicy == "" {
		return config.ApprovalPolicyWriteOnly
	}
	return s.BaseApprovalPolicy
}

// ToggleYOLO atomically toggles between YOLO auto-approval and the configured baseline policy.
// It returns whether YOLO mode is now enabled and the resulting effective policy.
func (s *Session) ToggleYOLO() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if config.IsAutoPolicy(s.ApprovalPolicy) {
		base := s.BaseApprovalPolicy
		if base == "" || config.IsAutoPolicy(base) {
			base = config.ApprovalPolicyWriteOnly
		}
		s.ApprovalPolicy = base
		return false, s.ApprovalPolicy
	}
	s.ApprovalPolicy = config.ApprovalPolicyAuto
	return true, s.ApprovalPolicy
}
