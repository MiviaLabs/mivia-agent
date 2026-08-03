package agentmsg

import (
	"fmt"
	"strings"
)

// RouteAction is the policy decision for one ask.
type RouteAction string

const (
	RouteDeliver RouteAction = "deliver" // live target in same run
	RouteSpawn   RouteAction = "spawn"   // referral-as-spawn (non-blocking only)
	RouteDecline RouteAction = "decline"
)

// Decline reasons are stable tool-result vocabulary (not free-form chat).
const (
	DeclineParentModeUnimplemented = "parent_mode_unimplemented"
	DeclineNoSuchRole              = "no_such_role"
	DeclineNotAllowed              = "not_allowed"
	DeclineQuotaExceeded           = "quota_exceeded"
	DeclineDepthExceeded           = "depth_exceeded"
	DeclineCycle                   = "cycle"
	// DeclineTargetNotRunning reports an ask declined because its target is not
	// live (no same-run task with that role). Its value is unified with the
	// mid-park terminal decline reason (agentmsg.DeclineReasonResponderTerminal):
	// both describe a terminal/unavailable target, so one reason string is
	// load-bearing for callers that must not branch on the distinction. Prefer
	// agentmsg.DeclineReasonTargetTerminal for new code.
	DeclineTargetNotRunning   = DeclineReasonTargetTerminal
	DeclineSpawnQuotaExceeded = "spawn_quota_exceeded"
	DeclineInvalid            = "invalid"
)

// RoutingPolicy is the pure [subagents.messaging.routing] decision input.
type RoutingPolicy struct {
	Mode                    string   // "policy" | "parent"
	MaxAsksPerTask          int      // default 4
	MaxReferralDepth        int      // default 2
	Allow                   []string // "from->to"; empty = any live pair
	MaxReferralSpawnsPerRun int      // default 4
}

// RouteInput is one ask evaluation. Callers resolve live targets and quotas.
type RouteInput struct {
	FromRole string
	ToRole   string
	// Blocking is true when the asker waits (wait_seconds > 0).
	Blocking bool
	// TargetRunning is true when a same-run task with ToRole is live.
	TargetRunning bool
	// AsksUsedByTask is asks already posted by the asker task this attempt.
	AsksUsedByTask int
	// ReferralSpawnsUsed is referral-as-spawn count for the run so far.
	ReferralSpawnsUsed int
	// ChainDepth is ancestor hop count for this ask (0 = first hop).
	ChainDepth int
	// Cycle is true when ToRole already appears in the ancestor chain.
	Cycle bool
}

// RouteDecision is the pure policy result.
type RouteDecision struct {
	Action RouteAction
	Reason string // set when Action == RouteDecline
}

// RouteAsk applies referral policy. No I/O, no side effects.
func RouteAsk(policy RoutingPolicy, in RouteInput) RouteDecision {
	mode := strings.TrimSpace(strings.ToLower(policy.Mode))
	if mode == "" {
		mode = "policy"
	}
	if mode == "parent" {
		return RouteDecision{Action: RouteDecline, Reason: DeclineParentModeUnimplemented}
	}
	if mode != "policy" {
		return RouteDecision{Action: RouteDecline, Reason: DeclineInvalid}
	}
	from := strings.TrimSpace(in.FromRole)
	to := strings.TrimSpace(in.ToRole)
	if to == "" {
		return RouteDecision{Action: RouteDecline, Reason: DeclineNoSuchRole}
	}
	if from == "" {
		return RouteDecision{Action: RouteDecline, Reason: DeclineInvalid}
	}
	maxAsks := policy.MaxAsksPerTask
	if maxAsks <= 0 {
		maxAsks = 4
	}
	if in.AsksUsedByTask >= maxAsks {
		return RouteDecision{Action: RouteDecline, Reason: DeclineQuotaExceeded}
	}
	maxDepth := policy.MaxReferralDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if in.ChainDepth >= maxDepth {
		return RouteDecision{Action: RouteDecline, Reason: DeclineDepthExceeded}
	}
	if in.Cycle {
		return RouteDecision{Action: RouteDecline, Reason: DeclineCycle}
	}

	allow := parseAllowPairs(policy.Allow)
	explicitAllow := len(allow) > 0
	pairOK := allowPair(allow, from, to)

	if in.TargetRunning {
		// Empty allow = any live same-run role; non-empty requires pair.
		if explicitAllow && !pairOK {
			return RouteDecision{Action: RouteDecline, Reason: DeclineNotAllowed}
		}
		return RouteDecision{Action: RouteDeliver}
	}

	// Target not running.
	if in.Blocking {
		return RouteDecision{Action: RouteDecline, Reason: DeclineTargetNotRunning}
	}
	// Referral-as-spawn requires explicit allow pair (never empty-list default).
	if !explicitAllow || !pairOK {
		return RouteDecision{Action: RouteDecline, Reason: DeclineNotAllowed}
	}
	maxSpawn := policy.MaxReferralSpawnsPerRun
	if maxSpawn <= 0 {
		maxSpawn = 4
	}
	if in.ReferralSpawnsUsed >= maxSpawn {
		return RouteDecision{Action: RouteDecline, Reason: DeclineSpawnQuotaExceeded}
	}
	return RouteDecision{Action: RouteSpawn}
}

func parseAllowPairs(raw []string) map[string]bool {
	out := map[string]bool{}
	for _, e := range raw {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		// Normalize "a->b" with optional spaces.
		parts := strings.Split(e, "->")
		if len(parts) != 2 {
			continue
		}
		a := strings.TrimSpace(parts[0])
		b := strings.TrimSpace(parts[1])
		if a == "" || b == "" {
			continue
		}
		out[a+"->"+b] = true
	}
	return out
}

func allowPair(allow map[string]bool, from, to string) bool {
	if len(allow) == 0 {
		return true
	}
	return allow[from+"->"+to]
}

// AllowPairKey formats a pair for config/docs.
func AllowPairKey(from, to string) string {
	return fmt.Sprintf("%s->%s", strings.TrimSpace(from), strings.TrimSpace(to))
}
