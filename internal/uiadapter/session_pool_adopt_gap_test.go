package uiadapter

import "testing"

func TestToolRootForReturnsDirWhenOutsideBoundRoot(t *testing.T) {
	if got := toolRootFor("/repo/root", "/elsewhere"); got != "/elsewhere" {
		t.Fatalf("toolRootFor(bound, outside dir) = %q, want the dir unchanged", got)
	}
}

func TestAuthoritativeFullDiskLockedDefaultsToFalse(t *testing.T) {
	p := &SessionPool{}
	if got := p.authoritativeFullDiskLocked(); got != false {
		t.Fatalf("authoritativeFullDiskLocked() with no agent state and no sessions = %v, want false", got)
	}
}

func TestSessionBusProviderLockedReturnsNilWithNoSessions(t *testing.T) {
	p := &SessionPool{}
	if got := p.sessionBusProviderLocked(); got != nil {
		t.Fatal("sessionBusProviderLocked() with no sessions returned a non-nil provider, want nil")
	}
}
