package config

// EffectiveOutputTokens returns the response allowance for one request: the
// completion size asked for on the wire, and the reserve subtracted from the
// context window to derive the prompt budget. Those two must stay in lockstep
// - providers validate input_tokens + max_tokens <= context_window - so this
// is the single place both are decided. A nil result means no ceiling applies.
//
// An EXPLICIT request ([chat] max_tokens) is authoritative up to the model's
// own ceiling. An UNSET request falls back to the model ceiling capped at
// DefaultOutputReserveTokens, because a model's max_output_tokens is a
// per-response maximum rather than a sensible per-request default; see that
// constant for the prompt-budget damage the uncapped fallback caused.
func EffectiveOutputTokens(profile ModelSpec, requested *int) *int {
	ceiling := profile.MaxOutputTokens
	if ceiling < 0 {
		ceiling = 0
	}
	if requested != nil && *requested > 0 {
		limit := *requested
		if ceiling > 0 && limit > ceiling {
			limit = ceiling
		}
		return clampReserveToWindow(profile, limit)
	}
	if ceiling <= 0 {
		return nil
	}
	limit := ceiling
	if limit > DefaultOutputReserveTokens {
		limit = DefaultOutputReserveTokens
	}
	return clampReserveToWindow(profile, limit)
}

// clampReserveToWindow keeps the response allowance inside the declared
// context window. A reserve larger than the whole window is unsatisfiable:
// providers validate input_tokens + max_tokens <= context_window, so such a
// request is rejected outright rather than merely leaving no prompt room.
// Validated config never reaches this (load rejects windows at or below the
// reserve), so this guards hand-built profiles and explicit operator requests.
func clampReserveToWindow(profile ModelSpec, limit int) *int {
	if profile.ContextWindowTokens > 0 && limit > profile.ContextWindowTokens {
		limit = profile.ContextWindowTokens
	}
	return &limit
}

func promptCap(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// EffectivePromptTokens computes the local prompt budget after reserving the
// configured completion allowance and applying an optional operator/session cap.
//
// The returned budget is never negative. When the completion reserve consumes
// or exceeds the whole context window, the budget is 0: the guard fails closed
// and the provider-side prompt-too-long recovery is the backstop. Validated
// config cannot reach that state - load rejects context_window_tokens below
// minContextWindowTokens (1024) and windows at or below the max_tokens
// reserve - so 0 only surfaces for hand-constructed profiles in tests and
// runtime bindings.
//
// A non-positive ContextWindowTokens is the undeclared/unrecognized window
// fallback: it fails closed at UnknownContextWindowTokens (128k). Validated config
// also never reaches that branch (load rejects windows below 1024); it exists
// for hand-constructed profiles in tests and legacy runtime bindings.
func EffectivePromptTokens(profile ModelSpec, maxTokens *int, operatorCap, requested int) int {
	capacity := profile.ContextWindowTokens
	if capacity <= 0 {
		capacity = UnknownContextWindowTokens
	}
	if reserve := EffectiveOutputTokens(profile, maxTokens); reserve != nil {
		capacity -= *reserve
	}
	// Clamp at 0: a reserve >= window leaves no prompt capacity. Returning a
	// negative budget would surface as a "budget must be positive" plan
	// failure or silently disable the pruning guard.
	if capacity < 0 {
		capacity = 0
	}
	if operatorCap > 0 && operatorCap < capacity {
		capacity = operatorCap
	}
	if requested > 0 && requested < capacity {
		capacity = requested
	}
	return capacity
}
