package config

// EffectiveOutputTokens returns the tighter positive model/session response
// ceiling. A nil result means neither layer configured a ceiling.
func EffectiveOutputTokens(profile ModelSpec, requested *int) *int {
	limit := profile.MaxOutputTokens
	if limit < 0 {
		limit = 0
	}
	if requested != nil && *requested > 0 && (limit == 0 || *requested < limit) {
		limit = *requested
	}
	if limit <= 0 {
		return nil
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
// A non-positive ContextWindowTokens is the legacy "no declared window"
// default: it falls back to maxContextWindowTokens (10M). Validated config
// also never reaches that branch (load rejects windows below 1024); it exists
// so profiles built without a declared window keep their historical capacity.
func EffectivePromptTokens(profile ModelSpec, maxTokens *int, operatorCap, requested int) int {
	capacity := profile.ContextWindowTokens
	if capacity <= 0 {
		capacity = maxContextWindowTokens
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
