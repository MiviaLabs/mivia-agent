package chat_test

import (
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestSessionApprovalPolicy_Defaults(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	if got := sess.ApprovalPolicyValue(); got != config.ApprovalPolicyWriteOnly {
		t.Fatalf("ApprovalPolicyValue() = %q, want %q", got, config.ApprovalPolicyWriteOnly)
	}
	if got := sess.BaseApprovalPolicyValue(); got != config.ApprovalPolicyWriteOnly {
		t.Fatalf("BaseApprovalPolicyValue() = %q, want %q", got, config.ApprovalPolicyWriteOnly)
	}
}

func TestSessionApprovalPolicy_SetAndGet(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.SetApprovalPolicy("auto")
	if got := sess.ApprovalPolicyValue(); got != config.ApprovalPolicyAuto {
		t.Fatalf("ApprovalPolicyValue() = %q, want %q", got, config.ApprovalPolicyAuto)
	}
	if got := sess.BaseApprovalPolicyValue(); got != config.ApprovalPolicyAuto {
		t.Fatalf("BaseApprovalPolicyValue() = %q, want %q (should initialize base on first set)", got, config.ApprovalPolicyAuto)
	}
}

func TestSessionApprovalPolicy_ToggleYOLO_PreservesBasePolicy(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.SetBaseApprovalPolicy("always")
	sess.SetApprovalPolicy("always")

	// 1. Toggle ON: becomes auto
	enabled, policy := sess.ToggleYOLO()
	if !enabled || policy != config.ApprovalPolicyAuto {
		t.Fatalf("ToggleYOLO() = (%v, %q), want (true, %q)", enabled, policy, config.ApprovalPolicyAuto)
	}
	if got := sess.ApprovalPolicyValue(); got != config.ApprovalPolicyAuto {
		t.Fatalf("ApprovalPolicyValue() = %q, want %q", got, config.ApprovalPolicyAuto)
	}

	// 2. Toggle OFF: restores base policy ("always")
	enabled, policy = sess.ToggleYOLO()
	if enabled || policy != config.ApprovalPolicyAlways {
		t.Fatalf("ToggleYOLO() = (%v, %q), want (false, %q)", enabled, policy, config.ApprovalPolicyAlways)
	}
	if got := sess.ApprovalPolicyValue(); got != config.ApprovalPolicyAlways {
		t.Fatalf("ApprovalPolicyValue() = %q, want %q", got, config.ApprovalPolicyAlways)
	}
}

func TestSessionApprovalPolicy_ConcurrentSafety(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.SetBaseApprovalPolicy("write-only")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			sess.ToggleYOLO()
		}()
		go func() {
			defer wg.Done()
			_ = sess.ApprovalPolicyValue()
		}()
	}
	wg.Wait()
}
