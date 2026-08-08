package agentmsg

import (
	"strings"
	"testing"
)

func TestRouteAskDeliverLiveAnyRole(t *testing.T) {
	p := RoutingPolicy{Mode: "policy", MaxAsksPerTask: 4, MaxReferralDepth: 2, MaxReferralSpawnsPerRun: 4}
	d := RouteAsk(p, RouteInput{FromRole: "reviewer", ToRole: "auditor", TargetRunning: true})
	if d.Action != RouteDeliver {
		t.Fatalf("got %+v", d)
	}
}

func TestRouteAskDeliverRequiresAllowWhenSet(t *testing.T) {
	p := RoutingPolicy{
		Mode: "policy", MaxAsksPerTask: 4, MaxReferralDepth: 2,
		Allow: []string{"reviewer->auditor"}, MaxReferralSpawnsPerRun: 4,
	}
	if d := RouteAsk(p, RouteInput{FromRole: "reviewer", ToRole: "auditor", TargetRunning: true}); d.Action != RouteDeliver {
		t.Fatalf("allowed pair: %+v", d)
	}
	if d := RouteAsk(p, RouteInput{FromRole: "auditor", ToRole: "reviewer", TargetRunning: true}); d.Action != RouteDecline || d.Reason != DeclineNotAllowed {
		t.Fatalf("disallowed live pair: %+v", d)
	}
}

func TestRouteAskBlockingNoSpawn(t *testing.T) {
	p := RoutingPolicy{
		Mode: "policy", MaxAsksPerTask: 4, MaxReferralDepth: 2,
		Allow: []string{"reviewer->auditor"}, MaxReferralSpawnsPerRun: 4,
	}
	d := RouteAsk(p, RouteInput{
		FromRole: "reviewer", ToRole: "auditor", Blocking: true, TargetRunning: false,
	})
	if d.Action != RouteDecline || d.Reason != DeclineTargetNotRunning {
		t.Fatalf("got %+v", d)
	}
}

func TestRouteAskSpawnWhenAllowedNonBlocking(t *testing.T) {
	p := RoutingPolicy{
		Mode: "policy", MaxAsksPerTask: 4, MaxReferralDepth: 2,
		Allow: []string{"reviewer->auditor"}, MaxReferralSpawnsPerRun: 4,
	}
	d := RouteAsk(p, RouteInput{
		FromRole: "reviewer", ToRole: "auditor", Blocking: false, TargetRunning: false,
	})
	if d.Action != RouteSpawn {
		t.Fatalf("got %+v", d)
	}
}

func TestRouteAskSpawnRequiresExplicitAllow(t *testing.T) {
	p := RoutingPolicy{Mode: "policy", MaxAsksPerTask: 4, MaxReferralDepth: 2, MaxReferralSpawnsPerRun: 4}
	d := RouteAsk(p, RouteInput{
		FromRole: "reviewer", ToRole: "auditor", Blocking: false, TargetRunning: false,
	})
	if d.Action != RouteDecline || d.Reason != DeclineNotAllowed {
		t.Fatalf("empty allow must refuse spawn: %+v", d)
	}
}

func TestRouteAskQuotasDepthCycle(t *testing.T) {
	p := RoutingPolicy{
		Mode: "policy", MaxAsksPerTask: 2, MaxReferralDepth: 2,
		Allow: []string{"a->b", "b->c", "c->d"}, MaxReferralSpawnsPerRun: 1,
	}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true, AsksUsedByTask: 2}); d.Reason != DeclineQuotaExceeded {
		t.Fatalf("ask quota: %+v", d)
	}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true, ChainDepth: 2}); d.Reason != DeclineDepthExceeded {
		t.Fatalf("depth: %+v", d)
	}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true, Cycle: true}); d.Reason != DeclineCycle {
		t.Fatalf("cycle: %+v", d)
	}
	// Regression: cycle must win over quota and depth.
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true, Cycle: true, AsksUsedByTask: 2}); d.Reason != DeclineCycle {
		t.Fatalf("cycle + quota: %+v", d)
	}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true, Cycle: true, ChainDepth: 2}); d.Reason != DeclineCycle {
		t.Fatalf("cycle + depth: %+v", d)
	}
	if d := RouteAsk(p, RouteInput{
		FromRole: "a", ToRole: "b", Blocking: false, TargetRunning: false,
		ReferralSpawnsUsed: 1,
	}); d.Reason != DeclineSpawnQuotaExceeded {
		t.Fatalf("spawn quota: %+v", d)
	}
}

func TestRouteAskParentModeUnimplemented(t *testing.T) {
	d := RouteAsk(RoutingPolicy{Mode: "parent"}, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true})
	if d.Action != RouteDecline || d.Reason != DeclineParentModeUnimplemented {
		t.Fatalf("got %+v", d)
	}
}

func TestRouteAskNoSuchRoleEmptyTo(t *testing.T) {
	d := RouteAsk(RoutingPolicy{Mode: "policy"}, RouteInput{FromRole: "a", ToRole: "", TargetRunning: true})
	if d.Reason != DeclineNoSuchRole {
		t.Fatalf("got %+v", d)
	}
}

func TestRouteAskDefaultsAndParseEdges(t *testing.T) {
	// Empty mode → policy; empty max fields → defaults; empty from → invalid.
	d := RouteAsk(RoutingPolicy{}, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true})
	if d.Action != RouteDeliver {
		t.Fatalf("empty mode: %+v", d)
	}
	if d := RouteAsk(RoutingPolicy{Mode: "policy"}, RouteInput{FromRole: "", ToRole: "b", TargetRunning: true}); d.Reason != DeclineInvalid {
		t.Fatalf("empty from: %+v", d)
	}
	// max_spawn default when target not running + allow.
	d = RouteAsk(RoutingPolicy{Mode: "policy", Allow: []string{"a->b"}}, RouteInput{
		FromRole: "a", ToRole: "b", TargetRunning: false, Blocking: false,
	})
	if d.Action != RouteSpawn {
		t.Fatalf("spawn defaults: %+v", d)
	}
	// Malformed allow entries ignored.
	d = RouteAsk(RoutingPolicy{Mode: "policy", Allow: []string{"", "nook", "a->", "->b", "a->b"}}, RouteInput{
		FromRole: "a", ToRole: "b", TargetRunning: true,
	})
	if d.Action != RouteDeliver {
		t.Fatalf("parse allow: %+v", d)
	}
	if AllowPairKey(" x ", " y ") != "x->y" {
		t.Fatalf("AllowPairKey spaces")
	}
	// Fail-closed rule: a non-empty Allow list that yields zero valid pairs
	// must never degrade to any-live. Both entries here are malformed.
	d = RouteAsk(RoutingPolicy{Mode: "policy", Allow: []string{"", "bad"}}, RouteInput{
		FromRole: "a", ToRole: "b", TargetRunning: true,
	})
	if d.Action != RouteDecline || d.Reason != DeclineNotAllowed {
		t.Fatalf("all-malformed allow must fail closed: %+v", d)
	}
}

// TestRouteAskMalformedAllowFailsClosed pins the fail-closed routing rule: a
// non-empty Allow list whose entries all fail to parse must decline with
// DeclineNotAllowed, never deliver to any live target. Before the fix,
// "reviewer--auditor" (double dash, no arrow) parsed to an empty pair map and
// RouteAsk delivered to any live target, bypassing the operator's routing
// isolation.
func TestRouteAskMalformedAllowFailsClosed(t *testing.T) {
	p := RoutingPolicy{Mode: "policy", MaxAsksPerTask: 4, MaxReferralDepth: 2, MaxReferralSpawnsPerRun: 4}

	// Red case: zero valid pairs, live target -> fail closed.
	p.Allow = []string{"reviewer--auditor"}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true}); d.Action != RouteDecline || d.Reason != DeclineNotAllowed {
		t.Fatalf("all-malformed allow with live target: %+v", d)
	}

	// Non-blocking non-running variant: the spawn path already declines and
	// must stay declined.
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", Blocking: false, TargetRunning: false}); d.Action != RouteDecline || d.Reason != DeclineNotAllowed {
		t.Fatalf("all-malformed allow spawn path: %+v", d)
	}

	// Stability pin: "reviewer-->auditor" still contains the separator, so
	// parseAllowPairs keeps the mis-split pair and already declines today.
	p.Allow = []string{"reviewer-->auditor"}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true}); d.Action != RouteDecline || d.Reason != DeclineNotAllowed {
		t.Fatalf("mis-split pair must stay declined: %+v", d)
	}
}

// TestRouteAskAllowListTable covers the structured-input classes for the
// Allow list: empty, whitespace-only, single empty entry, valid, reversed,
// duplicate, partial-malformed-with-valid, and oversized entries. It also pins
// the ordering: cycle and ask quota still win over the fail-closed guard.
func TestRouteAskAllowListTable(t *testing.T) {
	base := RoutingPolicy{Mode: "policy", MaxAsksPerTask: 4, MaxReferralDepth: 2, MaxReferralSpawnsPerRun: 4}
	cases := []struct {
		name       string
		allow      []string
		from, to   string
		wantAction RouteAction
		wantReason string
	}{
		{name: "nil empty list delivers any live", allow: nil, from: "a", to: "b", wantAction: RouteDeliver},
		{name: "empty list delivers any live", allow: []string{}, from: "a", to: "b", wantAction: RouteDeliver},
		{name: "whitespace-only list fails closed", allow: []string{"   "}, from: "a", to: "b", wantAction: RouteDecline, wantReason: DeclineNotAllowed},
		{name: "single empty entry fails closed", allow: []string{""}, from: "a", to: "b", wantAction: RouteDecline, wantReason: DeclineNotAllowed},
		{name: "valid pair delivers", allow: []string{"a->b"}, from: "a", to: "b", wantAction: RouteDeliver},
		{name: "reversed pair declines", allow: []string{"b->a"}, from: "a", to: "b", wantAction: RouteDecline, wantReason: DeclineNotAllowed},
		{name: "duplicate pairs behave as one", allow: []string{"a->b", "a->b"}, from: "a", to: "b", wantAction: RouteDeliver},
		{name: "duplicate pairs decline other pair", allow: []string{"a->b", "a->b"}, from: "a", to: "c", wantAction: RouteDecline, wantReason: DeclineNotAllowed},
		{name: "partial malformed delivers valid pair", allow: []string{"", "nook", "a->", "->b", "a->b"}, from: "a", to: "b", wantAction: RouteDeliver},
		{name: "partial malformed declines other pair", allow: []string{"", "nook", "a->", "->b", "a->b"}, from: "a", to: "c", wantAction: RouteDecline, wantReason: DeclineNotAllowed},
		{name: "oversized entry fails closed", allow: []string{strings.Repeat("x", 10000)}, from: "a", to: "b", wantAction: RouteDecline, wantReason: DeclineNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Allow = tc.allow
			d := RouteAsk(p, RouteInput{FromRole: tc.from, ToRole: tc.to, TargetRunning: true})
			if d.Action != tc.wantAction {
				t.Fatalf("Action = %q, want %q (%+v)", d.Action, tc.wantAction, d)
			}
			if d.Action == RouteDecline && d.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", d.Reason, tc.wantReason)
			}
		})
	}

	// Ordering pins: cycle and ask quota win over the fail-closed guard
	// because the guard sits after those checks.
	p := base
	p.Allow = []string{"reviewer--auditor"}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true, Cycle: true}); d.Reason != DeclineCycle {
		t.Fatalf("cycle must win over fail-closed guard: %+v", d)
	}
	if d := RouteAsk(p, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true, AsksUsedByTask: 4}); d.Reason != DeclineQuotaExceeded {
		t.Fatalf("quota must win over fail-closed guard: %+v", d)
	}
}

// TestRouteAskModeDenialPins is the same-class sweep: mode parsing already
// fails closed (unknown mode -> DeclineInvalid), so parseAllowPairs/RouteAsk
// is the only fail-open parse site in the package.
func TestRouteAskModeDenialPins(t *testing.T) {
	if d := RouteAsk(RoutingPolicy{Mode: "bogus"}, RouteInput{FromRole: "a", ToRole: "b", TargetRunning: true}); d.Action != RouteDecline || d.Reason != DeclineInvalid {
		t.Fatalf("bogus mode must decline: %+v", d)
	}
}
