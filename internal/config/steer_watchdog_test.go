package config

import "testing"

// swPtr returns a pointer to v. Local helper: named distinctly from the
// package-level test helper ptr (prompt_budget_test.go) to avoid redeclaration.
func swPtr(v int) *int { return &v }

// TestMessagingConfigSteerWatchdogUnsetDefaults300 verifies that a
// MessagingConfig with SteerWatchdogSeconds nil resolves to the default 300.
func TestMessagingConfigSteerWatchdogUnsetDefaults300(t *testing.T) {
	cfg := resolveMessagingConfig(MessagingConfig{})
	if cfg.SteerWatchdogSeconds == nil {
		t.Fatalf("SteerWatchdogSeconds: got nil, want *300")
	}
	if got := *cfg.SteerWatchdogSeconds; got != 300 {
		t.Fatalf("SteerWatchdogSeconds: got %d, want 300", got)
	}
}

// TestMessagingConfigSteerWatchdogZeroDisabled verifies that an explicit 0 is
// preserved through resolution (watchdog disabled), not replaced by the default.
func TestMessagingConfigSteerWatchdogZeroDisabled(t *testing.T) {
	cfg := resolveMessagingConfig(MessagingConfig{SteerWatchdogSeconds: swPtr(0)})
	if cfg.SteerWatchdogSeconds == nil {
		t.Fatalf("SteerWatchdogSeconds: got nil, want *0")
	}
	if got := *cfg.SteerWatchdogSeconds; got != 0 {
		t.Fatalf("SteerWatchdogSeconds: got %d, want 0", got)
	}
}

// TestMessagingConfigSteerWatchdogCustom verifies that a positive custom value
// is preserved through resolution.
func TestMessagingConfigSteerWatchdogCustom(t *testing.T) {
	cfg := resolveMessagingConfig(MessagingConfig{SteerWatchdogSeconds: swPtr(120)})
	if cfg.SteerWatchdogSeconds == nil {
		t.Fatalf("SteerWatchdogSeconds: got nil, want *120")
	}
	if got := *cfg.SteerWatchdogSeconds; got != 120 {
		t.Fatalf("SteerWatchdogSeconds: got %d, want 120", got)
	}
}

// TestMessagingConfigSteerWatchdogSecondsResolved verifies the single source
// of truth for the CLI handler construction sites: nil → default 300, an
// explicit 0 → disabled (0), a positive value → itself. The method must be
// idempotent on both raw and already-resolved configs.
func TestMessagingConfigSteerWatchdogSecondsResolved(t *testing.T) {
	cases := []struct {
		name string
		cfg  MessagingConfig
		want int
	}{
		{"nil_defaults_to_300", MessagingConfig{}, 300},
		{"explicit_zero_disabled", MessagingConfig{SteerWatchdogSeconds: swPtr(0)}, 0},
		{"explicit_120", MessagingConfig{SteerWatchdogSeconds: swPtr(120)}, 120},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.SteerWatchdogSecondsResolved(); got != tc.want {
				t.Fatalf("SteerWatchdogSecondsResolved() = %d, want %d", got, tc.want)
			}
			// Idempotent on the resolved form too.
			resolved := resolveMessagingConfig(tc.cfg)
			if got := resolved.SteerWatchdogSecondsResolved(); got != tc.want {
				t.Fatalf("resolved SteerWatchdogSecondsResolved() = %d, want %d", got, tc.want)
			}
		})
	}
}
