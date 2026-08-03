package agentmsg

import "testing"

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
	// allowPair empty map returns true via len==0 path when parse yields empty.
	d = RouteAsk(RoutingPolicy{Mode: "policy", Allow: []string{"", "bad"}}, RouteInput{
		FromRole: "a", ToRole: "b", TargetRunning: true,
	})
	if d.Action != RouteDeliver {
		t.Fatalf("empty parsed allow = any live: %+v", d)
	}
}
