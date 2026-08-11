package config

// resolveMessagingConfig fills zero fields with DefaultMessagingConfig.
// Messaging is always enabled; any TOML enabled= value is ignored.
func resolveMessagingConfig(cfg MessagingConfig) MessagingConfig {
	// Drop any kill-switch value so callers never observe Enabled=false.
	cfg.Enabled = nil
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = DefaultMessagingConfig.MaxBodyBytes
	}
	if cfg.MaxMessagesPerTask == 0 {
		cfg.MaxMessagesPerTask = DefaultMessagingConfig.MaxMessagesPerTask
	}
	if cfg.MailboxCapacity == 0 {
		cfg.MailboxCapacity = DefaultMessagingConfig.MailboxCapacity
	}
	if cfg.MaxPendingQuestions == 0 {
		cfg.MaxPendingQuestions = DefaultMessagingConfig.MaxPendingQuestions
	}
	// nil = default (300s); an explicit 0 is meaningful (watchdog disabled)
	// and must not be overwritten.
	if cfg.SteerWatchdogSeconds == nil {
		cfg.SteerWatchdogSeconds = intPtr(*DefaultMessagingConfig.SteerWatchdogSeconds)
	}
	cfg.Routing = resolveMessagingRouting(cfg.Routing)
	return cfg
}

func resolveMessagingRouting(cfg MessagingRoutingConfig) MessagingRoutingConfig {
	if cfg.Mode == "" {
		cfg.Mode = DefaultMessagingConfig.Routing.Mode
	}
	if cfg.MaxAsksPerTask == 0 {
		cfg.MaxAsksPerTask = DefaultMessagingConfig.Routing.MaxAsksPerTask
	}
	if cfg.MaxReferralDepth == 0 {
		cfg.MaxReferralDepth = DefaultMessagingConfig.Routing.MaxReferralDepth
	}
	if cfg.MaxReferralSpawnsPerRun == 0 {
		cfg.MaxReferralSpawnsPerRun = DefaultMessagingConfig.Routing.MaxReferralSpawnsPerRun
	}
	return cfg
}
