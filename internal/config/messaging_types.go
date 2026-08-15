package config

// MessagingConfig is the [subagents.messaging] surface for typed, budgeted
// agent messages. Messaging is always on; Enabled is accepted in TOML for
// forward compatibility but ignored (IsEnabled always returns true).
type MessagingConfig struct {
	// Enabled is ignored: messaging is always enabled. Retained so older
	// configs with enabled=true|false still parse without error.
	Enabled *bool `toml:"enabled"`
	// MaxBodyBytes is the per-message inline body budget. Default 2048.
	MaxBodyBytes int `toml:"max_body_bytes"`
	// MaxMessagesPerTask is the child upstream send quota per attempt. Default 32.
	MaxMessagesPerTask int `toml:"max_messages_per_task"`
	// MailboxCapacity is parent→child mailbox depth (phase 03). Default 32.
	MailboxCapacity int `toml:"mailbox_capacity"`
	// MaxPendingQuestions is RESERVED and a no-op: the effective value is always
	// 1. Exactly one park per task is structurally enforced by the question
	// registry (one pendingQuestion per runID/taskID key) plus the awaiting_input
	// single-bit ledger status; N>1 is unsupported. The field still parses from
	// TOML (and the config resolver still fills the default of 1) so existing
	// configs load unchanged, but nothing reads it for behavior.
	MaxPendingQuestions int `toml:"max_pending_questions"`
	// SteerWatchdogSeconds: nil = default (300s); explicit 0 = disabled
	// (unbounded); positive = seconds.
	SteerWatchdogSeconds *int `toml:"steer_watchdog_seconds"`
	// Routing is parent-side Ask referral policy (plan 53.04). Always active.
	Routing MessagingRoutingConfig `toml:"routing"`
}

// MessagingRoutingConfig is [subagents.messaging.routing] for peer referral.
// mode "policy" is implemented; "parent" is declared but unimplemented.
type MessagingRoutingConfig struct {
	// Mode is "policy" (default) or "parent" (unimplemented).
	Mode string `toml:"mode"`
	// MaxAsksPerTask bounds UNANSWERED asks posted by one task. Default 4.
	// Semantics: the slot is released when an ask is answered or sealed.
	MaxAsksPerTask int `toml:"max_asks_per_task"`
	// MaxReferralDepth is max hops in an ask chain (A→B→C = 2). Default 2.
	MaxReferralDepth int `toml:"max_referral_depth"`
	// Allow is "from_role->to_role" pairs. Empty = any live same-run role;
	// referral-as-spawn always requires an explicit pair.
	Allow []string `toml:"allow"`
	// MaxReferralSpawnsPerRun caps referral-as-spawn. Default 4.
	MaxReferralSpawnsPerRun int `toml:"max_referral_spawns_per_run"`
}

// IsEnabled always returns true. Messaging cannot be disabled (product
// decision 2026-08-03); the TOML enabled field is ignored if present.
func (m MessagingConfig) IsEnabled() bool {
	return true
}

// SteerWatchdogSecondsResolved returns the effective steer watchdog interval
// in seconds: nil → the default 300, an explicit 0 → disabled (unbounded),
// otherwise the configured value. Single source of truth for the CLI handler
// construction sites (plan 54 §4.5); the config-layer resolver
// (resolveMessagingConfig) fills nil the same way, so this is idempotent on
// both resolved and raw configs.
func (m MessagingConfig) SteerWatchdogSecondsResolved() int {
	if m.SteerWatchdogSeconds == nil {
		return *DefaultMessagingConfig.SteerWatchdogSeconds
	}
	return *m.SteerWatchdogSeconds
}
